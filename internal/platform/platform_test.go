package platform

import (
	"runtime"
	"strings"
	"testing"
)

// An absent capability must always say what would fix it.
//
// This is the whole reason the package exists. The answers used to live where
// they were needed, and were inconsistent in the way scattered answers always
// are: voice named the packages to install, desktop named the display server,
// and the notifier said nothing at all — which meant a machine without
// notify-send dropped every observation the daemon raised, in silence, and that
// was a real bug.
//
// A refusal that names the fix costs one round. A bare refusal costs five, or in
// the notifier's case cost everything, because nobody knew to look.
func TestEveryAbsentCapabilityNamesItsFix(t *testing.T) {
	i := Probe()
	for _, row := range []struct {
		name string
		c    Capability
	}{
		{"windows", i.Windows},
		{"input", i.Input},
		{"screenshot", i.Screenshot},
		{"notify", i.Notify},
		{"record", i.Record},
		{"speak", i.Speak},
		{"browser", i.Browser},
		{"accessibility", i.Accessibility},
	} {
		if row.c.Available {
			if row.c.How == "" {
				t.Errorf("%s is available and does not say how", row.name)
			}
			if row.c.Why != "" {
				t.Errorf("%s is available and still explains an absence: %q", row.name, row.c.Why)
			}
			continue
		}
		if strings.TrimSpace(row.c.Why) == "" {
			t.Errorf("%s is unavailable and says nothing about why", row.name)
		}
		if len(row.c.Why) < 20 {
			t.Errorf("%s gives a reason too short to act on: %q", row.name, row.c.Why)
		}
	}
}

// The OS has to be recognised, or every reason below it is written for a machine
// this is not.
func TestTheOperatingSystemIsRecognised(t *testing.T) {
	got := Probe().OS
	if got == Unknown {
		t.Fatalf("running on %s and the platform layer does not know what that is", runtime.GOOS)
	}
	want := map[string]OS{"linux": Linux, "darwin": MacOS, "windows": Windows}[runtime.GOOS]
	if want != "" && got != want {
		t.Errorf("OS is %q, want %q", got, want)
	}
}

// A machine with no session attached is a real answer, not a failure. It is what
// a container and a systemd unit started before login both look like, and the
// tools should say so rather than blaming a missing binary.
func TestNoGraphicalSessionIsItsOwnAnswer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the display server question only has this shape on Linux")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")

	i := Probe()
	if i.Display != Headless {
		t.Fatalf("display is %q with neither variable set, want headless", i.Display)
	}
	for _, c := range []Capability{i.Windows, i.Input, i.Accessibility} {
		if c.Available {
			t.Error("a capability claimed to work with no session attached")
		}
		if !strings.Contains(c.Why, "graphical session") {
			t.Errorf("the reason blames something other than the missing session: %q", c.Why)
		}
	}
}

// Wayland is the system working, not a missing dependency, and the reason has to
// say so or someone spends an afternoon installing xdotool again.
func TestWaylandIsExplainedRatherThanBlamedOnAMissingBinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Wayland only applies on Linux")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")

	i := Probe()
	if i.Display != Wayland {
		t.Fatalf("display is %q, want wayland", i.Display)
	}
	if i.Input.Available {
		t.Error("synthetic input claimed to work under Wayland")
	}
	if !strings.Contains(i.Input.Why, "Wayland") {
		t.Errorf("the reason does not mention Wayland: %q", i.Input.Why)
	}
	if strings.Contains(i.Input.Why, "install") {
		t.Errorf("the reason tells them to install something that will not help: %q", i.Input.Why)
	}
	// Screenshots survive, because grim works where xdotool cannot. Losing that
	// distinction would take away the one thing she can still do there.
	if i.Screenshot.Available && i.Screenshot.How == "" {
		t.Error("screenshots are available and do not say how")
	}
}

// X11 with the tools installed is the case everything else is measured against.
func TestX11WithTheToolsPresentIsUsable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("X11 only applies on Linux")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")

	i := Probe()
	if i.Display != X11 {
		t.Fatalf("display is %q with DISPLAY set, want x11", i.Display)
	}
	// Whether xdotool is installed here is not the point; whether the answer is
	// coherent is. Available means it names a mechanism, absent means it names a
	// fix, and the previous test covers both.
	if i.Input.Available == (i.Input.How == "") {
		t.Errorf("input is incoherent: available=%v how=%q", i.Input.Available, i.Input.How)
	}
}

// Describe is what a status line prints, so it has to name every capability. A
// missing row is a capability nobody thinks to ask about.
func TestDescribeMentionsEveryCapability(t *testing.T) {
	got := Probe().Describe()
	for _, want := range []string{
		"windows", "input", "screenshot", "notify",
		"record", "speak", "browser", "accessibility",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the description leaves out %q:\n%s", want, got)
		}
	}
}

// Current caches and Probe does not, which is the difference between them.
//
// Probing looks for binaries on disk and reads the environment, so the answer is
// stable under a running process and worth holding. Probe exists for the two
// cases where it is not: a test that moves the environment, and the rare machine
// where something was installed while she was running.
func TestCurrentCachesAndProbeDoesNot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("this moves DISPLAY, which only decides anything on Linux")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")

	first := Current()
	if first.Display != Current().Display {
		t.Fatal("two calls to Current disagreed with nothing changing")
	}

	// Move the ground under it.
	t.Setenv("DISPLAY", "")
	if Current().Display != first.Display {
		t.Error("Current re-probed instead of using the cached answer")
	}
	if Probe().Display == first.Display {
		t.Error("Probe returned the cached answer instead of re-examining")
	}
}
