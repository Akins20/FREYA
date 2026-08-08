package skills

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
)

// The bug this whole mechanism exists for.
//
// Clicking through a browser certificate warning was refused in exactly one
// tool, browser_click_text, and its refusal text said "Do not look for another
// way through" — while browser_click, browser_double_click, browser_right_click,
// browser_press and browser_submit were all another way through. A rule enforced
// at one call site out of six is not a rule.
func TestEveryMutatingBrowserToolCarriesTheWarningGuard(t *testing.T) {
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterBrowser(r, g, NewTabs())

	// The ways out of a warning page, which must stay usable on one.
	ways := map[string]bool{
		"browser_open": true, "browser_attach": true,
		"browser_tabs": true, "browser_sync_logins": true,
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, s := range r.skills {
		if !strings.HasPrefix(name, "browser_") || !s.Mutates || ways[name] {
			continue
		}
		if s.Precheck == nil {
			t.Errorf("%s changes the page and has no warning guard — it is another way "+
				"through a certificate warning", name)
		}
	}
}

// A tool registered after the guard was installed must still get it, because
// "remember to add the guard" is the failure mode being cured.
func TestProtectCoversAToolAddedLater(t *testing.T) {
	r := New()
	refused := errors.New("nope")
	pre := func(context.Context, map[string]any) error { return refused }

	r.Register(Skill{
		Tool:    llm.Tool{Name: "fam_first", Params: llm.ObjectSchema(nil)},
		Mutates: true,
		Handler: func(context.Context, map[string]any) (string, error) { return "ran", nil },
	})
	if n := r.Protect("fam_", pre); n != 1 {
		t.Fatalf("Protect covered %d tools, want 1", n)
	}

	// The same family, registered afterwards, and protected by the same call.
	r.Register(Skill{
		Tool:    llm.Tool{Name: "fam_second", Params: llm.ObjectSchema(nil)},
		Mutates: true,
		Handler: func(context.Context, map[string]any) (string, error) { return "ran", nil },
	})
	if n := r.Protect("fam_", pre); n != 2 {
		t.Errorf("re-protecting covered %d tools, want 2 — a tool added later was missed", n)
	}

	if _, err := r.Execute(context.Background(), "fam_second", nil); !errors.Is(err, refused) {
		t.Errorf("the later tool ran anyway: %v", err)
	}
}

// A read must not be taxed by a rule about changing things. Looking at a warning
// page is exactly what she should do with one.
func TestProtectLeavesReadsAlone(t *testing.T) {
	r := New()
	r.Register(Skill{
		Tool:    llm.Tool{Name: "fam_read", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) { return "page text", nil },
	})
	r.Protect("fam_", func(context.Context, map[string]any) error {
		return errors.New("refused")
	})

	out, err := r.Execute(context.Background(), "fam_read", nil)
	if err != nil {
		t.Fatalf("a read was refused by a mutation guard: %v", err)
	}
	if out != "page text" {
		t.Errorf("got %q", out)
	}
}

// Two rules over one family must both apply, or installing a second silently
// disables the first.
func TestProtectChainsRatherThanReplaces(t *testing.T) {
	r := New()
	var ran []string
	r.Register(Skill{
		Tool:    llm.Tool{Name: "fam_x", Params: llm.ObjectSchema(nil)},
		Mutates: true,
		Handler: func(context.Context, map[string]any) (string, error) { return "ran", nil },
	})
	r.Protect("fam_", func(context.Context, map[string]any) error {
		ran = append(ran, "first")
		return nil
	})
	r.Protect("fam_", func(context.Context, map[string]any) error {
		ran = append(ran, "second")
		return nil
	})
	if _, err := r.Execute(context.Background(), "fam_x", nil); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 2 || ran[0] != "first" || ran[1] != "second" {
		t.Errorf("prechecks ran as %v, want both in order", ran)
	}
}
