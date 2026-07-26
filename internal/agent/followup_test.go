package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
)

// cannedProvider returns a fixed reply, so Followup's decision logic can be
// tested without a live model.
type cannedProvider struct{ reply string }

func (cannedProvider) Name() string { return "canned" }
func (c cannedProvider) Chat(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return &llm.Response{Text: c.reply}, nil
}

func followupAgent(t *testing.T, reply string, withHistory bool) *Agent {
	t.Helper()
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if withHistory {
		if _, err := store.AppendTurn(memory.Turn{Role: "user", Text: "can you do my accounting quiz later?"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendTurn(memory.Turn{Role: "assistant", Text: "Sure, I'll get to it."}); err != nil {
			t.Fatal(err)
		}
	}
	return &Agent{Provider: cannedProvider{reply: reply}, Store: store, Persona: DefaultPersona()}
}

func TestFollowupSpeaksWhenWarranted(t *testing.T) {
	a := followupAgent(t, "Want me to start on that accounting quiz now?", true)
	line, err := a.Followup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if line == "" {
		t.Fatal("a genuine follow-up should be returned, not silence")
	}
}

func TestFollowupStaysQuietOnPASS(t *testing.T) {
	// PASS, however she phrases it, means stay silent.
	for _, reply := range []string{"PASS", "pass", "PASS.", "PASS — nothing is pending"} {
		a := followupAgent(t, reply, true)
		line, err := a.Followup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if line != "" {
			t.Errorf("reply %q should stay quiet, got %q", reply, line)
		}
	}
}

// capturingProvider records the request so a test can assert what was sent.
type capturingProvider struct {
	reply    string
	lastUser string
}

func (*capturingProvider) Name() string { return "capturing" }
func (c *capturingProvider) Chat(_ context.Context, req llm.Request) (*llm.Response, error) {
	if len(req.Messages) > 0 {
		c.lastUser = req.Messages[len(req.Messages)-1].Text
	}
	return &llm.Response{Text: c.reply}, nil
}

func TestFollowupWeavesInState(t *testing.T) {
	store, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(memory.Turn{Role: "user", Text: "let's talk later"}); err != nil {
		t.Fatal(err)
	}
	cap := &capturingProvider{reply: "PASS"}
	a := &Agent{
		Provider: cap,
		Store:    store,
		Persona:  DefaultPersona(),
		StateSummary: func() string {
			return "- due in 2h0m: Basic Accounting quiz\n- you scheduled: \"check the download\" (runs in 5m0s)"
		},
		UserActivity: func() string { return `focused on "Quizzes - BUS 1102 - Google Chrome" (google-chrome)` },
	}
	if _, err := a.Followup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.lastUser, "Basic Accounting quiz") ||
		!strings.Contains(cap.lastUser, "check the download") {
		t.Fatalf("pending state was not woven into the review prompt:\n%s", cap.lastUser)
	}
	if !strings.Contains(cap.lastUser, "Quizzes - BUS 1102") {
		t.Fatalf("user activity was not woven into the review prompt:\n%s", cap.lastUser)
	}
}

func TestFollowupSilentWithNoHistory(t *testing.T) {
	a := followupAgent(t, "I'd invent something here", false)
	line, err := a.Followup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if line != "" {
		t.Fatalf("no conversation should mean no follow-up, got %q", line)
	}
}
