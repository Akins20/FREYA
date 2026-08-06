package browser

import (
	"strings"
	"testing"
	"time"
)

// The first report the failure journal ever received: told which of two accounts
// to use, she let Chrome fill the form, and Chrome filled the FIRST account's
// password instead. She could not choose — the account chooser is browser UI, not
// page DOM — and she had a tool that solves it outright which she had never
// called in a hundred sessions, because nothing connected it to this moment.
func TestMultipleAccountsSayWhatToDoWhenAutofillPicksWrong(t *testing.T) {
	accounts := []SavedLogin{
		{Origin: "https://portal.example.edu/login", Username: "c210929247",
			LastUsed: time.Now().Add(-2 * time.Hour)},
		{Origin: "https://portal.example.edu/login", Username: "C1980058",
			LastUsed: time.Now().Add(-72 * time.Hour)},
	}
	out := FormatAccounts("portal.example.edu", accounts)

	// It still asks, which is the pre-existing rule.
	if !strings.Contains(out, "ASK the user which to use") {
		t.Errorf("the tool stopped asking which account to use:\n%s", out)
	}
	// And now it says what to do when the answer turns out not to be enough.
	if !strings.Contains(out, "browser_sync_logins") {
		t.Errorf("the way out is not mentioned where the problem appears:\n%s", out)
	}
	for _, want := range []string{
		"do not retype it",                 // she must not start typing passwords
		"cannot be switched from the page", // why it is not her fault
		"Chrome must be closed first",      // the one precondition
		"sign in once themselves",          // the fallback that always works
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing guidance %q:\n%s", want, out)
		}
	}
}

// One account is unambiguous, and burying it in advice about a problem it cannot
// have would make the common case worse.
func TestASingleAccountStaysTerse(t *testing.T) {
	out := FormatAccounts("portal.example.edu", []SavedLogin{
		{Origin: "https://portal.example.edu/login", Username: "only-one"},
	})
	if strings.Contains(out, "browser_sync_logins") {
		t.Errorf("the single-account case was cluttered with multi-account advice:\n%s", out)
	}
	if !strings.Contains(out, "Safe to sign in with it") {
		t.Errorf("the single-account case lost its instruction:\n%s", out)
	}
}
