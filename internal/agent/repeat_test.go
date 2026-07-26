package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/skills"
)

// The loop breaker is the general form of a lesson learned the expensive way:
// her worst recorded run made nineteen consecutive identical failing calls, and
// showing her the page's real options after each miss did not stop the next one.
// The only thing that reliably breaks it is declining to make the call.

// alwaysFailing is a tool that never works, so the only variable is how many
// times the loop lets her try it.
func failingRegistry(t *testing.T, calls *int) *skills.Registry {
	t.Helper()
	r := skills.New()
	r.Register(skills.Skill{
		Tool: llm.Tool{
			Name:        "stuck",
			Description: "always fails",
			Params:      llm.ObjectSchema(map[string]llm.Property{"sel": {Type: "string"}}),
		},
		Handler: func(context.Context, map[string]any) (string, error) {
			*calls++
			return "", errStuck
		},
		Affordances: func(context.Context) []string { return []string{"Start Quiz", "Back to list"} },
	})
	return r
}

var errStuck = &stuckErr{}

type stuckErr struct{}

func (*stuckErr) Error() string { return "no element matches" }

func TestRepeatedIdenticalFailingCallIsRefused(t *testing.T) {
	var executed int
	reg := failingRegistry(t, &executed)

	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builder := memory.NewContextBuilder(store, nil, "persona")

	// Five rounds, each asking for the very same call with the very same
	// arguments, then a plain answer.
	same := llm.ToolCall{ID: "c", Name: "stuck", Args: map[string]any{"sel": "#z_b"}}
	replies := []llm.Response{
		{ToolCalls: []llm.ToolCall{same}},
		{ToolCalls: []llm.ToolCall{same}},
		{ToolCalls: []llm.ToolCall{same}},
		{ToolCalls: []llm.ToolCall{same}},
		{ToolCalls: []llm.ToolCall{same}},
		{Text: "I give up on that one."},
	}
	a := New(&scriptedProvider{responses: replies}, reg, store, builder, DefaultPersona())

	res, err := a.Ask(context.Background(), "click the thing")
	if err != nil {
		t.Fatal(err)
	}

	// Two attempts run; every later identical call is refused without running.
	if executed != repeatLimit {
		t.Fatalf("the failing tool ran %d times, want %d — the loop breaker did not engage",
			executed, repeatLimit)
	}
	if res.Rounds < 5 {
		t.Fatalf("expected the exchange to continue past the refusals, got %d rounds", res.Rounds)
	}
}

func TestRefusalTellsHerWhatIsAvailable(t *testing.T) {
	var executed int
	reg := failingRegistry(t, &executed)
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builder := memory.NewContextBuilder(store, nil, "persona")

	same := llm.ToolCall{ID: "c", Name: "stuck", Args: map[string]any{"sel": "#z_b"}}
	prov := &scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{same}},
		{ToolCalls: []llm.ToolCall{same}},
		{ToolCalls: []llm.ToolCall{same}}, // this one is refused
		{Text: "fine"},
	}}
	a := New(prov, reg, store, builder, DefaultPersona())
	if _, err := a.Ask(context.Background(), "click"); err != nil {
		t.Fatal(err)
	}

	// The refusal reaches the model as a tool result, and carries the real options.
	var sawRefusal bool
	for _, m := range prov.lastReq.Messages {
		if m.Role == llm.RoleTool && strings.Contains(m.Text, "REFUSED") {
			sawRefusal = true
			if !strings.Contains(m.Text, "Start Quiz") {
				t.Errorf("the refusal did not carry the available options: %q", m.Text)
			}
			if !strings.Contains(m.Text, "something different") {
				t.Errorf("the refusal did not point anywhere: %q", m.Text)
			}
		}
	}
	if !sawRefusal {
		t.Fatal("the model was never told the call had been refused")
	}
}

// Different arguments are exploration, not a loop, and must not be blocked.
func TestDifferentArgumentsAreNotRefused(t *testing.T) {
	var executed int
	reg := failingRegistry(t, &executed)
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builder := memory.NewContextBuilder(store, nil, "persona")

	call := func(sel string) llm.ToolCall {
		return llm.ToolCall{ID: "c", Name: "stuck", Args: map[string]any{"sel": sel}}
	}
	a := New(&scriptedProvider{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{call("#a")}},
		{ToolCalls: []llm.ToolCall{call("#b")}},
		{ToolCalls: []llm.ToolCall{call("#c")}},
		{ToolCalls: []llm.ToolCall{call("#d")}},
		{Text: "done"},
	}}, reg, store, builder, DefaultPersona())

	if _, err := a.Ask(context.Background(), "try things"); err != nil {
		t.Fatal(err)
	}
	if executed != 4 {
		t.Fatalf("four distinct attempts ran %d times; exploration must not be blocked", executed)
	}
}

// A success clears the count, because the world moved and the next identical
// call is a fresh question.
func TestSuccessClearsTheFailureCount(t *testing.T) {
	log := newAttemptLog()
	log.record("k", errStuck)
	log.record("k", errStuck)
	if log.failures("k") != 2 {
		t.Fatalf("failures = %d, want 2", log.failures("k"))
	}
	log.record("k", nil)
	if log.failures("k") != 0 {
		t.Fatalf("a success left %d failures on the record", log.failures("k"))
	}
}
