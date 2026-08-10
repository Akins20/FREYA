package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// twoClickTools reproduces the pair she ping-ponged between: one takes the label
// under "text", the other takes a CSS selector under "selector".
func twoClickTools(t *testing.T) *Registry {
	t.Helper()
	r := New()
	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_click_text",
			Description: "click by visible text",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name": {Type: "string", Description: "Tab name."},
				"text": {Type: "string", Description: "The visible text to click."},
			}, "text"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			return "Clicked " + argString(args, "text") + ".", nil
		},
	})
	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "browser_click_real",
			Description: "click a CSS selector",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"name":     {Type: "string", Description: "Tab name."},
				"selector": {Type: "string", Description: "An exact CSS selector."},
			}, "selector"),
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			return "Clicked " + argString(args, "selector") + ".", nil
		},
	})
	return r
}

// The exact measured failure: the label was right, the key was wrong, and the
// error said only "text is required" — so she believed she had complied, varied
// something else, and failed identically fourteen times.
func TestAMisfiledValueIsUsedRatherThanRefused(t *testing.T) {
	r := twoClickTools(t)

	out, err := r.Execute(context.Background(), "browser_click_text",
		map[string]any{"selector": "Submit Quiz"})
	if err != nil {
		t.Fatalf("an unambiguous misfiling cost the turn: %v", err)
	}
	if !strings.Contains(out, "Clicked Submit Quiz") {
		t.Errorf("the value was not used: %q", out)
	}
	// And she is told the right key, or she files it wrong again next time.
	if !strings.Contains(out, `"selector"`) || !strings.Contains(out, `"text"`) {
		t.Errorf("the correction does not name both keys: %q", out)
	}
}

// When it cannot be salvaged, the error has to show her the mismatch. Stating the
// rule alone is what produced the loop.
func TestTheErrorNamesWhatWasActuallySent(t *testing.T) {
	r := twoClickTools(t)

	_, err := r.Execute(context.Background(), "browser_click_text",
		map[string]any{"selector": "#d2l_submit", "target": "Submit Quiz"})
	if err == nil {
		t.Fatal("an ambiguous call was run anyway")
	}
	msg := err.Error()

	// What was sent, values and all.
	if !strings.Contains(msg, `selector="#d2l_submit"`) || !strings.Contains(msg, `target="Submit Quiz"`) {
		t.Errorf("the error does not show what was sent:\n%s", msg)
	}
	// What is missing.
	if !strings.Contains(msg, `missing "text"`) {
		t.Errorf("the error does not name the empty slot:\n%s", msg)
	}
	// What this tool takes.
	if !strings.Contains(msg, "text (required)") {
		t.Errorf("the error does not say what the tool accepts:\n%s", msg)
	}
	// And where the stray value belongs — the clause that ends the ping-pong.
	if !strings.Contains(msg, "browser_click_real takes it") {
		t.Errorf("the error does not point the misfiled key at its home:\n%s", msg)
	}
}

// An empty string is how it actually failed: the key was present, the value was
// blank, and the tool reported it as required.
func TestABlankRequiredValueCountsAsMissing(t *testing.T) {
	r := twoClickTools(t)
	_, err := r.Execute(context.Background(), "browser_click_text",
		map[string]any{"name": "quiz", "text": "   "})
	if err == nil {
		t.Fatal("a blank required value was accepted")
	}
	if !strings.Contains(err.Error(), `text=""`) && !strings.Contains(err.Error(), `text="   "`) {
		t.Errorf("the error does not show the blank value:\n%s", err)
	}
}

// A call with nothing in it at all should say so, rather than listing an empty set.
func TestNoArgumentsAtAllIsSaidPlainly(t *testing.T) {
	r := twoClickTools(t)
	_, err := r.Execute(context.Background(), "browser_click_text", nil)
	if err == nil {
		t.Fatal("an empty call was run")
	}
	if !strings.Contains(err.Error(), "no arguments at all") {
		t.Errorf("an empty call was not described honestly:\n%s", err)
	}
}

// The tab name has a documented default — blank means the tab she used last — so
// declaring it required told her to invent one. "no tab named X" was 31 of the
// baseline's failures.
func TestOmittingTheTabNameIsFine(t *testing.T) {
	r := twoClickTools(t)
	out, err := r.Execute(context.Background(), "browser_click_text",
		map[string]any{"text": "Submit Quiz"})
	if err != nil {
		t.Fatalf("omitting the optional tab name was refused: %v", err)
	}
	if !strings.Contains(out, "Clicked Submit Quiz") {
		t.Errorf("out = %q", out)
	}
}

// A correct call must be untouched — no note, no rewriting, no cost.
func TestACorrectCallIsLeftAlone(t *testing.T) {
	r := twoClickTools(t)
	out, err := r.Execute(context.Background(), "browser_click_text",
		map[string]any{"name": "quiz", "text": "Submit Quiz"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Clicked Submit Quiz." {
		t.Errorf("a correct call was altered: %q", out)
	}
}

// Salvage must stay narrow. Two candidates is a guess, and a guess that clicks
// the wrong thing is worse than an error that explains itself.
func TestSalvageDoesNotGuessBetweenCandidates(t *testing.T) {
	r := New()
	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "write_file",
			Description: "write",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":    {Type: "string"},
				"content": {Type: "string"},
			}, "path", "content"),
		},
		Handler: func(context.Context, map[string]any) (string, error) { return "written", nil },
	})
	_, err := r.Execute(context.Background(), "write_file",
		map[string]any{"file": "/tmp/a.txt", "body": "hello"})
	if err == nil {
		t.Fatal("two missing slots and two stray values were guessed at")
	}
	if !strings.Contains(err.Error(), `missing "content" and "path"`) {
		t.Errorf("the error does not name both empty slots:\n%s", err)
	}
}

// The real declarations must actually allow the salvage. A schema that still
// demanded the tab name would leave two slots empty and defeat it.
func TestTheRealClickToolsAllowSalvage(t *testing.T) {
	tabs := NewTabs()
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterBrowser(r, g, tabs)

	for _, tool := range r.Tools() {
		if tool.Name != "browser_click_text" {
			continue
		}
		if len(tool.Params.Required) != 1 || tool.Params.Required[0] != "text" {
			t.Fatalf("browser_click_text requires %v; salvage needs exactly one empty slot",
				tool.Params.Required)
		}
		return
	}
	t.Fatal("browser_click_text is not registered")
}
