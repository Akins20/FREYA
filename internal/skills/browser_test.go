package skills

import (
	"context"
	"testing"
	"time"

	"github.com/Akins20/FREYA/internal/browser"
	"os"
	"strings"
)

// TestSecretFieldsAreRefused is the guarantee that matters most in browser
// automation: credentials must not flow through a tool. The description says
// so, but a model that has talked itself into an exception should still hit a
// hard refusal in the code.
func TestSecretFieldsAreRefused(t *testing.T) {
	mustRefuse := []string{
		"#password", "input[type=password]", `input[type="password"]`,
		"#user_passwd", ".login-pwd", "#otp-code", "input[name=cvv]",
		"#card-number", "#ssn", "#api_token", "#mfa_code",
	}
	for _, sel := range mustRefuse {
		if !looksLikeSecretField(sel) {
			t.Errorf("would have typed into a credential field: %q", sel)
		}
	}

	mustAllow := []string{
		"#search", "input[name=query]", ".comment-box", "#email",
		"textarea#message", "#firstname",
	}
	for _, sel := range mustAllow {
		if looksLikeSecretField(sel) {
			t.Errorf("blocked an ordinary field: %q", sel)
		}
	}
}

// Every click tool reads the event log, not just the gestures.
//
// The browser package doc records the failure the log was built for: "a click
// that started a download looked exactly like a click that did nothing, which is
// how four clicks and four dialogs happen". browser.Describe was then called in
// browser_gestures.go and nowhere else, so right-click, double-click and drag
// reported downloads and dialogs, and browser_click and browser_click_text —
// the tools that do the actual clicking, and the ones the story is about — did
// not.
//
// Observe does not cover it: a download leaves the page fingerprint identical
// and a dialog is auto-answered before the second sample, so the before/after
// comparison sees no change and the result reads as nothing having happened.
func TestSideEffectsIsSafeWithoutATab(t *testing.T) {
	if got := sideEffects(nil, time.Now(), tabSet{}); got != "" {
		t.Errorf("a nil tab produced %q", got)
	}
	if got := sideEffects(&openTab{}, time.Now(), tabSet{}); got != "" {
		t.Errorf("a tab with no client produced %q", got)
	}
}

// A tab reading that failed must not turn every open page into a new one.
//
// "Nothing was open" and "I could not ask" both arrive as an empty list. Treated
// as the same thing, a failed baseline makes every tab in the browser look like
// something the click just opened — so the note would announce six new tabs and
// send her to browser_attach for pages she had nothing to do with. That is the
// Indeed bug inverted: the same tool, inventing a side effect instead of missing
// one.
func TestAFailedTabReadingClaimsNothing(t *testing.T) {
	// An unreachable context: Targets cannot answer, so the reading is not ok.
	snap := pageIDs(browser.Context("nonexistent-context-for-this-test"))
	if snap.ok {
		t.Fatal("precondition: a context that does not exist should not read as ok")
	}
	if len(snap.ids) != 0 {
		t.Errorf("a failed reading carried %d ids", len(snap.ids))
	}
	if got := openedTabs(browser.ContextGuest, snap); got != "" {
		t.Errorf("a failed baseline still claimed tabs opened: %q", got)
	}
}

// The fingerprint has to survive a tab reading that fails on both samples, and
// has to notice one that fails on only one.
//
// Observe compares two opaque strings, so "unknown" cannot be expressed. A
// sentinel makes the two-sided failure — the common case, a briefly unreachable
// endpoint — compare equal instead of reading as a change every time.
func TestAnUnreadableTabSetIsStableRatherThanEmpty(t *testing.T) {
	failed, alsoFailed := tabSet{}, tabSet{}
	if failed.component() != alsoFailed.component() {
		t.Error("two failed readings disagree, so every unreachable moment reads as a change")
	}
	if failed.component() == "" {
		t.Error("a failed reading is indistinguishable from an empty browser")
	}
	empty := tabSet{ids: []string{}, ok: true}
	if empty.component() == failed.component() {
		t.Error("an empty browser and an unreadable one produce the same fingerprint")
	}
	one := tabSet{ids: []string{"A"}, ok: true}
	two := tabSet{ids: []string{"A", "B"}, ok: true}
	if one.component() == two.component() {
		t.Error("opening a tab did not change the fingerprint")
	}
}

// Every browser tool that takes a target the model composed hands back the
// page's real options when it misses.
//
// The rule that produced Protect applies here too: a guard installed at one call
// site is a guard the next tool does not inherit. Affordances went onto the click
// and interact family and stopped there, leaving the three tools that fail on a
// selector or a phrase with nothing to offer. browser_upload's own error text
// already told her to go and inspect the page for input[type=file], which is the
// listing this hands back for free.
func TestEveryBrowserToolWithATargetOffersThePageBack(t *testing.T) {
	r := New()
	RegisterBrowser(r, approveAll(), NewTabs())
	for _, name := range []string{
		"browser_click", "browser_click_text", "browser_fill", "browser_element",
		"browser_wait", "browser_upload",
	} {
		if !r.Has(name) {
			t.Errorf("%s is not registered", name)
			continue
		}
		// AffordancesFor returns nil when the skill has no hook at all. With no
		// browser running the hook itself also returns nil, so this asserts the
		// wiring through the registry rather than the content of the listing.
		if r.AffordancesFor(context.Background(), name, map[string]any{}) == nil &&
			!hasAffordances(r, name) {
			t.Errorf("%s can miss on a target and hands back nothing to act on", name)
		}
	}
}

// hasAffordances reports whether a skill declares the hook, regardless of what
// it returns right now.
func hasAffordances(r *Registry, name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	return ok && s.Affordances != nil
}

// Every tool that clicks reports a tab it opened, not just some of them.
//
// The event log was added because browser.Describe was called in the gestures
// and nowhere else, so the tools doing the actual clicking reported no downloads
// and no dialogs. The tab check arrived the same way round: browser_click and
// browser_click_text learned about new tabs while the gestures did not — and
// ctrl-click is literally "open in a new tab", so it was missing from the one
// place it is guaranteed to matter.
//
// Asserted against the source, because the failure is a call site nobody
// updated, and every unit test of the behaviour would still pass.
func TestEveryClickingToolChecksForATabItOpened(t *testing.T) {
	for _, file := range []string{"browser.go", "browser_gestures.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		body := string(src)
		clicks := strings.Count(body, "sideEffects(tab, started, before)")
		if clicks == 0 {
			t.Errorf("%s reports side effects without a tab baseline — a click that "+
				"opens a tab reads as a click that did nothing", file)
		}
		// The old shape, which reports downloads and dialogs but not tabs. Matched
		// on the call-site variable rather than on browser.Describe itself, because
		// sideEffects is the one place that legitimately calls it and searching for
		// the function name flags its own definition.
		if n := strings.Count(body, "browser.Describe(tab.client.Since(started))"); n > 0 {
			t.Errorf("%s reports side effects directly in %d place(s); route it through "+
				"sideEffects so the tab check is not a per-tool decision", file, n)
		}
	}
}
