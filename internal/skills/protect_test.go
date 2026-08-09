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
		// Raises a window and waits for a person; it clicks nothing itself, so it
		// cannot be a way through anything. If a human then chooses to click
		// through a certificate warning on their own screen, that is a decision
		// they made with the page in front of them — which is the outcome this
		// guard exists to force, not one it exists to prevent.
		"browser_hand_over": true,
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	checked := 0
	for name, s := range r.skills {
		if !strings.HasPrefix(name, "browser_") || !s.Mutates || ways[name] {
			continue
		}
		checked++
		if s.Precheck == nil {
			t.Errorf("%s changes the page and has no warning guard — it is another way "+
				"through a certificate warning", name)
		}
	}
	// The floor. Every assertion above is gated on s.Mutates, which is exactly
	// the flag that was missing from seven interaction tools and made an
	// earlier version of this test pass while proving nothing. If the flag
	// goes again the loop body simply never runs, and without this the suite
	// stays green while the guard it checks has been switched off wholesale.
	if checked < 12 {
		t.Errorf("only %d browser tools reached the assertion, expected at least 12 — "+
			"either Mutates has been dropped again or this test has stopped testing "+
			"anything", checked)
	}
}

// The test above passed while the guard covered almost nothing, and this is the
// one that catches that.
//
// Protect attaches by `Mutates`, and the interaction family — click, fill, type,
// press, submit — was never marked as mutating. So the guard skipped exactly the
// tools it was written for, and the test above skipped them too, because it
// iterated `s.Mutates` and the unguarded tools are excluded by the very
// condition under test. It read as coverage and asserted nothing.
//
// So this names the tools LITERALLY. A list written out by hand cannot be
// filtered down to nothing by the bug it is looking for.
func TestTheClickFamilyIsGuardedByName(t *testing.T) {
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterBrowser(r, g, NewTabs())

	// Everything that puts input into a page. Typing a password onto a
	// certificate warning is the disaster this is guarding against, so the fill
	// and type tools matter at least as much as the click ones.
	for _, name := range []string{
		"browser_click", "browser_click_text", "browser_double_click",
		"browser_right_click", "browser_press", "browser_submit",
		"browser_fill", "browser_type", "browser_select", "browser_check",
		"browser_drag", "browser_upload",
	} {
		r.mu.RLock()
		s, ok := r.skills[name]
		r.mu.RUnlock()
		if !ok {
			continue // not registered in this build; nothing to guard
		}
		if !s.Mutates {
			t.Errorf("%s is not marked Mutates, so it gets no warning guard, no "+
				"verify-after-act and no before/after sampling — every framework "+
				"protection is keyed off that flag", name)
		}
		if s.Precheck == nil {
			t.Errorf("%s can act on a browser certificate warning", name)
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
