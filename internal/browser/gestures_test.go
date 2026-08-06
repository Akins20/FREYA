package browser

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Ctrl and shift are how "these three files" is expressed in every file manager
// ever written, and the mask is what carries them.
func TestModifiersReachTheBrowser(t *testing.T) {
	if modifierMask([]string{"ctrl"}) != 2 {
		t.Errorf("ctrl = %d, want 2", modifierMask([]string{"ctrl"}))
	}
	if modifierMask([]string{"shift"}) != 8 {
		t.Errorf("shift = %d, want 8", modifierMask([]string{"shift"}))
	}
	if got := modifierMask([]string{"ctrl", "shift"}); got != 10 {
		t.Errorf("ctrl+shift = %d, want 10 (they combine)", got)
	}
	// Case and spacing are how a model actually writes them.
	if modifierMask([]string{" Ctrl ", "SHIFT"}) != 10 {
		t.Error("modifiers were dropped because of case or spacing")
	}
	if modifierMask([]string{"nonsense"}) != 0 {
		t.Error("an unknown modifier changed the mask")
	}
}

// The "which buttons are held" field is separate from "which button caused this",
// and drag handlers read the former. A move without it looks like the mouse is
// not being held.
func TestHeldButtonsAreReportedSeparately(t *testing.T) {
	if buttonsMask(Left) != 1 || buttonsMask(Right) != 2 || buttonsMask(Middle) != 4 {
		t.Errorf("button masks wrong: left=%d right=%d middle=%d",
			buttonsMask(Left), buttonsMask(Right), buttonsMask(Middle))
	}
}

// Events are the only channel that can report what the DOM cannot show. They
// were read and discarded; this is the log that keeps them.
func TestNotableEventsAreKeptAndScopedToWhatJustHappened(t *testing.T) {
	var l eventLog
	before := time.Now()
	time.Sleep(2 * time.Millisecond)

	l.add("download", "a download started: report.pdf")
	l.add("dialog", `the page opened an alert saying "saved"`)

	got := l.since(before)
	if len(got) != 2 {
		t.Fatalf("kept %d events, want 2", len(got))
	}
	// Only what happened after the action, so a click reports its own effects
	// rather than everything the page has ever done.
	after := time.Now()
	if n := len(l.since(after)); n != 0 {
		t.Errorf("%d events leaked in from before the action", n)
	}

	rendered := Describe(got)
	if !strings.Contains(rendered, "report.pdf") || !strings.Contains(rendered, "outside the page") {
		t.Errorf("events do not reach the model legibly:\n%s", rendered)
	}
	if Describe(nil) != "" {
		t.Error("a quiet action still added a section about nothing")
	}
}

// A page can emit thousands of events; the log exists to answer "what did I just
// cause", not to be a history.
func TestTheEventLogIsBounded(t *testing.T) {
	var l eventLog
	for i := 0; i < maxEvents*3; i++ {
		l.add("download", "event")
	}
	if n := len(l.events); n > maxEvents {
		t.Errorf("kept %d events, cap is %d", n, maxEvents)
	}
}

// A javascript dialog BLOCKS the renderer. Left unanswered it hangs every later
// call against the page — including the ones that would have found the problem.
func TestABlockingDialogIsAnsweredAndReported(t *testing.T) {
	c := &Client{events: &eventLog{}}
	before := time.Now()

	c.handleEvent("Page.javascriptDialogOpening", json.RawMessage(
		`{"message":"Your download will begin shortly","type":"alert"}`))

	got := c.events.since(before)
	if len(got) != 1 {
		t.Fatalf("a blocking dialog was not recorded: %+v", got)
	}
	if !strings.Contains(got[0].Detail, "blocking the page") {
		t.Errorf("the record does not say why it mattered: %q", got[0].Detail)
	}
	if !strings.Contains(got[0].Detail, "accepted") {
		t.Errorf("an alert should be accepted so the page continues: %q", got[0].Detail)
	}
}

// Accepting a beforeunload navigates away and discards the page — the one
// destructive answer, so it is the one that is refused.
func TestBeforeUnloadIsDismissedNotAccepted(t *testing.T) {
	c := &Client{events: &eventLog{}}
	before := time.Now()
	c.handleEvent("Page.javascriptDialogOpening", json.RawMessage(
		`{"message":"Changes you made may not be saved","type":"beforeunload"}`))

	got := c.events.since(before)
	if len(got) != 1 || !strings.Contains(got[0].Detail, "dismissed") {
		t.Errorf("a beforeunload was accepted, throwing the page away: %+v", got)
	}
}

// The measured failure this whole file answers: she clicks Download, the page
// does not change, and she concludes nothing happened.
func TestADownloadIsVisibleEvenThoughThePageDidNotChange(t *testing.T) {
	c := &Client{events: &eventLog{}}
	before := time.Now()

	c.handleEvent("Browser.downloadWillBegin", json.RawMessage(
		`{"suggestedFilename":"IMG_20260622_113634.jpg","url":"https://drive.google.com/x"}`))
	c.handleEvent("Browser.downloadProgress", json.RawMessage(
		`{"state":"completed","receivedBytes":2411724}`))

	rendered := Describe(c.events.since(before))
	if !strings.Contains(rendered, "IMG_20260622_113634.jpg") {
		t.Errorf("the download's name never reached her:\n%s", rendered)
	}
	if !strings.Contains(rendered, "finished") || !strings.Contains(rendered, "MB") {
		t.Errorf("completion and size are missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, DownloadDir()) {
		t.Errorf("she is not told where the file landed:\n%s", rendered)
	}
}

// A click that opens a new window is a thing she has to know about, or she keeps
// driving the old one.
func TestANewWindowIsReported(t *testing.T) {
	c := &Client{events: &eventLog{}}
	before := time.Now()
	c.handleEvent("Page.windowOpen", json.RawMessage(`{"url":"https://example.com/doc"}`))
	if r := Describe(c.events.since(before)); !strings.Contains(r, "new window or tab") {
		t.Errorf("a new window went unmentioned:\n%s", r)
	}
}
