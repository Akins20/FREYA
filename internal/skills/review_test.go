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

// blindEyes is a provider that can see, so review registers, but is never
// reached in these tests because nothing renders without a browser.
type blindEyes struct{ called int }

func (b *blindEyes) Name() string { return "test/eyes" }

func (b *blindEyes) Chat(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return nil, fmt.Errorf("the reasoning half is not what these tests exercise")
}

func (b *blindEyes) AnalyzeImage(ctx context.Context, prompt string, images [][]byte, mimes []string) (string, error) {
	b.called++
	return "three things are wrong with this page", nil
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
