package skills

import (
	"strings"
	"testing"
)

// The live half of the capability report keeps four answers apart.
//
// "Accessibility: yes" is a fact about the machine and stays true all day.
// Whether the window in front of her can be read is a fact about now, and they
// come apart badly: a Chromium application publishes nothing inside itself
// unless it was started with --force-renderer-accessibility, so the capability
// report said yes while desktop_inspect returned a window with nothing in it.
// Each of the four states leads somewhere different, and collapsing any two
// loses the part she needed.
func TestTheDesktopSectionKeepsItsFourAnswersApart(t *testing.T) {
	got := desktopSection(
		[]string{"Text Editor"},
		[]string{"Slack", "Visual Studio Code"},
		[]string{"Some Canvas App"},
		[]string{"xterm"},
		3,
	)
	for _, want := range []string{
		"readable: Text Editor",
		"withholding: Slack, Visual Studio Code",
		"--force-renderer-accessibility",
		"nothing named in them: Some Canvas App",
		"not on the accessibility bus at all: xterm",
		// The cap is said out loud. A silent one reads as "that was everything".
		"3 more window(s) were not examined",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report is missing %q:\n%s", want, got)
		}
	}
}

// A desktop with nothing to report says nothing, or the capability report grows
// an empty heading on every machine that has no graphical session.
func TestTheDesktopSectionIsSilentWithNothingToSay(t *testing.T) {
	if got := desktopSection(nil, nil, nil, nil, 0); got != "" {
		t.Errorf("an empty desktop still produced a report: %q", got)
	}
	// And one readable window is enough to be worth saying.
	if got := desktopSection([]string{"Files"}, nil, nil, nil, 0); !strings.Contains(got, "Files") {
		t.Errorf("a readable window was not reported: %q", got)
	}
}
