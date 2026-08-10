package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/memory"
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

// She is only ever heard from when she wants something, unless told otherwise.
//
// Followup chases loose ends: a deadline, something she said she would do, an
// obvious next step. That is useful, and it is also why she never simply says
// hello. Somebody who only speaks to hand you a job is not company, and the
// point of her running all day is that she is around.
func TestCompanyIsOnlyOfferedWhenAskedFor(t *testing.T) {
	quiet := &Agent{Persona: DefaultPersona()}
	if strings.Contains(quiet.followupSystem(), "You may also just talk") {
		t.Error("she was given permission to chat without companion chattiness")
	}

	sociable := &Agent{Persona: DefaultPersona(), Sociable: true}
	block := sociable.followupSystem()
	if !strings.Contains(block, "You may also just talk") {
		t.Fatal("companion chattiness did not unlock a non-task check-in")
	}
	// The bar has to travel with the permission, or this becomes filler on a
	// timer, which is worse than silence and gets noticed within a day.
	for _, want := range []string{"PASS", "stepped away", "concentration"} {
		if !strings.Contains(block, want) {
			t.Errorf("the permission to talk dropped the guard rail %q", want)
		}
	}
}
