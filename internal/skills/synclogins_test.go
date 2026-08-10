package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
)

// A capability that cannot run where she runs is not a capability.
//
// browser_sync_logins is the answer to "Chrome keeps filling the wrong saved
// account's password" — the situation that blocked a real sign-in. As KindWrite
// it scored RiskMedium, which requires a confirmation, and the daemon, voice and
// one-shot -ask have no terminal to confirm on: the answer defaults to no and the
// tool declines itself. So the fix for the blocker was itself blocked, in every
// mode she is actually used in.
//
// This pins the classification rather than the behaviour of the guard, because
// raising it back to KindWrite would silently restore the deadlock — nothing
// would fail, she would simply be unable to sign in again.
func TestSyncingLoginsCanRunWithNobodyToAsk(t *testing.T) {
	var seen guard.Action
	// A decider that refuses everything, standing in for "there is no terminal":
	// the daemon's confirmer says no when it cannot ask.
	g := guard.New(func(_ context.Context, a guard.Action, _ guard.Assessment) bool {
		seen = a
		return false
	}, nil)

	r := New()
	RegisterBrowser(r, g, NewTabs())

	// It will fail on the sync script or a closed Chrome; what matters is WHY.
	_, err := r.Execute(context.Background(), "browser_sync_logins", nil)
	if err != nil && strings.Contains(err.Error(), "declined") {
		t.Fatalf("browser_sync_logins still needs a confirmation, so it cannot run in "+
			"the daemon, over voice, or from -ask — the modes she is actually used "+
			"in. It is the fix for a sign-in blocker and is itself blocked: %v", err)
	}

	// And the audit trail must still say what was copied. Lowering the risk is
	// not the same as hiding the action.
	if seen.Command != "" {
		if !strings.Contains(seen.Reason, "cookies") || !strings.Contains(seen.Reason, "saved logins") {
			t.Errorf("the audit reason no longer names what is copied: %q", seen.Reason)
		}
	}
}
