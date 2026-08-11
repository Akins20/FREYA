package skills

import (
	"context"
	"testing"
	"time"
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
	if got := sideEffects(nil, time.Now()); got != "" {
		t.Errorf("a nil tab produced %q", got)
	}
	if got := sideEffects(&openTab{}, time.Now()); got != "" {
		t.Errorf("a tab with no client produced %q", got)
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
