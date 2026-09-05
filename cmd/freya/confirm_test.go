package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/gui"
)

// A window driven the way the real one is: over HTTP, through the token, on the
// event stream. The unexported subs map is not reachable from this package, and
// faking it would skip the part most likely to be wrong.
type testWindow struct {
	srv     *gui.Server
	base    string
	token   string
	pending chan gui.Pending
	events  chan gui.Event
}

func openTestWindow(t *testing.T) *testWindow {
	t.Helper()
	srv, err := gui.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	url, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); wg.Wait() })

	// http://127.0.0.1:PORT/?t=TOKEN
	cut := strings.Index(url, "/?t=")
	w := &testWindow{srv: srv, base: url[:cut], token: url[cut+4:],
		pending: make(chan gui.Pending, 4), events: make(chan gui.Event, 64)}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, w.base+"/events?t="+w.token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		close(ready)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var e gui.Event
			if json.Unmarshal([]byte(line[6:]), &e) != nil {
				continue
			}
			select {
			case w.events <- e:
			default:
			}
			if e.Kind != "confirm" {
				continue
			}
			var p gui.Pending
			if json.Unmarshal([]byte(e.Text), &p) == nil {
				w.pending <- p
			}
		}
	}()
	<-ready

	// The subscription is registered inside the handler, so wait for the server
	// to see it rather than for the response header.
	for i := 0; i < 200; i++ {
		if srv.HasWindow() {
			return w
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the window never registered on the event stream")
	return nil
}

// answer replies to the next question the window is shown.
func (w *testWindow) answer(t *testing.T, ok bool) gui.Pending {
	t.Helper()
	var p gui.Pending
	select {
	case p = <-w.pending:
	case <-time.After(5 * time.Second):
		t.Fatal("no question reached the window")
	}
	body := strings.NewReader(`{"id":"` + p.ID + `","ok":` + map[bool]string{true: "true", false: "false"}[ok] + `}`)
	req, _ := http.NewRequest(http.MethodPost, w.base+"/answer?t="+w.token, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("answering returned %s", resp.Status)
	}
	return p
}

func rmAction() guard.Action {
	return guard.Action{Kind: guard.KindDelete, Command: "rm", Args: []string{"-rf", "notes"},
		Reason: "clearing the old draft"}
}

func highRisk() guard.Assessment {
	return guard.Assessment{Risk: guard.RiskHigh, Confirm: true,
		Preview: "delete 4 files (12 KB)"}
}

// The bug this whole router exists for: with a window open and no terminal, every
// destructive action came back "declined by user" for a question no user was ever
// shown, because the guard decided attendance by whether stdin is a TTY.
func TestAQuestionGoesToTheWindowWhenOneIsOpen(t *testing.T) {
	w := openTestWindow(t)

	asked := make(chan string, 4)
	terminal := func(context.Context, guard.Action, guard.Assessment) bool {
		asked <- "terminal"
		return false
	}
	c := newConfirmRoutes(&guard.Guard{}, terminal)
	c.setWindow(w.srv)
	c.setVoice(func(context.Context, guard.Action, guard.Assessment) bool {
		asked <- "voice"
		return false
	})

	got := make(chan bool, 1)
	go func() { got <- c.confirm(context.Background(), rmAction(), highRisk()) }()

	p := w.answer(t, true)
	if p.Command != "rm -rf notes" {
		t.Errorf("the window was shown %q, not the command that will run", p.Command)
	}
	if p.Reason != "clearing the old draft" || p.Preview != "delete 4 files (12 KB)" {
		t.Errorf("reason=%q preview=%q — the preview is the safety feature", p.Reason, p.Preview)
	}
	if p.Risk != guard.RiskHigh.String() {
		t.Errorf("risk=%q", p.Risk)
	}
	if p.Seconds < 60 {
		t.Errorf("seconds=%d — the countdown is what makes a timeout legible", p.Seconds)
	}

	select {
	case answer := <-got:
		if !answer {
			t.Error("the window said yes and the router returned no")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the router never returned")
	}
	select {
	case where := <-asked:
		t.Errorf("the %s was asked as well; the same question was put twice", where)
	default:
	}
}

// A no from the window is an answer, not a reason to go and ask the same person
// again over the speakers until one of the channels says yes.
func TestARefusalInTheWindowIsNotRetriedElsewhere(t *testing.T) {
	w := openTestWindow(t)

	var elsewhere int
	c := newConfirmRoutes(&guard.Guard{}, func(context.Context, guard.Action, guard.Assessment) bool {
		elsewhere++
		return true
	})
	c.setWindow(w.srv)

	got := make(chan bool, 1)
	go func() { got <- c.confirm(context.Background(), rmAction(), highRisk()) }()
	w.answer(t, false)

	select {
	case answer := <-got:
		if answer {
			t.Error("the window said no and the action was approved anyway")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the router never returned")
	}
	if elsewhere != 0 {
		t.Errorf("the terminal was asked %d times after the window had already answered", elsewhere)
	}
}

// No window subscribed is not a refusal. This is the distinction the tri-state
// return exists for: before it, a server with no window open returned a bare
// false and the daemon's voice channel was never reached.
func TestNoWindowFallsThroughRatherThanRefusing(t *testing.T) {
	srv, err := gui.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if srv.HasWindow() {
		t.Fatal("a server nobody has opened reports a window")
	}

	var reached string
	c := newConfirmRoutes(&guard.Guard{}, nil)
	c.setWindow(srv)
	c.setVoice(func(context.Context, guard.Action, guard.Assessment) bool {
		reached = "voice"
		return true
	})

	if !c.confirm(context.Background(), rmAction(), highRisk()) {
		t.Error("refused instead of asking the channel that was actually available")
	}
	if reached != "voice" {
		t.Error("voice was never reached")
	}
}

// Order matters, and it is window, terminal, voice. Speech is last on purpose:
// a typed answer is the word the person meant, while a spoken one survives a
// recorder, a transcription and a yes/no parse first.
func TestTheTerminalIsPreferredToSpeech(t *testing.T) {
	var reached []string
	c := newConfirmRoutes(&guard.Guard{}, func(context.Context, guard.Action, guard.Assessment) bool {
		reached = append(reached, "terminal")
		return true
	})
	c.setVoice(func(context.Context, guard.Action, guard.Assessment) bool {
		reached = append(reached, "voice")
		return true
	})

	c.confirm(context.Background(), rmAction(), highRisk())
	if len(reached) != 1 || reached[0] != "terminal" {
		t.Errorf("asked %v", reached)
	}
}

// Attendance is about whether anyone can be reached, not about stdin. Getting
// this wrong in either direction is bad: false when a window is open refuses
// work nobody declined, true with nothing listening blocks a turn on a question
// that will never be seen.
func TestAttendanceFollowsTheChannelsThatExist(t *testing.T) {
	c := newConfirmRoutes(&guard.Guard{}, nil)
	if c.attended() {
		t.Error("claimed somebody could be asked with no terminal, no voice and no window")
	}

	c.setVoice(func(context.Context, guard.Action, guard.Assessment) bool { return false })
	if !c.attended() {
		t.Error("a live microphone is somebody to ask")
	}

	c2 := newConfirmRoutes(&guard.Guard{}, nil)
	srv, err := gui.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c2.setWindow(srv)
	if c2.attended() {
		t.Error("a window server with no window open is not somebody to ask")
	}
	w := openTestWindow(t)
	c2.setWindow(w.srv)
	if !c2.attended() {
		t.Error("an open window is somebody to ask")
	}
}

// The guard reads Confirm and Attended without a lock, so both must be set
// before anything can run an action and never assigned again. This pins that
// newConfirmRoutes is the only thing that touches them.
func TestTheGuardIsWiredOnceAndOnlyOnce(t *testing.T) {
	g := &guard.Guard{}
	c := newConfirmRoutes(g, nil)
	if g.Confirm == nil || g.Attended == nil {
		t.Fatal("newConfirmRoutes left the guard unwired")
	}
	// Registering a channel afterwards must go through the router, not the guard.
	c.setVoice(func(context.Context, guard.Action, guard.Assessment) bool { return true })
	if !g.Attended() {
		t.Error("the guard did not see a channel registered after wiring")
	}
	if !g.Confirm(context.Background(), rmAction(), highRisk()) {
		t.Error("the guard did not route to a channel registered after wiring")
	}
}

// A spoken exchange has to reach the window, or pressing the microphone leaves
// the thread blank through the whole of it and the answer never lands in it at
// all — a voice turn never goes through /ask, so nothing else would put it there.
//
// This is the failure mode the hub introduced and nothing caught: taking the
// per-turn hook swap out left the window with no subscription, every kind was
// dropped, and nothing errored.
func TestASpokenExchangeReachesTheWindow(t *testing.T) {
	w := openTestWindow(t)
	events := make(chan struct{ kind, text string }, 32)
	// A second listener on the same server, so this test reads events without
	// re-implementing the SSE parse in openTestWindow.
	hub := newTraceHub()
	hub.Add(windowTrace(w.srv))
	hub.Add(func(kind, _, text string) { events <- struct{ kind, text string }{kind, text} })

	for _, e := range []struct{ kind, name, text string }{
		{"listening", "", ""},
		{"heard", "", "open my portal"},
		{"thought", "", "she needs the portal"},
		{"tool-start", "browser_open", "portal"},
		{"tool-ok", "browser_open", ""},
		{"speaking", "", "opening it now"},
		{"spoken", "", "Done — it's open."},
		{"turn-done", "", ""},
	} {
		hub.emit(e.kind, e.name, e.text)
	}

	// Everything published must be delivered; the mapping is allowed to rename a
	// kind but never to drop one.
	got := map[string]bool{}
	for len(events) > 0 {
		e := <-events
		got[e.kind] = true
	}
	for _, kind := range []string{"listening", "heard", "thought", "tool-start",
		"tool-ok", "speaking", "spoken", "turn-done"} {
		if !got[kind] {
			t.Errorf("%s never left the hub", kind)
		}
	}

	// And the window's side of the rename: the transcript arrives as its own kind
	// so the front end can render it as the user's turn, and the spoken reply
	// arrives as a reply so it lands in the thread.
	deadline := time.After(5 * time.Second)
	want := map[string]string{"heard": "open my portal", "reply": "Done — it's open.", "done": ""}
	seen := map[string]string{}
	for len(seen) < len(want) {
		select {
		case e := <-w.events:
			if _, ok := want[e.Kind]; ok {
				seen[e.Kind] = e.Text
			}
		case <-deadline:
			t.Fatalf("the window only ever saw %v", seen)
		}
	}
	for kind, text := range want {
		if seen[kind] != text {
			t.Errorf("the window saw %s=%q, want %q", kind, seen[kind], text)
		}
	}
}
