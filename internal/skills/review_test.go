package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/llm"
)

// blindEyes is a provider that can see, so review registers. Whether it is
// actually reached is decided by the renderer stub each test installs, not by
// whether the machine running the tests happens to have Chrome on it.
type blindEyes struct{ called int }

func (b *blindEyes) Name() string { return "test/eyes" }

func (b *blindEyes) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("the reasoning half is not what these tests exercise")
}

func (b *blindEyes) AnalyzeImage(ctx context.Context, prompt string, images [][]byte, mimes []string) (string, error) {
	b.called++
	return "three things are wrong with this page", nil
}

// renders makes the renderer answer the way a test needs, and restores the real
// one afterwards.
//
// Without this the suite asserted the right things and could only observe them
// on a machine with no browser: the "a review that saw nothing must fail" test
// passed because rendering failed for want of Chrome, and failed the moment it
// met a machine where review actually works.
func renders(t *testing.T, fn func(ctx context.Context, page string) ([]byte, error)) {
	t.Helper()
	prev := renderPage
	renderPage = fn
	t.Cleanup(func() { renderPage = prev })
}

// nothingRenders is the renderer being unavailable, which is what the gate has
// to survive.
func nothingRenders(t *testing.T) {
	t.Helper()
	renders(t, func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("start the renderer: no browser")
	})
}

// everythingRenders is a working renderer, returning bytes nobody decodes.
func everythingRenders(t *testing.T) {
	t.Helper()
	renders(t, func(context.Context, string) ([]byte, error) {
		return []byte{0x89, 'P', 'N', 'G'}, nil
	})
}

func reviewOn(t *testing.T, pages ...string) (*Registry, string, *blindEyes) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range pages {
		body := "<!doctype html><html><body><h1>" + name + "</h1></body></html>"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	eyes := &blindEyes{}
	r := New()
	RegisterReview(r, eyes)
	return r, dir, eyes
}

// A review that looked at nothing must fail, not succeed quietly.
//
// This is the bug the counting exists for. Every page failing to render used to
// leave the render errors in the returned text, append the line about someone
// seeing the page cold, and hand it all back with a nil error. Three
// consequences, all measured on real runs: the trail recorded a successful
// review; unreviewedSite in internal/agent, whose whole job is to refuse an
// answer until review has run, was satisfied by a call in which nobody looked;
// and the model read the render failure, wrote "review skipped due to renderer
// environment" into its own plan notes, marked the step done and shipped.
//
// A tool that reports success for work it did not do defeats every gate built
// on top of it, and this one is the gate the README leads with.
func TestAReviewThatSawNothingFails(t *testing.T) {
	nothingRenders(t)
	r, dir, eyes := reviewOn(t, "index.html")

	out, err := r.Execute(context.Background(), "review", map[string]any{"path": dir})
	if err == nil {
		t.Fatalf("a review that rendered nothing returned success: %q", out)
	}
	if eyes.called != 0 {
		t.Errorf("the reviewer was asked to look at %d images with no renderer", eyes.called)
	}
	// The message has to say the page is unreviewed, because the model acts on
	// this text and "could not render" reads as a detail rather than a verdict.
	if !strings.Contains(err.Error(), "unreviewed") {
		t.Errorf("the failure does not say the page is unreviewed: %v", err)
	}
	// And it must not carry the closing line, which asserts somebody looked.
	if strings.Contains(err.Error(), "seeing the page cold") {
		t.Errorf("a failed review still claims a reader saw the page: %v", err)
	}
}

// Not a page in sight is a different failure and must stay one, so that
// pointing review at the wrong folder does not read as a renderer problem.
func TestAReviewOfNoPagesFails(t *testing.T) {
	r, dir, _ := reviewOn(t)

	if out, err := r.Execute(context.Background(), "review", map[string]any{"path": dir}); err == nil {
		t.Fatalf("review of an empty folder returned success: %q", out)
	}
}

// review must not exist at all on a provider that cannot see. A push telling
// her to call a tool that is not registered costs a round and reads, from her
// side, as the machine being broken.
func TestReviewIsAbsentWithoutVision(t *testing.T) {
	r := New()
	RegisterReview(r, &llm.Mock{})
	if r.Has("review") {
		t.Error("review registered against a provider with no vision")
	}
}

// A partial review is unmistakable, and keeps what it saw.
//
// The handler used to return the error and discard every verdict it already
// had, so a transient failure on page three cost pages one and two as well and
// the retry paid for them again. Both failure kinds now land in the same list:
// a page that would not render, and a page the reviewer could not look at.
func TestAPartialReviewNamesWhatNobodySaw(t *testing.T) {
	note := unseenNote([]string{"b.html (the reviewer could not look at it: rate limited)"}, 3)
	for _, want := range []string{"1 of 3", "NOT looked at", "b.html", "Nothing above speaks to them"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note is missing %q: %s", want, note)
		}
	}
}

// A complete review says nothing about pages nobody missed, or every result
// grows a caveat and the caveat stops being read.
func TestACompleteReviewCarriesNoCaveat(t *testing.T) {
	if got := unseenNote(nil, 4); got != "" {
		t.Errorf("a review that saw everything still warned: %q", got)
	}
}

// The partial path, through the handler rather than through unseenNote alone.
//
// unseenNote is a pure function and was tested as one, so what nobody exercised
// was the branch that decides which pages reach it: that a page which fails to
// render is counted as unseen, that the verdicts already collected survive
// alongside it, and that the whole thing still succeeds rather than throwing
// away work for a transient failure on one page.
func TestAPartialRenderKeepsItsVerdictsAndNamesTheRest(t *testing.T) {
	renders(t, func(_ context.Context, page string) ([]byte, error) {
		if strings.HasSuffix(page, "b.html") {
			return nil, fmt.Errorf("start the renderer: no browser")
		}
		return []byte{0x89, 'P', 'N', 'G'}, nil
	})
	r, dir, eyes := reviewOn(t, "a.html", "b.html", "c.html")

	out, err := r.Execute(context.Background(), "review", map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("one page failing to render threw away the other two: %v", err)
	}
	if eyes.called != 2 {
		t.Errorf("the reviewer looked at %d pages, want 2", eyes.called)
	}
	for _, want := range []string{"a.html", "c.html", "b.html", "NOT looked at"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result does not mention %q:\n%s", want, out)
		}
	}
	// The verdicts it did get must still be there, or a partial is no better
	// than a failure.
	if !strings.Contains(out, "three things are wrong with this page") {
		t.Errorf("the verdicts collected before the failure were discarded:\n%s", out)
	}
}

// Everything rendering is the ordinary case and must carry no warning at all,
// or the warning is on every result and stops being read.
func TestAReviewThatSawEverythingSaysSo(t *testing.T) {
	everythingRenders(t)
	r, dir, eyes := reviewOn(t, "a.html", "b.html")

	out, err := r.Execute(context.Background(), "review", map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("a review where everything rendered failed: %v", err)
	}
	if eyes.called != 2 {
		t.Errorf("the reviewer looked at %d pages, want 2", eyes.called)
	}
	if strings.Contains(out, "NOT looked at") {
		t.Errorf("a complete review warned about pages nobody missed:\n%s", out)
	}
	if !strings.Contains(out, "seeing the page cold") {
		t.Errorf("a complete review dropped the line that frames the verdict:\n%s", out)
	}
}
