package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubAsker struct {
	reply string
	err   error
	seen  chan string
}

func (s *stubAsker) Ask(_ context.Context, in string) (string, error) {
	if s.seen != nil {
		s.seen <- in
	}
	return s.reply, s.err
}

func newTestServer(t *testing.T, a Asker) *Server {
	t.Helper()
	s, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// This endpoint can run shell commands, so the two things that gate it are the
// two things worth testing hardest.
//
// Loopback alone is not a boundary: any page in any tab can post a form at
// 127.0.0.1 without being able to read the reply, and that is already enough
// when the far end runs commands. The token is what a page from elsewhere cannot
// know, and the local check is what stops it being reachable from the network at
// all. Both, never either.
func TestAWindowWithoutTheTokenIsRefused(t *testing.T) {
	s := newTestServer(t, &stubAsker{reply: "hi"})

	for _, c := range []struct {
		name, query string
		cookie      string
	}{
		{"no token at all", "", ""},
		{"empty token", "?t=", ""},
		{"wrong token", "?t=deadbeef", ""},
		{"wrong cookie", "", "deadbeef"},
		// The shape a guess would take: right length, wrong value.
		{"same length, wrong value", "?t=" + strings.Repeat("a", 48), ""},
	} {
		req := httptest.NewRequest(http.MethodGet, "/"+c.query, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "freya_token", Value: c.cookie})
		}
		w := httptest.NewRecorder()
		s.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Errorf("%s: reached the handler", c.name)
		})).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", c.name, w.Code)
		}
	}
}

// Right token, wrong machine. The listener binds 127.0.0.1 so this should be
// unreachable anyway; the check is the second lock, because a listener is one
// edit away from being 0.0.0.0 and this is a shell.
func TestATokenFromElsewhereIsStillRefused(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	req := httptest.NewRequest(http.MethodGet, "/?t="+s.token, nil)
	req.RemoteAddr = "192.168.1.50:5000"
	w := httptest.NewRecorder()
	reached := false
	s.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).ServeHTTP(w, req)
	if reached || w.Code != http.StatusForbidden {
		t.Errorf("a remote address with the right token got %d (reached=%v)", w.Code, reached)
	}
}

// The right token gets in, and is moved to a cookie so it stops riding in the
// query string of every later request.
func TestTheRightTokenGetsInAndIsPutInACookie(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	req := httptest.NewRequest(http.MethodGet, "/?t="+s.token, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	reached := false
	s.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).ServeHTTP(w, req)
	if !reached {
		t.Fatalf("the right token was refused: %d", w.Code)
	}
	var got *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "freya_token" {
			got = c
		}
	}
	if got == nil {
		t.Fatal("no cookie was set, so the token stays in the URL forever")
	}
	if !got.HttpOnly {
		t.Error("the cookie is readable from script")
	}
	if got.SameSite != http.SameSiteStrictMode {
		t.Error("the cookie is not SameSite=Strict, so another site's request carries it")
	}
}

// Two turns at once would interleave into one archive and one cached prefix,
// which internal/memory/journal.go explains is how both get corrupted. The
// window refuses the second rather than queueing it, so the person typing finds
// out immediately instead of watching a reply arrive against the wrong question.
func TestASecondTurnIsRefusedWhileOneIsRunning(t *testing.T) {
	release := make(chan struct{})
	s := newTestServer(t, askerFunc(func(context.Context, string) (string, error) {
		<-release
		return "done", nil
	}))

	first := s.postAsk(t, "one")
	if first != http.StatusAccepted {
		t.Fatalf("the first turn got %d", first)
	}
	// Wait for the goroutine to actually be in the asker.
	deadline := time.Now().Add(2 * time.Second)
	for !s.Busy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := s.postAsk(t, "two"); got != http.StatusConflict {
		t.Errorf("a second turn got %d, want 409", got)
	}
	close(release)
}

// Nothing wired up must say so rather than panicking on a nil interface.
func TestNoAskerIsAnAnswerRatherThanACrash(t *testing.T) {
	s := newTestServer(t, nil)
	if got := s.postAsk(t, "hello"); got != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", got)
	}
}

// An empty message is refused before a turn starts, or she is asked to answer
// whitespace and the round is spent finding that out.
func TestAnEmptyMessageStartsNoTurn(t *testing.T) {
	s := newTestServer(t, &stubAsker{reply: "x"})
	for _, body := range []string{`{"text":""}`, `{"text":"   "}`, `{"text":"\n"}`} {
		req := httptest.NewRequest(http.MethodPost, "/ask?t="+s.token, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		s.guard(http.HandlerFunc(s.ask)).ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s got %d, want 400", body, w.Code)
		}
	}
}

// A window that stopped reading must not be able to stall the turn producing the
// events. Minimised, asleep or gone, it is the same to the sender — and losing a
// thought bubble is nothing next to hanging her mid-task.
func TestAStalledWindowCannotStallHer(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	stuck := make(chan Event) // unbuffered and never read
	s.mu.Lock()
	s.subs[stuck] = struct{}{}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			s.Emit(Event{Kind: "thought", Text: fmt.Sprintf("%d", i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Emit blocked on a window that was not reading")
	}
}

// Every open window sees the turn, because the trace is the point.
func TestEveryOpenWindowSeesTheSameEvent(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	a, b := make(chan Event, 4), make(chan Event, 4)
	s.mu.Lock()
	s.subs[a], s.subs[b] = struct{}{}, struct{}{}
	s.mu.Unlock()

	s.Emit(Event{Kind: "tool", Name: "browser_open"})
	for name, ch := range map[string]chan Event{"first": a, "second": b} {
		select {
		case e := <-ch:
			if e.Name != "browser_open" {
				t.Errorf("%s window got %+v", name, e)
			}
		case <-time.After(time.Second):
			t.Errorf("%s window got nothing", name)
		}
	}
}

// The stream has to be text/event-stream and has to flush, or the window shows
// nothing until the turn ends — which is the whole thing it exists to avoid.
func TestTheStreamIsSentAsEventsAndFlushed(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/events?t="+s.token, nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.events(w, req)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.subs)
		s.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Emit(Event{Kind: "reply", Text: "the answer"})
	time.Sleep(80 * time.Millisecond)
	cancel()
	wg.Wait()

	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content type is %q", ct)
	}
	body := w.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), "data: ") {
		t.Fatalf("not an SSE frame: %q", body)
	}
	var got Event
	line := strings.TrimPrefix(strings.SplitN(strings.TrimSpace(body), "\n", 2)[0], "data: ")
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("frame is not JSON: %v (%q)", err, line)
	}
	if got.Text != "the answer" {
		t.Errorf("got %+v", got)
	}
}

// Every window gets its own token, so one being seen somewhere does not open the
// next one.
func TestEachRunMintsItsOwnToken(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		s := newTestServer(t, nil)
		if len(s.token) < 32 {
			t.Fatalf("token is only %d characters", len(s.token))
		}
		if seen[s.token] {
			t.Fatal("two runs minted the same token")
		}
		seen[s.token] = true
	}
}

// The page is served from the binary, so there is nothing to install and nothing
// to go missing.
func TestTheWindowIsInsideTheBinary(t *testing.T) {
	for _, name := range []string{"assets/index.html", "assets/app.css", "assets/app.js"} {
		b, err := assets.ReadFile(name)
		if err != nil {
			t.Errorf("%s is not embedded: %v", name, err)
			continue
		}
		if len(b) < 200 {
			t.Errorf("%s is suspiciously small (%d bytes)", name, len(b))
		}
	}
}

type askerFunc func(context.Context, string) (string, error)

func (f askerFunc) Ask(ctx context.Context, in string) (string, error) { return f(ctx, in) }

// postAsk runs one /ask through the guard and returns the status.
func (s *Server) postAsk(t *testing.T, text string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text})
	req := httptest.NewRequest(http.MethodPost, "/ask?t="+s.token, strings.NewReader(string(body)))
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.guard(http.HandlerFunc(s.ask)).ServeHTTP(w, req)
	_, _ = io.Copy(io.Discard, w.Body)
	return w.Code
}

// The archive is a flat log with a session id on every turn — the right shape
// for what it is, and the wrong shape for a list of conversations.
func TestTheRailGroupsTurnsIntoConversations(t *testing.T) {
	got := conversations([]Turn{
		{Sess: "a", Role: "user", Text: "tidy my downloads", At: "2026-08-21 09:00"},
		{Sess: "a", Role: "assistant", Text: "done", At: "2026-08-21 09:01"},
		{Sess: "b", Role: "user", Text: "what is the price cap", At: "2026-08-21 10:00"},
		{Sess: "b", Role: "assistant", Text: "£1,663", At: "2026-08-21 10:02"},
		{Sess: "b", Role: "user", Text: "and last year", At: "2026-08-21 10:05"},
	})
	if len(got) != 2 {
		t.Fatalf("want 2 conversations, got %d", len(got))
	}
	// Newest first, because that is the one being continued.
	if got[0].ID != "b" {
		t.Errorf("newest is %q, want b", got[0].ID)
	}
	// Titled by the FIRST thing the user said, not the last: it is what they
	// will recognise, and a generated title would be a model call to name
	// something already in their own words.
	if got[0].Title != "what is the price cap" {
		t.Errorf("title is %q", got[0].Title)
	}
	if got[0].Turns != 3 {
		t.Errorf("counted %d turns, want 3", got[0].Turns)
	}
	// The timestamp shown is the latest, so the rail reads as "when did I last
	// touch this" rather than "when did I start it".
	if got[0].At != "2026-08-21 10:05" {
		t.Errorf("time is %q, want the latest", got[0].At)
	}
}

// A session with no user turn is a background job or a watcher note. It still
// belongs in the list, and it must not appear as a blank row.
func TestASessionWithNothingSaidIsStillNamed(t *testing.T) {
	got := conversations([]Turn{{Sess: "j1", Role: "assistant", Text: "finished that job", At: "x"}})
	if len(got) != 1 || got[0].Title == "" {
		t.Fatalf("got %+v", got)
	}
}

// Turns with no session belong to nothing and must not invent a conversation.
func TestTurnsWithNoSessionAreSkipped(t *testing.T) {
	if got := conversations([]Turn{{Role: "user", Text: "orphan"}}); len(got) != 0 {
		t.Errorf("an orphan turn produced %d conversations", len(got))
	}
}

// Titles are cut on a word, and only when they need cutting.
func TestATitleIsCutOnAWord(t *testing.T) {
	short := "tidy my downloads"
	if got := firstLine(short, 60); got != short {
		t.Errorf("a short title was altered: %q", got)
	}
	if got := firstLine("first line\nsecond line", 60); got != "first line" {
		t.Errorf("multi-line title became %q", got)
	}
	long := firstLine(strings.Repeat("word ", 40), 30)
	if len([]rune(long)) > 31 || !strings.HasSuffix(long, "…") {
		t.Errorf("long title became %q (%d runes)", long, len([]rune(long)))
	}
	if strings.Contains(strings.TrimSuffix(long, "…"), "  ") {
		t.Errorf("cut mid-word: %q", long)
	}
}

// With nothing wired up the rail is empty rather than an error, because a
// missing archive is not a reason for the window to stop working.
func TestNoArchiveMeansAnEmptyRailNotAFailure(t *testing.T) {
	s := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/history?t="+s.token, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.guard(http.HandlerFunc(s.history)).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d", w.Code)
	}
	var list []Conversation
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("got %d conversations from no archive", len(list))
	}
}

// ---- speaking and listening ----------------------------------------------

type stubControls struct {
	talkErr error
	talked  int
	voice   bool
	stopped int
}

func (c *stubControls) Talk() error { c.talked++; return c.talkErr }
func (c *stubControls) Voice(on bool) bool {
	c.voice = on
	return c.voice
}
func (c *stubControls) Stop() string { c.stopped++; return "stopped what she was doing" }

func post(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path+"?t="+s.token, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.guard(http.HandlerFunc(s.voiceHandler)).ServeHTTP(w, req)
	return w
}

// The microphone button presses HER pipeline. The alternative — getUserMedia in
// the page — is a second recorder fighting the first for one device, and audio
// from a web page has walked around the voiceprint that decides whose
// instructions she takes.
func TestTheWindowPressesHerMicrophoneRatherThanItsOwn(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	c := &stubControls{}
	s.SetControls(c)

	if w := post(t, s, "/voice/talk"); w.Code != http.StatusOK {
		t.Fatalf("talk returned %d: %s", w.Code, w.Body.String())
	}
	if c.talked != 1 {
		t.Errorf("Talk called %d times", c.talked)
	}
}

// A second press while the first is still recording is a refusal with a reason,
// not a silent nothing. Silence here is how a button gets pressed four more
// times.
func TestASecondPressIsRefusedWithAReason(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	s.SetControls(&stubControls{talkErr: errors.New("the microphone is in use by the push-to-talk")})

	w := post(t, s, "/voice/talk")
	if w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "microphone") {
		t.Errorf("the refusal does not say why: %q", w.Body.String())
	}
}

// Voice reports what it IS afterwards, not what was asked for. Echoing the
// request would make the button claim voice is on in a session that has no
// synthesiser.
func TestTheVoiceToggleReportsTheStateNotTheRequest(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	s.SetControls(&stubControls{})

	var got struct {
		Voice bool `json:"voice"`
	}
	w := post(t, s, "/voice/on")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Voice {
		t.Error("turning voice on reported it off")
	}
	w = post(t, s, "/voice/off")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Voice {
		t.Error("turning voice off reported it on")
	}
}

func TestStopGoesThroughToHerInterrupt(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	c := &stubControls{}
	s.SetControls(c)

	w := post(t, s, "/voice/stop")
	if w.Code != http.StatusOK || c.stopped != 1 {
		t.Fatalf("code=%d stopped=%d", w.Code, c.stopped)
	}
	if !strings.Contains(w.Body.String(), "stopped what she was doing") {
		t.Errorf("the window was not told what was stopped: %s", w.Body.String())
	}
}

// A session with no voice must say so rather than accept the press and do
// nothing, which is the same failure as the silent second press one level up.
func TestNoVoiceSaysSoRatherThanSwallowingThePress(t *testing.T) {
	s := newTestServer(t, &stubAsker{})

	w := post(t, s, "/voice/talk")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", w.Code)
	}
}

// GET must not reach any of this. These endpoints start a recording and cancel
// running work; a page elsewhere that can make the browser issue a GET at
// 127.0.0.1 must not be able to trigger either.
func TestVoiceControlsRefuseGET(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	c := &stubControls{}
	s.SetControls(c)

	for _, path := range []string{"/voice/talk", "/voice/on", "/voice/stop"} {
		req := httptest.NewRequest(http.MethodGet, path+"?t="+s.token, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		s.guard(http.HandlerFunc(s.voiceHandler)).ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s returned %d, want 405", path, w.Code)
		}
	}
	if c.talked != 0 || c.stopped != 0 {
		t.Error("a GET reached the microphone or the interrupt")
	}
}

// And the token still gates them, like everything else. Worth pinning
// separately: these were added after the guard was written and a new mux entry
// that forgets s.guard is invisible until somebody looks.
func TestVoiceControlsAreBehindTheToken(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	c := &stubControls{}
	s.SetControls(c)

	req := httptest.NewRequest(http.MethodPost, "/voice/talk", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.guard(http.HandlerFunc(s.voiceHandler)).ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Error("the microphone was reachable without the token")
	}
	if c.talked != 0 {
		t.Error("Talk ran for an untokened request")
	}
}

// Every id the script reaches for must exist in the page.
//
// This is a real bug, found by looking at the window rather than at the code:
// the inspector's markup was inserted by a replacement whose anchor had moved,
// the replacement silently did nothing, and the page shipped without it. The
// script then called $('ins-voice') on null inside an async poll, the rejection
// went nowhere, and the whole activity panel was simply absent — no error, no
// blank panel, nothing to notice.
//
// The rule this pins is narrow and mechanical: if app.js names an id, index.html
// has to have it.
func TestTheScriptOnlyReachesForElementsThePageHas(t *testing.T) {
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(html)

	// $('x') and getElementById('x'), single or double quoted.
	re := regexp.MustCompile(`(?:\$|getElementById)\(\s*['"]([A-Za-z0-9_-]+)['"]\s*\)`)
	seen := map[string]bool{}
	var missing []string
	for _, m := range re.FindAllStringSubmatch(string(js), -1) {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		if !strings.Contains(page, `id="`+id+`"`) {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("app.js reaches for %d ids the page does not have: %s\n"+
			"Each one is a null dereference in whatever function touches it, and in an "+
			"async handler that failure is silent — the feature is simply not there.",
			len(missing), strings.Join(missing, ", "))
	}
	if len(seen) < 10 {
		t.Errorf("only found %d ids; the pattern has stopped matching the script", len(seen))
	}
}

// And the other direction: a control the page shows but the script never listens
// to is a button that does nothing when pressed.
//
// The first version of this only grepped app.js for the id, so a control that
// was mentioned once — in a $() that read it and never bound a handler — counted
// as wired. It also never opened index.html, so it would have passed happily for
// a button that had been deleted from the page. Both halves are checked now: the
// element exists, AND something listens to it.
func TestEveryControlInThePageIsWiredUp(t *testing.T) {
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, page := string(js), string(html)

	// id -> the event it must handle. A control with no handler is furniture.
	controls := map[string]string{
		"new-chat": "click",
		"theme":    "click",
		"inspect":  "click",
		"voice":    "click",
		"mic":      "click",
		"stop":     "click",
		"pending":  "click",
		"thread":   "click",
		"composer": "submit",
		"input":    "keydown",
	}
	for id, event := range controls {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("#%s is wired up in the script and is not in the page", id)
			continue
		}
		// Either $('x').addEventListener('click', …) directly, or through the
		// module-level alias some of them are held in (const input = $('input')).
		names := []string{`\$\(\s*['"]` + regexp.QuoteMeta(id) + `['"]\s*\)`}
		alias := regexp.MustCompile(
			`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*\$\(\s*['"]` +
				regexp.QuoteMeta(id) + `['"]\s*\)`)
		if m := alias.FindStringSubmatch(script); m != nil {
			names = append(names, `\b`+regexp.QuoteMeta(m[1]))
		}
		var wired bool
		for _, n := range names {
			if regexp.MustCompile(n + `\s*\.addEventListener\(\s*['"]` +
				regexp.QuoteMeta(event) + `['"]`).MatchString(script) {
				wired = true
				break
			}
		}
		if !wired {
			t.Errorf("#%s is in the page but nothing listens for its %s — pressing it "+
				"does nothing, silently", id, event)
		}
	}
}

// ---- the address that goes on a command line -----------------------------

// The token must never reach Chrome's argv.
//
// openWindow launches Chrome with --app=<url>, and Chrome keeps its argv.
// /proc/<pid>/cmdline is world-readable on an ordinary Linux desktop, so a token
// there is readable by every uid on the machine — and loopback is reachable by
// every uid by definition, which would leave an endpoint that runs shell
// commands effectively unauthenticated locally. She also has run_shell and a
// process-list tool, so any `ps` she runs would put the live token into
// archive.jsonl and into the next prompt sent to the model.
func TestTheHandoffAddressCarriesNoTokenAndWorksOnce(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	if _, err := s.Listen(); err != nil {
		t.Fatal(err)
	}

	url, err := s.HandoffURL()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(url, s.token) {
		t.Fatalf("the handoff address contains the token: %s", url)
	}
	nonce := url[strings.Index(url, "?h=")+3:]
	if nonce == "" || nonce == s.token {
		t.Fatalf("no usable nonce in %s", url)
	}

	// It opens the window once...
	req := httptest.NewRequest(http.MethodGet, "/?h="+nonce, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	var reached int
	s.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ })).ServeHTTP(w, req)
	if reached != 1 {
		t.Fatalf("the handoff did not admit the first request (code %d)", w.Code)
	}
	// ...and hands over the real token as a cookie, so nothing later needs a URL.
	var cookie string
	for _, c := range w.Result().Cookies() {
		if c.Name == "freya_token" {
			cookie = c.Value
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Error("the token cookie is not HttpOnly SameSite=Strict")
			}
		}
	}
	if cookie != s.token {
		t.Error("the first load did not leave the real token in a cookie")
	}

	// And it is spent. Anyone who read it off argv afterwards has nothing.
	req2 := httptest.NewRequest(http.MethodGet, "/?h="+nonce, nil)
	req2.RemoteAddr = "127.0.0.1:5000"
	w2 := httptest.NewRecorder()
	reached = 0
	s.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ })).ServeHTTP(w2, req2)
	if reached != 0 || w2.Code != http.StatusForbidden {
		t.Errorf("the handoff was reusable: code %d, reached %d", w2.Code, reached)
	}
}

func TestAnInventedHandoffOpensNothing(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	if _, err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/?h="+strings.Repeat("ab", 16), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.guard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a made-up nonce got in")
	})).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("code %d, want 403", w.Code)
	}
}

// A question that misses the stream must not be lost.
//
// Emit drops events when a subscriber's channel is full — right for a thought
// bubble, catastrophic for this one kind. A window that missed it shows nothing,
// looks perfectly healthy, and five minutes later the guard reports "declined by
// user" for something no human ever saw. A reconnect is enough to cause it.
func TestAWindowThatArrivesLateStillGetsAskedTheQuestion(t *testing.T) {
	s := newTestServer(t, &stubAsker{})

	// A window is present so Confirm asks rather than falling through...
	early := make(chan Event, 4)
	s.mu.Lock()
	s.subs[early] = struct{}{}
	s.mu.Unlock()

	go s.Confirm(context.Background(), "rm -rf notes", "tidying", "destructive", "delete 4 files")

	select {
	case <-early:
	case <-time.After(3 * time.Second):
		t.Fatal("the first window never got the question")
	}

	// ...and now it goes away and a fresh one connects, the way an EventSource
	// reconnect does.
	s.mu.Lock()
	delete(s.subs, early)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events?t="+s.token, nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:5000"
	// A locked recorder, because the handler writes from its own goroutine while
	// this one reads. httptest.ResponseRecorder is not safe for that and -race
	// says so.
	w := &lockedRecorder{ResponseRecorder: httptest.NewRecorder()}
	done := make(chan struct{})
	go func() { defer close(done); s.events(w, req) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(w.body(), "rm -rf notes") {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	t.Error("a window that connected while a question was outstanding was never " +
		"told about it; the turn dies of a silent timeout the user reads as their own refusal")
}

// A replayed question shows the time actually left, not a fresh five minutes.
//
// The stored countdown was being resent verbatim, so a window that connected
// four minutes in was handed a full clock and then had the question expire under
// it — the same lie about a silent timeout that raising the wait from ninety
// seconds existed to stop.
func TestAReplayedQuestionCountsDownFromWhereItIs(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	ch := make(chan Event, 4)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	go s.Confirm(context.Background(), "rm -rf build", "cleanup", "destructive", "")
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no question was asked")
	}

	// Wind the clock on by hand: this is the only way to observe the difference
	// without waiting minutes, and it is the deadline that is stored.
	s.mu.Lock()
	for id, q := range s.pending {
		q.deadline = time.Now().Add(42 * time.Second)
		s.pending[id] = q
	}
	s.mu.Unlock()

	out := s.outstanding()
	if len(out) != 1 {
		t.Fatalf("%d outstanding, want 1", len(out))
	}
	if out[0].Seconds > 45 || out[0].Seconds < 30 {
		t.Errorf("the replayed countdown says %ds left; about 42 was actually left. "+
			"A window arriving late is told it has the full wait and then watches the "+
			"question expire early.", out[0].Seconds)
	}
}

// The same question is also on the state poll, so a window can recover one even
// if the stream never carries it.
func TestTheStatePollCarriesAnOutstandingQuestion(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	ch := make(chan Event, 4)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	go s.Confirm(context.Background(), "sudo rm /etc/hosts", "cleanup", "destructive", "")
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no question was asked")
	}

	req := httptest.NewRequest(http.MethodGet, "/state?t="+s.token, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.guard(http.HandlerFunc(s.stateHandler)).ServeHTTP(w, req)

	var st State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Asking) != 1 || st.Asking[0].Command != "sudo rm /etc/hosts" {
		t.Errorf("the state poll does not carry the outstanding question: %+v", st.Asking)
	}
}

// Busy is the process-wide answer OR this window's own turn, never just the
// window's. A spoken turn, a REPL turn and a due self-task all set the first and
// none of them touch the second.
func TestBusyIsNotOverwrittenByTheWindowsOwnIdleness(t *testing.T) {
	s := newTestServer(t, &stubAsker{})
	s.SetState(func() State { return State{Busy: true, Model: "m"} })

	req := httptest.NewRequest(http.MethodGet, "/state?t="+s.token, nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	s.guard(http.HandlerFunc(s.stateHandler)).ServeHTTP(w, req)

	var st State
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Busy {
		t.Error("the gatherer said she was working and the server said she was idle; " +
			"the inspector then polls at the slow interval for the whole of a spoken turn")
	}
}

// lockedRecorder is an httptest.ResponseRecorder that a test can read while the
// handler is still writing to it.
type lockedRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (l *lockedRecorder) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ResponseRecorder.Write(b)
}

func (l *lockedRecorder) WriteHeader(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ResponseRecorder.WriteHeader(code)
}

func (l *lockedRecorder) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ResponseRecorder.Flush()
}

func (l *lockedRecorder) body() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ResponseRecorder.Body.String()
}

// Nothing in this window builds markup out of text.
//
// Tool names, arguments, error text, her replies and everything read back from
// the archive all pass through here. textContent everywhere makes the XSS
// boundary a one-line grep rather than a property somebody has to keep noticing;
// the last two innerHTML assignments went with the trace box they were in.
func TestTheWindowNeverBuildsMarkupFromText(t *testing.T) {
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(js), "\n") {
		code := line
		if c := strings.Index(code, "//"); c >= 0 {
			code = code[:c] // a comment may name it; an assignment may not
		}
		if strings.Contains(code, "innerHTML") || strings.Contains(code, "outerHTML") ||
			strings.Contains(code, "insertAdjacentHTML") {
			t.Errorf("app.js:%d builds markup from a string: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// Every event kind the server can emit is handled by the window.
//
// This is the test that would have caught `retry`: it has been on the wire since
// the agent could decide to go round again, and app.js had no case for it, so it
// was read off the stream and dropped on the floor. Nothing failed. The kinds are
// read from Event.Kind's own doc comment, so the list cannot drift from the type
// it documents.
func TestEveryEventKindIsHandled(t *testing.T) {
	src, err := os.ReadFile("gui.go")
	if err != nil {
		t.Fatal(err)
	}
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}

	// The comment block immediately above `Kind string`.
	i := strings.Index(string(src), "Kind string `json:\"kind\"`")
	if i < 0 {
		t.Fatal("Event.Kind has moved; this test reads the comment above it")
	}
	head := string(src)[:i]
	start := strings.LastIndex(head, "// thought")
	if start < 0 {
		t.Fatal("the kind list above Event.Kind is gone")
	}
	var kinds []string
	for _, tok := range strings.FieldsFunc(head[start:], func(r rune) bool {
		return r == '|' || r == '\n' || r == ' ' || r == '\t'
	}) {
		if tok == "//" || tok == "" {
			continue
		}
		kinds = append(kinds, tok)
	}
	if len(kinds) < 8 {
		t.Fatalf("only parsed %d kinds from the doc comment: %v", len(kinds), kinds)
	}

	var missing []string
	for _, k := range kinds {
		if !strings.Contains(string(js), "case '"+k+"':") {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the window has no case for %d event kind(s): %s\n"+
			"They are read off the stream and dropped, silently — which is what "+
			"happened to `retry` for the whole of its life.",
			len(missing), strings.Join(missing, ", "))
	}
}

// A finish is paired with its own start, not with the last one wearing the same
// name.
//
// A round's tools run on separate goroutines, so six file_read in one round is
// ordinary. Paired by name the window could not say which duration, which
// arguments and which error text belonged together — survivable while a call was
// one grey dot, and a visible lie now the row carries all three.
func TestAToolEventNamesTheInvocationNotJustTheTool(t *testing.T) {
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "e.call") {
		t.Error("app.js does not read the call id off the event, so it is back to " +
			"pairing finishes by tool name")
	}

	// And the field really is on the wire.
	yes := true
	body, err := json.Marshal(Event{Kind: "tool", Name: "file_read", Call: "x-r1-3", OK: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"call":"x-r1-3"`) {
		t.Errorf("the call id does not survive encoding: %s", body)
	}
}

// Every class the script puts on an element has a rule to draw it.
//
// Written after cutting a block of dead CSS took `.failed` and `@keyframes
// pulse` out with it. Nothing failed: error messages simply rendered as plain
// paragraphs and the "she is working" dot stopped moving, both of which look
// like a design decision until you go looking. The classes are still set, the
// element is still there, and the page is silently wrong — the same shape as
// every other bug this window has produced.
func TestEveryClassTheScriptSetsIsStyled(t *testing.T) {
	js, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	css, err := assets.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	script, sheet := string(js), string(css)

	// el('div', 'turn user'), className = '…', classList.add/toggle('…')
	var used []string
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`el\('[a-z]+',\s*'([^']*)'`),
		regexp.MustCompile(`className\s*=\s*'([^']*)'`),
		regexp.MustCompile(`classList\.(?:add|toggle)\('([^']+)'`),
	} {
		for _, m := range re.FindAllStringSubmatch(script, -1) {
			used = append(used, strings.Fields(m[1])...)
		}
	}

	// Comments stripped first. A comment that MENTIONS a class counted as a rule
	// that draws it, which is how the first version of this test passed while
	// `.failed` had been deleted — the sentence "with none of .failed's alarm"
	// was keeping it alive.
	bare := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(sheet, " ")
	styled := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.([a-zA-Z][\w-]*)`).FindAllStringSubmatch(bare, -1) {
		styled[m[1]] = true
	}

	// Names built by concatenation ('pill voice-' + st.voice) are matched by
	// their prefix, and a couple carry a second class that does the drawing.
	exempt := map[string]bool{"voice-": true, "perm-no": true, "perm-yes": true}

	seen, missing := map[string]bool{}, []string{}
	for _, c := range used {
		if c == "" || seen[c] || exempt[c] || styled[c] {
			continue
		}
		seen[c] = true
		missing = append(missing, c)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("app.js sets %d class(es) app.css never draws: %s\n"+
			"The element is there and the page is silently wrong, which reads as a "+
			"design decision rather than a missing rule.",
			len(missing), strings.Join(missing, ", "))
	}
	if len(seen)+len(styled) < 20 {
		t.Error("the patterns have stopped matching; this test is no longer checking anything")
	}
}

// And the animations: a class that asks for one it does not have just sits still.
func TestEveryAnimationTheSheetAsksForExists(t *testing.T) {
	css, err := assets.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	sheet := string(css)

	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`@keyframes\s+([\w-]+)`).FindAllStringSubmatch(sheet, -1) {
		defined[m[1]] = true
	}
	var missing []string
	for _, m := range regexp.MustCompile(`animation:\s*([\w-]+)`).FindAllStringSubmatch(sheet, -1) {
		if m[1] == "none" || defined[m[1]] {
			continue
		}
		missing = append(missing, m[1])
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("app.css animates %s, which is not defined anywhere — the element "+
			"simply sits still", strings.Join(missing, ", "))
	}
}
