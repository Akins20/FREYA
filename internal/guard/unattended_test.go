package guard

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Asked to write hello.html in her working directory, she came back with one
// tool call and "file_write FAILED: declined by user". Nobody had declined
// anything: the session had no terminal, so the confirmation was never shown,
// and the stand-in prompt returned false — which is exactly how a person says
// no. The guard could not tell the two apart, so the model was told the user
// had refused, told the user the user had refused, and stopped.
func TestARefusalNobodyCouldAnswerDoesNotSayTheUserDeclined(t *testing.T) {
	asked := false
	g := New(func(context.Context, Action, Assessment) bool { asked = true; return false }, nil)
	g.Attended = func() bool { return false }

	_, err := g.Run(context.Background(),
		Action{Kind: KindWrite, Paths: []string{"/home/akins/hello.html"}},
		func(context.Context) (string, error) { return "", errors.New("must not run") })

	if asked {
		t.Fatal("the prompt was invoked in a session with nobody to answer it")
	}
	if !errors.Is(err, ErrUnattended) {
		t.Fatalf("got %v, want ErrUnattended", err)
	}
	if errors.Is(err, ErrDenied) || strings.Contains(err.Error(), "declined by user") {
		t.Errorf("reported as a user's refusal: %v", err)
	}
	// And it has to say what it needed and that nothing happened, or she has
	// nothing to tell the user beyond that it failed.
	for _, want := range []string{"medium", "creates or modifies files", "Nothing was done"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal never mentions %q: %v", want, err)
		}
	}
}

// The real no still reads as a real no. This distinction only matters if both
// halves of it are true.
func TestAnAnsweredNoIsStillDeclinedByUser(t *testing.T) {
	g := New(func(context.Context, Action, Assessment) bool { return false }, nil)

	_, err := g.Run(context.Background(),
		Action{Kind: KindWrite, Paths: []string{"/home/akins/hello.html"}},
		func(context.Context) (string, error) { return "", errors.New("must not run") })

	if !errors.Is(err, ErrDenied) {
		t.Fatalf("got %v, want ErrDenied", err)
	}
	if errors.Is(err, ErrUnattended) {
		t.Errorf("a declined action claims nobody was asked: %v", err)
	}
}

// An unattended guard that is never asked anything still runs the work: this is
// about actions that need approval, not about refusing headless sessions.
func TestUnattendedDoesNotBlockWhatNeedsNoApproval(t *testing.T) {
	g := New(func(context.Context, Action, Assessment) bool { return false }, nil)
	g.Attended = func() bool { return false }
	g.Workspace = "/home/akins/freya-workspace"

	out, err := g.Run(context.Background(),
		Action{Kind: KindWrite, Paths: []string{"/home/akins/freya-workspace/hello.html"}},
		func(context.Context) (string, error) { return "wrote it", nil })
	if err != nil || out != "wrote it" {
		t.Fatalf("a low-risk write in her own workspace was stopped: %q %v", out, err)
	}
}
