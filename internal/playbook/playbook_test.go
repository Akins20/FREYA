package playbook

import (
	"strings"
	"testing"
)

// flat collapses the body's line wrapping, so a phrase can be asserted without
// the test caring where the prose happens to break.
func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// The playbook is where an affordance has to live, because a tool she never
// thinks to reach for is a tool she does not have.
//
// browser_sync_logins existed for a hundred sessions and was never once called:
// nothing connected it to the moment it solved. The gestures added after the
// Google Drive run would go the same way if the only place they appeared was
// their own descriptions, which she reads only if she is already looking.
func TestTheAppsPlaybookNamesTheToolsAtTheMomentTheyAreNeeded(t *testing.T) {
	s, ok := Get("apps")
	if !ok {
		t.Fatal("there is no playbook for working a web application")
	}

	// Each pairing is a situation she got stuck in, and the tool that gets her out.
	for situation, tool := range map[string]string{
		"right-click":     "browser_right_click",
		"double-click":    "browser_double_click",
		"multi-select":    "browser_select_also",
		"upload":          "browser_upload",
		"inner scrolling": "browser_scroll_within",
		"dragging":        "browser_drag",
		"downloads":       "browser_downloads",
		"saved logins":    "browser_sync_logins",
	} {
		if !strings.Contains(s.Body, tool) {
			t.Errorf("%s has no route out: %s is never mentioned", situation, tool)
		}
	}

	// The two failures that cost whole exchanges have to be named as such.
	body := flat(s.Body)
	if !strings.Contains(body, "looks exactly like a click that failed") {
		t.Error("nothing warns that a download leaves the page unchanged — the trap " +
			"that had her clicking again")
	}
	if !strings.Contains(body, "not part of the page") {
		t.Error("nothing explains that the OS file chooser cannot be driven")
	}
	if !strings.Contains(body, "single click usually only highlights") {
		t.Error("nothing explains why one click looks like it did nothing")
	}
}

// It is only useful if she can find it.
func TestTheAppsPlaybookIsListed(t *testing.T) {
	idx := Index()
	if !strings.Contains(idx, "apps") {
		t.Errorf("the apps playbook is not in the index:\n%s", idx)
	}
	found := false
	for _, n := range Names() {
		if n == "apps" {
			found = true
		}
	}
	if !found {
		t.Error("the apps playbook is not in Names()")
	}
}
