package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/browser"
	"github.com/Akins20/FREYA/internal/guard"
)

// The tool must exist, be reachable from any request, and describe itself as a
// pause rather than a surrender — she already knows how to say "I'm stuck", and
// another way to give up is not what was missing.
func TestTheHandoverIsAlwaysAvailableAndReadsAsAPause(t *testing.T) {
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterBrowser(r, g, NewTabs())

	r.mu.RLock()
	s, ok := r.skills["browser_hand_over"]
	r.mu.RUnlock()
	if !ok {
		t.Fatal("browser_hand_over is not registered")
	}
	if !coreTools["browser_hand_over"] {
		t.Error("it is not in the core kit — a verification wall can appear on any " +
			"page, so it must not wait on a routing decision")
	}

	d := s.Tool.Description
	for _, want := range []string{"CAPTCHA", "two-factor", "not the same as giving up"} {
		if !strings.Contains(d, want) {
			t.Errorf("the description is missing %q", want)
		}
	}
	// It must say what it is NOT for, or it becomes the answer to every failure.
	if !strings.Contains(d, "Do NOT use it for an ordinary failure") {
		t.Error("nothing stops this becoming a general-purpose escape hatch")
	}
	// And it must require the ask, since a raised window with no instruction is
	// just a window.
	if _, err := r.Execute(context.Background(), "browser_hand_over",
		map[string]any{"asking": ""}); err == nil {
		t.Error("it accepted an empty ask")
	}
}

// The window it raises must be HERS. Both windows are titled "… - Google
// Chrome", so matching on title is a coin flip, and raising the user's own
// browser would leave them staring at their tabs wondering what to click.
func TestItIdentifiesHerBrowserByProfileNotTitle(t *testing.T) {
	if !strings.Contains(browser.ProfileMarker, "freya-chrome") {
		t.Errorf("the marker does not name her profile directory: %q", browser.ProfileMarker)
	}
	if !strings.Contains(browser.ProfileMarker, "user-data-dir") {
		t.Errorf("the marker is not the command-line flag that distinguishes her "+
			"instance from the user's: %q", browser.ProfileMarker)
	}
	// It must not be something that would also match the user's Chrome.
	if strings.Contains(browser.ProfileMarker, "google-chrome") {
		t.Error("the marker would match the user's own browser too")
	}
}

// With her browser not running there is nothing to raise, and that must be a
// plain message rather than a panic or a silent success.
func TestRaisingWithNoBrowserSaysSo(t *testing.T) {
	if _, err := freyaChromePID(context.Background()); err == nil {
		t.Skip("her browser is running in this environment")
	} else if !strings.Contains(err.Error(), "not running") {
		t.Errorf("unclear error when her browser is absent: %v", err)
	}
}
