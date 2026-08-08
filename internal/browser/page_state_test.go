package browser

import (
	"strings"
	"testing"
)

// The trap: Chrome's own warning pages are real pages with real text. Read as
// content, "Your connection is not private" becomes "the site now says something
// about privacy", and the next twenty rounds go on Chrome's error page.
func TestABrowserWarningIsNotMistakenForTheSite(t *testing.T) {
	cases := map[string]string{
		"Your connection is not private": "certificate warning",
		"Deceptive site ahead":           "safe-browsing",
		"This site can't be reached":     "did not respond",
		"ERR_NAME_NOT_RESOLVED":          "does not exist",
	}
	for text, want := range cases {
		var s PageState
		hay := strings.ToLower(text)
		for _, p := range interstitialPatterns {
			if strings.Contains(hay, p.needle) {
				s.Interstitial = p.says
				break
			}
		}
		if s.Interstitial == "" {
			t.Errorf("%q was not recognised as a browser page", text)
			continue
		}
		if !strings.Contains(s.Interstitial, want) {
			t.Errorf("%q -> %q, want something about %q", text, s.Interstitial, want)
		}
	}
	// And the warnings say not to push through them.
	for _, p := range interstitialPatterns {
		if strings.Contains(p.needle, "deceptive") && !strings.Contains(p.says, "Do not click through") {
			t.Error("the safe-browsing warning does not say to stop")
		}
	}
}

// A page that is still loading reads as an empty page — the same trap from the
// other side, and the reason she reports "there is nothing there".
func TestStillLoadingIsSaidOutLoud(t *testing.T) {
	s := PageState{Loading: true}
	d := s.Describe()
	if !strings.Contains(d, "still loading") {
		t.Errorf("a half-loaded page is not flagged: %q", d)
	}
	if !strings.Contains(d, "rather than concluding it is empty") {
		t.Errorf("the note does not name the wrong conclusion: %q", d)
	}
}

// An ordinary page must add nothing, or every read grows a paragraph of noise
// and she stops reading the part that matters.
func TestAnOrdinaryPageSaysNothingExtra(t *testing.T) {
	if d := (PageState{URL: "https://example.com", Title: "Home"}).Describe(); d != "" {
		t.Errorf("an unremarkable page added %q", d)
	}
}

// A page that threw is a page whose controls may simply not be wired up, which
// is worth knowing before concluding a button is missing.
func TestPageErrorsAreReported(t *testing.T) {
	s := PageState{Errors: []string{"TypeError: undefined is not a function"}}
	if !strings.Contains(s.Describe(), "TypeError") {
		t.Errorf("a page error was swallowed: %q", s.Describe())
	}
}

// The measured answer that made this a refusal rather than a note: asked what to
// do on "Your connection is not private" with the user waiting to sign in, she
// said she would click Advanced and proceed. She had been told it was a browser
// warning. She read that, understood it, and chose to go through.
func TestClickingThroughASafetyWarningIsRefused(t *testing.T) {
	warned := PageState{Interstitial: "this is CHROME'S OWN certificate warning, not the site"}

	for _, target := range []string{
		"Advanced", "Proceed to example.com (unsafe)", "Continue to site",
		"I understand the risks", "accept the risk",
	} {
		why := RefuseUnsafeProceed(warned, target)
		if why == "" {
			t.Errorf("clicking %q on a safety warning was allowed", target)
			continue
		}
		if !strings.Contains(why, "password") {
			t.Errorf("the refusal does not say what is at stake: %q", why)
		}
		if !strings.Contains(why, "Tell the user") {
			t.Errorf("the refusal does not say what to do instead: %q", why)
		}
		if !strings.Contains(why, "Do not look for another way through") {
			t.Errorf("the refusal invites her to route around it: %q", why)
		}
	}

	// Leaving is always allowed.
	if why := RefuseUnsafeProceed(warned, "Back to safety"); why != "" {
		t.Errorf("leaving the warning was refused: %q", why)
	}

	// And an ordinary page is untouched — this must not become a tax on every
	// click that happens to say "details".
	ordinary := PageState{}
	for _, target := range []string{"Advanced", "Details", "Continue to checkout"} {
		if why := RefuseUnsafeProceed(ordinary, target); why != "" {
			t.Errorf("an ordinary page refused %q: %s", target, why)
		}
	}
}
