package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
)

// Phase 26's whole premise, pinned.
//
// Every browser fix this week was the same shape: an action whose effect is
// invisible in the DOM — a download, a dialog, a new window, an OS file chooser.
// Each was fixed on its own. The general cure is that an action which changes
// nothing observable must SAY so, rather than returning a cheerful sentence the
// model then reports as success.
//
// The machinery for that already existed and was keyed off Mutates, which seven
// of the interaction tools never set — so it ran for half the family and nobody
// noticed. Both halves are named here: the flag, and the fingerprint it gates.
func TestEveryMutatingBrowserToolCanTellItDidNothing(t *testing.T) {
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterBrowser(r, g, NewTabs())

	// Actions whose effect is deliberately not a page change: saving a PDF writes
	// a file, uploading hands bytes to an input. Their result is checked
	// elsewhere and a page fingerprint would say "nothing happened" wrongly.
	offPage := map[string]bool{"browser_save_pdf": true, "browser_upload": true}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, s := range r.skills {
		if !strings.HasPrefix(name, "browser_") || !s.Mutates || offPage[name] {
			continue
		}
		if s.Observe == nil {
			t.Errorf("%s changes the page but has no fingerprint, so 'I did it and "+
				"nothing moved' is invisible — the exact failure this phase exists for", name)
		}
		if s.Affordances == nil {
			t.Errorf("%s fails without saying what IS available on the page", name)
		}
	}
}

// A verifier that samples the wrong subject is worse than none, because it
// answers confidently. The browser fingerprint used to resolve the most recently
// used tab rather than the one the call named, so with two tabs open the before
// and after samples could describe different pages.
func TestTheFingerprintFollowsTheTabTheCallNames(t *testing.T) {
	var sampled []string
	r := New()
	r.Register(Skill{
		Tool: llm.Tool{Name: "thing", Params: llm.ObjectSchema(map[string]llm.Property{
			"name": {Type: "string", Description: "which"},
		})},
		Mutates: true,
		Observe: func(_ context.Context, args map[string]any) string {
			sampled = append(sampled, argString(args, "name"))
			return "same"
		},
		Handler: func(context.Context, map[string]any) (string, error) { return "done", nil },
	})

	out, err := r.Execute(context.Background(), "thing", map[string]any{"name": "portal"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sampled) != 2 {
		t.Fatalf("the world was sampled %d times, want twice (before and after)", len(sampled))
	}
	for i, got := range sampled {
		if got != "portal" {
			t.Errorf("sample %d looked at %q, not the tab the call named", i, got)
		}
	}
	// Identical readings mean nothing happened, and that must reach the model.
	if !strings.Contains(strings.ToLower(out), "nothing observably changed") {
		t.Errorf("an action that changed nothing did not say so: %q", out)
	}
}
