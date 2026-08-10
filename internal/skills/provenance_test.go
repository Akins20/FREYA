package skills

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/llm"
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
	fab := "https://learn.uopeople.edu/d2l/lms/quizzing/user/quiz_summary.d2l?qi=9603&ou=8359"

	// The first attempts are allowed — being wrong once is cheap, and refusing
	// outright once cost her a whole session — but the truth rides along.
	note, err := CheckURL(ctx, fab)
	if err != nil {
		t.Fatalf("the first reconstruction should be allowed, not refused: %v", err)
	}
	if !strings.Contains(note, "not on any page you have read") {
		t.Fatalf("no warning was attached to an invented address: %q", note)
	}

	// Sustained walking is the disease, and it is stopped.
	_, _ = CheckURL(ctx, fab+"&x=1")
	_, err = CheckURL(ctx, "https://learn.uopeople.edu/d2l/lms/quizzing/user/quiz_summary.d2l?qi=9604&ou=8359")
	if err == nil {
		t.Fatal("a sustained pattern-walk was allowed — this is the failure that cost a session")
	}
	if !strings.Contains(err.Error(), "pattern-walk") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
}

// The rule that broke her: a portal's own front door has no parameters and must
// never be refused.
func TestOrdinaryPortalPagesAreNeverRefused(t *testing.T) {
	ctx, _ := scopedCtx()
	for _, u := range []string{
		"https://learn.uopeople.edu/d2l/home",
		"https://learn.uopeople.edu/d2l/login",
		"https://learn.uopeople.edu/d2l/lms/quizzing/user/quizzes_list.d2l",
		"https://portal.uopeople.edu/home/overview",
		"https://example.com/search?q=accounting",
		"https://example.com/page?lang=en",
	} {
		note, err := CheckURL(ctx, u)
		if err != nil {
			t.Errorf("an ordinary address was refused: %s → %v", u, err)
		}
		if note != "" {
			t.Errorf("an ordinary address was warned about: %s → %q", u, note)
		}
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
		if _, err := CheckURL(ctx, u); err != nil {
			t.Errorf("a front-door URL was refused: %s → %v", u, err)
		}
	}
}

// A URL she actually read is hers to use, however deep.
func TestObservedDeepLinkIsAllowed(t *testing.T) {
	ctx, scope := scopedCtx()
	deep := "https://learn.uopeople.edu/d2l/lms/quizzing/user/quiz_summary.d2l?qi=9603&ou=8359"

	if note, _ := CheckURL(ctx, deep); note == "" {
		t.Fatal("precondition: an unseen id-carrying URL should at least be flagged")
	}
	// browser_links printed it — now it is observed.
	scope.Ledger().ObserveText("Self-Quiz Unit 5 -> " + deep)
	note, err := CheckURL(ctx, deep)
	if err != nil || note != "" {
		t.Fatalf("a URL she was shown was still questioned: note=%q err=%v", note, err)
	}
}

// The user is the most authoritative source there is.
func TestURLTheUserTypedIsAllowed(t *testing.T) {
	ctx, scope := scopedCtx()
	scope.Ledger().ObserveText(
		"go to https://learn.uopeople.edu/d2l/lms/quizzing/user/quizzes_list.d2l?ou=8453 please")
	note, err := CheckURL(ctx, "https://learn.uopeople.edu/d2l/lms/quizzing/user/quizzes_list.d2l?ou=8453")
	if err != nil || note != "" {
		t.Fatalf("a URL the user typed was questioned: note=%q err=%v", note, err)
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
	// And it is now usable without question.
	if note, err := CheckURL(ctx, "https://portal.test/d2l/quiz/attempt?qi=7"); err != nil || note != "" {
		t.Fatalf("a harvested URL was questioned: note=%q err=%v", note, err)
	}
	// A neighbouring id she invented from it is flagged as a reconstruction.
	if note, _ := CheckURL(ctx, "https://portal.test/d2l/quiz/attempt?qi=8"); note == "" {
		t.Fatal("incrementing an observed id passed unremarked — that is the pattern-walk itself")
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
	if _, err := CheckURL(context.Background(), "https://x.test/a/b?c=1"); err != nil {
		// The process-default scope has a real ledger, so this may refuse; what
		// must not happen is a panic.
		t.Logf("default-scope refusal (acceptable): %v", err)
	}
}

// The bug this guard keeps reinventing: a limit meant for one runaway thread of
// work, applied for the life of the process.
//
// Measured live — 62 tool calls, four of them refused, every refusal a URL check
// long after the budget had been spent in an unrelated conversation minutes
// earlier. She was permanently banned from reconstructing anything on the
// strength of two old guesses.
func TestTheGuessBudgetIsPerExchangeNotPerLifetime(t *testing.T) {
	ctx, scope := scopedCtx()
	led := scope.Ledger()

	spendTheBudget := func() {
		for i := 0; i < guessBudget; i++ {
			if _, err := CheckURL(ctx, fmt.Sprintf("https://portal.test/quiz?qi=%d", 900+i)); err != nil {
				t.Fatalf("attempt %d within budget was refused: %v", i+1, err)
			}
		}
		if _, err := CheckURL(ctx, "https://portal.test/quiz?qi=999"); err == nil {
			t.Fatal("a sustained pattern-walk was allowed")
		}
	}

	spendTheBudget()

	// A new request is not the old one going wrong.
	led.BeginExchange()
	if _, err := CheckURL(ctx, "https://portal.test/quiz?qi=1234"); err != nil {
		t.Fatalf("a fresh request inherited an old exchange's ban: %v", err)
	}

	// And the same runaway is still stopped inside the new exchange. (The probe
	// above spent one of that exchange's guesses, so this starts another.)
	led.BeginExchange()
	spendTheBudget()
}

// Resetting the budget must not make her forget what she read. The two halves of
// the ledger have opposite lifetimes.
func TestBeginExchangeKeepsWhatSheWasShown(t *testing.T) {
	ctx, scope := scopedCtx()
	seen := "https://portal.test/d2l/quiz/attempt?qi=7"
	scope.Ledger().ObserveText("Self-Quiz Unit 5 -> " + seen)

	scope.Ledger().BeginExchange()

	if note, err := CheckURL(ctx, seen); err != nil || note != "" {
		t.Fatalf("a URL she had read was questioned after a reset: note=%q err=%v", note, err)
	}
}
