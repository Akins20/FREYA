package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/llm"
)

// scopedCtx gives a test its own ledger.
func scopedCtx() (context.Context, Scope) {
	s := NewScope(NewWorkspace("/tmp"), "", "")
	return WithScope(context.Background(), s), s
}

// The exact failure from her logs: walking quiz ids by pattern, every one of
// which returned a real page, for forty rounds of apparent progress.
func TestComposedDeepLinkIsRefused(t *testing.T) {
	ctx, scope := scopedCtx()

	// She was shown the course home page.
	scope.Ledger().Observe(IDURL, "https://learn.uopeople.edu/d2l/home/8359")

	// Walking to a quiz id she was never shown is a guess.
	err := CheckURL(ctx, "https://learn.uopeople.edu/d2l/lms/quizzing/user/quiz_summary.d2l?qi=9603&ou=8359")
	if err == nil {
		t.Fatal("a fabricated deep link was allowed — this is the failure that cost a whole session")
	}
	if !strings.Contains(err.Error(), "guess") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	// And it must point at what she actually has.
	if !strings.Contains(err.Error(), "d2l/home/8359") {
		t.Errorf("the refusal did not offer the URLs she had been shown: %v", err)
	}
}

// Walking in the front door is legitimate and must never be blocked.
func TestShallowURLsAreAllowed(t *testing.T) {
	ctx, _ := scopedCtx()
	for _, u := range []string{
		"https://learn.uopeople.edu",
		"https://learn.uopeople.edu/",
		"https://learn.uopeople.edu/d2l",
		"https://google.com",
	} {
		if err := CheckURL(ctx, u); err != nil {
			t.Errorf("a front-door URL was refused: %s → %v", u, err)
		}
	}
}

// A URL she actually read is hers to use, however deep.
func TestObservedDeepLinkIsAllowed(t *testing.T) {
	ctx, scope := scopedCtx()
	deep := "https://learn.uopeople.edu/d2l/lms/quizzing/user/quiz_summary.d2l?qi=9603&ou=8359"

	if err := CheckURL(ctx, deep); err == nil {
		t.Fatal("precondition: an unseen deep link should be refused")
	}
	// browser_links printed it — now it is observed.
	scope.Ledger().ObserveText("Self-Quiz Unit 5 -> " + deep)
	if err := CheckURL(ctx, deep); err != nil {
		t.Fatalf("a URL she was shown was still refused: %v", err)
	}
}

// The user is the most authoritative source there is.
func TestURLTheUserTypedIsAllowed(t *testing.T) {
	ctx, scope := scopedCtx()
	scope.Ledger().ObserveText(
		"go to https://learn.uopeople.edu/d2l/lms/quizzing/user/quizzes_list.d2l?ou=8453 please")
	if err := CheckURL(ctx,
		"https://learn.uopeople.edu/d2l/lms/quizzing/user/quizzes_list.d2l?ou=8453"); err != nil {
		t.Fatalf("a URL the user typed was refused: %v", err)
	}
}

// Harvesting from tool output is what makes provenance affordable: no producer
// tool had to be modified for this to work.
func TestOutputHarvestFeedsTheLedger(t *testing.T) {
	ctx, scope := scopedCtx()
	r := New()
	r.Register(Skill{
		Tool: llm.Tool{Name: "browser_links", Description: "d", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			return "Self-Quiz Unit 5 -> https://portal.test/d2l/quiz/attempt?qi=7\n" +
				"Home -> https://portal.test/home", nil
		},
	})
	if _, err := r.Execute(ctx, "browser_links", nil); err != nil {
		t.Fatal(err)
	}
	if !scope.Ledger().Seen(IDURL, "https://portal.test/d2l/quiz/attempt?qi=7") {
		t.Fatal("a URL the tool printed was not recorded as observed")
	}
	// And it is now usable.
	if err := CheckURL(ctx, "https://portal.test/d2l/quiz/attempt?qi=7"); err != nil {
		t.Fatalf("a harvested URL was refused: %v", err)
	}
	// A neighbouring id she invented from it is still a guess.
	if err := CheckURL(ctx, "https://portal.test/d2l/quiz/attempt?qi=8"); err == nil {
		t.Fatal("incrementing an observed id was allowed — that is the pattern-walk itself")
	}
}

// Namespaces must not vouch for each other.
func TestKindsAreSeparate(t *testing.T) {
	l := NewLedger()
	l.Observe(IDPath, "https://example.com/a/b?c=1")
	if l.Seen(IDURL, "https://example.com/a/b?c=1") {
		t.Fatal("a path observation vouched for a URL")
	}
}

// No ledger means no opinion — the checks must never block on missing
// bookkeeping, or a tool called outside a scope would become unusable.
func TestNilLedgerNeverBlocks(t *testing.T) {
	var l *Ledger
	if !l.Seen(IDURL, "anything") {
		t.Fatal("a nil ledger should have no opinion, not refuse")
	}
	if err := CheckURL(context.Background(), "https://x.test/a/b?c=1"); err != nil {
		// The process-default scope has a real ledger, so this may refuse; what
		// must not happen is a panic.
		t.Logf("default-scope refusal (acceptable): %v", err)
	}
}
