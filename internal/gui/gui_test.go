package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
