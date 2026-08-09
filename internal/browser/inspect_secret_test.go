package browser

import (
	"strings"
	"testing"
)

// A credential field must never be labelled with what is in it, checked against
// the JavaScript that actually ships.
//
// label() used to end with (el.innerText || el.value). An <input> has no
// innerText, so a filled field was labelled with its own contents, and on a
// sign-in page that is the password. The string goes back as a tool result, into
// the archive, and to the model on every later turn. It reached a real account,
// and nothing failed — the output looked like an ordinary element listing.
func TestInspectNeverLabelsAFieldWithItsValue(t *testing.T) {
	js := inspectScript(60)

	guard := strings.Index(js, "const secret = el =>")
	label := strings.Index(js, "const label = el =>")
	if guard < 0 {
		t.Fatal("the secret() guard is gone — a password field can be labelled with its value again")
	}
	if guard > label {
		t.Error("secret() is defined after the label() that calls it")
	}

	// Everything a real sign-in form uses to mark a credential field. A check
	// that only reads type=password misses the custom widgets, which is most of
	// the portals she actually signs into.
	for _, marker := range []string{"'password'", "one-time-code", "cc-number", "passw", "pwd", "cvv"} {
		if !strings.Contains(js, marker) {
			t.Errorf("secret() does not recognise %s, so a form marking a credential that "+
				"way would still have its contents printed", marker)
		}
	}

	// The guarded branch reports a length and returns before anything can reach
	// the value itself.
	body := js[strings.Index(js, "if (secret(el))"):]
	body = body[:strings.Index(body, "if (el.getAttribute")]
	if !strings.Contains(body, "characters entered") {
		t.Errorf("a credential field does not report its length:\n%s", body)
	}
	if strings.Contains(strings.Replace(body, "(el.value||'').length", "", 1), "el.value") {
		t.Errorf("the secret branch still exposes a value:\n%s", body)
	}
}

// And the ordinary case must keep working, or she loses the ability to read a
// page and the cure is worse than the leak.
func TestOrdinaryElementsAreStillLabelledNormally(t *testing.T) {
	js := inspectScript(60)
	for _, want := range []string{"aria-label", "el.placeholder", "el.innerText||el.value"} {
		if !strings.Contains(js, want) {
			t.Errorf("the normal labelling path lost %s", want)
		}
	}
}
