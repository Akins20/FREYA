package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/config"
	"github.com/Akins20/FREYA/internal/schedule"
	"github.com/Akins20/FREYA/internal/sentinel"
	"github.com/Akins20/FREYA/internal/skills"
	"github.com/Akins20/FREYA/internal/term"
)

// The inspector, against sources that actually have something in them.
//
// There was no test for guiSources.state() at all, and that is how three
// separate panels shipped wrong: reminders and observations were never checked
// against a populated store, the Busy field was computed and thrown away by the
// server, and the voice pill was hard-wired to "off" in the daemon. Every one of
// them is a value that looks plausible when it is empty.
func TestTheInspectorReportsWhatTheSourcesActuallyHold(t *testing.T) {
	dir := t.TempDir()

	notes, err := skills.RegisterNotes(skills.New(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// Written through the file the notebook reads, so this exercises the same
	// path a real note takes rather than a struct set by hand.
	due := time.Now().Add(-2 * time.Hour)
	later := time.Now().Add(48 * time.Hour)
	raw, _ := json.Marshal([]skills.Note{
		{ID: "n1", Text: "send the invoice", Created: time.Now(), Due: &due},
		{ID: "n2", Text: "book the MOT", Created: time.Now(), Due: &later},
		{ID: "n3", Text: "already handled", Created: time.Now(), Done: true},
	})
	if err := os.WriteFile(filepath.Join(dir, "notes.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err = skills.RegisterNotes(skills.New(), dir)
	if err != nil {
		t.Fatal(err)
	}

	tasks, err := schedule.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Add("check the build", time.Now().Add(10*time.Minute), ""); err != nil {
		t.Fatal(err)
	}

	sen := sentinel.New(sentinel.ChattyQuiet, nil)
	src := &guiSources{
		cfg:       &config.Config{Model: "gemini-3.5-flash-lite", DataDir: dir},
		notes:     notes,
		tasks:     tasks,
		sentinel:  sen,
		tabs:      skills.NewTabs(),
		terminals: term.NewManager(),
	}

	st := src.state()

	if st.Model != "gemini-3.5-flash-lite" {
		t.Errorf("model = %q", st.Model)
	}

	// Reminders fold notes and self-set tasks together, because from the outside
	// they are the same thing: something she said she would do later.
	var late, seen int
	texts := map[string]bool{}
	for _, r := range st.Reminders {
		texts[r.Text] = true
		if r.Late {
			late++
		}
		seen++
	}
	if !texts["send the invoice"] || !texts["book the MOT"] || !texts["check the build"] {
		t.Errorf("reminders lost something: %+v", st.Reminders)
	}
	if texts["already handled"] {
		t.Error("a note marked done is still being shown as outstanding")
	}
	if late != 1 {
		t.Errorf("%d reminders marked late, want 1 (only the overdue note)", late)
	}
	if seen != 3 {
		t.Errorf("%d reminders, want 3", seen)
	}

	// Watchers are counted from the sentinel rather than assumed.
	if st.Watchers != len(sen.Watchers()) {
		t.Errorf("watchers = %d, sentinel has %d", st.Watchers, len(sen.Watchers()))
	}

	// Voice with no voiceState at all is off, and says so rather than panicking.
	if st.Voice != "off" {
		t.Errorf("voice = %q with no voice stack", st.Voice)
	}
}

// Peek, never Pending. Pending DRAINS the queue, so a window polling every few
// seconds would consume every observation before the daemon could say it aloud
// and the user would never hear one.
func TestPollingTheInspectorDoesNotEatHerObservations(t *testing.T) {
	dir := t.TempDir()
	sen := sentinel.New(sentinel.ChattyQuiet, nil)
	src := &guiSources{
		cfg: &config.Config{DataDir: dir}, sentinel: sen,
		tabs: skills.NewTabs(), terminals: term.NewManager(),
	}

	// Two polls in a row must see the same thing. If state() drained, the second
	// would come back empty — which is exactly how the observations would vanish
	// before she ever spoke them.
	first := src.state()
	second := src.state()
	if len(first.Watching) != len(second.Watching) {
		t.Errorf("polling consumed observations: %d then %d", len(first.Watching), len(second.Watching))
	}
}

// Observations are ordered most-urgent-first, or a filling disk sits underneath
// a dozen ambient notes about repositories nobody has touched in a year.
func TestTheMostUrgentObservationComesFirst(t *testing.T) {
	dir := t.TempDir()
	sen := sentinel.New(sentinel.ChattyQuiet, nil)
	src := &guiSources{cfg: &config.Config{DataDir: dir}, sentinel: sen,
		tabs: skills.NewTabs(), terminals: term.NewManager()}

	st := src.state()
	worst := 3
	for _, w := range st.Watching {
		rank := map[string]int{"critical": 0, "notable": 1, "ambient": 2}[w.Urgency]
		if rank < worst && worst != 3 {
			t.Errorf("observations are out of order: %q (%s) came after a less urgent one",
				w.Summary, w.Urgency)
		}
		worst = rank
	}
}

// Every turn is registered, including the window's.
//
// CLAUDE.md states this as one of three constraints that "each has a test", and
// it was the one that did not. Without it Ctrl-C, the spoken "stop",
// stopEverything and the daemon's yield grace loop are all blind to a turn
// started in the window — and the daemon can hand the archive over from
// underneath one.
func TestAWindowTurnIsVisibleToTheRestOfHer(t *testing.T) {
	if currentTurn() != nil {
		t.Fatal("a turn was already registered before this test started")
	}

	seen := make(chan bool, 1)
	asker := &guiAsker{a: nil, vs: nil}
	// Stand in for the agent: the only thing under test is whether the turn is
	// registered while the work is running.
	asker.run = func(ctx context.Context, _ string) (string, error) {
		seen <- currentTurn() != nil
		return "done", nil
	}

	reply, err := asker.Ask(context.Background(), "do the thing")
	if err != nil {
		t.Fatal(err)
	}
	if reply != "done" {
		t.Errorf("reply = %q", reply)
	}
	if registered := <-seen; !registered {
		t.Error("the window's turn ran without registering, so nothing else in the " +
			"process can see it, stop it, or wait for it")
	}
	if currentTurn() != nil {
		t.Error("the turn is still registered after Ask returned")
	}
}

// And the description it registers under says what she is doing, because that is
// what the daemon prints when it has to hand over mid-exchange.
func TestTheRegisteredTurnSaysWhatSheIsWorkingOn(t *testing.T) {
	got := make(chan string, 1)
	asker := &guiAsker{}
	asker.run = func(ctx context.Context, _ string) (string, error) {
		if turn := currentTurn(); turn != nil {
			got <- turn.what
		} else {
			got <- ""
		}
		return "", nil
	}
	if _, err := asker.Ask(context.Background(), "open my portal and do the quizzes"); err != nil {
		t.Fatal(err)
	}
	what := <-got
	if !strings.Contains(what, "open my portal") {
		t.Errorf("registered as %q, which does not say what she is doing", what)
	}
}
