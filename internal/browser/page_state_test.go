package browser

import (
	"os"
	"os/exec"
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

// The blanket refusal must key on certainty, not suspicion.
//
// Interstitial is matched against page prose for phrases like "no internet" and
// "err_connection". A developer's browser is full of pages that legitimately
// contain those — a Stack Overflow answer about ERR_CONNECTION_REFUSED is
// exactly the page she reads while debugging one. Warning her costs a sentence;
// refusing every click on it would break the task.
func TestABlanketRefusalNeedsCertaintyNotSuspicion(t *testing.T) {
	// The real thing: Chrome's own markup or scheme.
	certain := PageState{BrowserPage: true, Interstitial: "Chrome's certificate warning"}
	why := RefuseInteraction(certain, "click Advanced")
	if why == "" {
		t.Fatal("a genuine browser interstitial allowed interaction")
	}
	for _, want := range []string{"SAFETY WARNING", "another way through", "Navigate away"} {
		if !strings.Contains(why, want) {
			t.Errorf("the refusal is missing %q: %s", want, why)
		}
	}

	// Suspicion alone — a page whose text merely mentions a browser error.
	suspected := PageState{Interstitial: "the connection failed; this is a browser error page"}
	if why := RefuseInteraction(suspected, "click Run"); why != "" {
		t.Errorf("a page that only MENTIONS a browser error was locked down: %s", why)
	}

	// And an ordinary page is untouched.
	if why := RefuseInteraction(PageState{}, "click Submit"); why != "" {
		t.Errorf("an ordinary page refused interaction: %s", why)
	}
}

// "Never report placeholders as the result" was prose, and readyState does not
// enforce it.
//
// The web playbook tells her that empty rows and grey skeleton bars mean she
// looked too early, not that the data is missing. The only thing checking that
// was `readyState != "complete"` — which flips to complete as soon as the
// document is in, long before an application's data arrives. So the exact state
// the instruction is about (shell present, rows still placeholders) reported as
// a finished, empty page.
func TestAPageThatIsCompleteButStillFillingInSaysSo(t *testing.T) {
	filling := PageState{Rendering: true}
	d := filling.Describe()
	if d == "" {
		t.Fatal("a page still rendering placeholders described itself as unremarkable")
	}
	for _, want := range []string{"still filling in", "shell rather than the data", "do NOT report this as empty"} {
		if !strings.Contains(d, want) {
			t.Errorf("the description is missing %q: %s", want, d)
		}
	}

	// While still navigating, Loading already covers it — saying both is noise.
	both := PageState{Loading: true}
	if strings.Contains(both.Describe(), "still filling in") {
		t.Error("a page that has not finished loading also claimed to be rendering")
	}

	// And an ordinary settled page says nothing at all.
	if got := (PageState{}).Describe(); got != "" {
		t.Errorf("a settled page described itself: %q", got)
	}
}

// "Never sit in the guest context and then say you can't sign in" was in the
// sign-in playbook for weeks with nothing checking it. The failure is not that
// she cannot sign in — it is that she is at a door she brought no key to,
// concludes it is locked, and reports a limitation that is not real. The user's
// session exists; it is one context away.
func TestASignInPageInTheGuestContextIsCalledOut(t *testing.T) {
	page := PageState{SignIn: true}

	said := GuestSignIn(page, true)
	if said == "" {
		t.Fatal("a sign-in page in guest passed without comment")
	}
	for _, want := range []string{"GUEST context", "auth", "do not try to type their password"} {
		if !strings.Contains(said, want) {
			t.Errorf("the note is missing %q: %s", want, said)
		}
	}

	// In auth she IS signed in, so the note would be noise on every login page.
	if got := GuestSignIn(page, false); got != "" {
		t.Errorf("the auth context was warned about its own session: %s", got)
	}
	// And an ordinary page in guest is not a sign-in.
	if got := GuestSignIn(PageState{}, true); got != "" {
		t.Errorf("an ordinary guest page was called a sign-in: %s", got)
	}
}

// The embedded page scripts are strings to Go, so a typo in them compiles fine
// and breaks every click at runtime instead. This parses them.
//
// Added after a click fix touched the busiest script in the tree: seven blocks
// of JavaScript that nothing in `go build` or `go test` was checking.
func TestTheEmbeddedPageScriptsParse(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed; cannot parse the page scripts")
	}
	blocks := 0
	var all strings.Builder
	for _, src := range []string{sigExpr, deepPrelude} {
		blocks++
		all.WriteString("(function(){ " + src + " });\n")
	}
	if blocks == 0 {
		t.Fatal("no scripts found to check")
	}

	f, err := os.CreateTemp(t.TempDir(), "*.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(all.String()); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out, err := exec.Command("node", "--check", f.Name()).CombinedOutput()
	if err != nil {
		t.Fatalf("an embedded page script does not parse — every call using it would "+
			"fail at runtime with a script error:\n%s", out)
	}
}
