package platform

import (
	"os"
	"os/exec"
	"path/filepath"
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
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

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
	// A Wayland session with no X server beside it, which is the only kind that
	// is genuinely closed. Pinned rather than inherited from the machine: this
	// test used to set WAYLAND_DISPLAY alone and pass on the developer's X11
	// box only because nothing looked any further.
	noXServer(t)

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

// noXServer and anXServer decide the question currentDisplay asks, so both
// Wayland branches are reachable from any machine.
func noXServer(t *testing.T) { t.Helper(); swapXProbe(t, func() bool { return false }) }
func anXServer(t *testing.T) { t.Helper(); swapXProbe(t, func() bool { return true }) }
func swapXProbe(t *testing.T, fn func() bool) {
	prev := xAnswers
	xAnswers = fn
	t.Cleanup(func() { xAnswers = prev })
}

// A Wayland session with an X server beside it is not a closed session, and
// refusing it costs every window on the machine.
//
// This is what nearly every Wayland desktop is, and what WSLg is out of the
// box. Reading WAYLAND_DISPLAY and stopping meant answering "one application
// cannot drive another's windows" while an X server sat there answering, with
// xdotool and wmctrl installed, driving X11 clients perfectly well. The fix is
// not to assume the other way: it is to ask, and to carry the half of the
// refusal that is still true.
func TestAWaylandSessionWithAnXServerIsNotRefusedOutright(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XWayland only applies on Linux")
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")
	anXServer(t)

	i := Probe()
	if i.Display != XWayland {
		t.Fatalf("display is %q with an X server answering, want xwayland", i.Display)
	}
	// Whether xdotool is installed on the machine running the tests is not the
	// point. What must hold is that the answer is no longer the flat Wayland
	// refusal, and that if it is a yes it says what it does not cover.
	if !i.Input.Available {
		if strings.Contains(i.Input.Why, "log into an X11 session") {
			t.Errorf("an XWayland session was refused as though it were closed: %q", i.Input.Why)
		}
		return // xdotool is simply not installed here, which the other tests cover
	}
	if i.Input.Caveat == "" {
		t.Error("input claims to work under XWayland without saying what it cannot reach")
	}
	if !strings.Contains(i.Input.Caveat, "Wayland") {
		t.Errorf("the caveat does not name the half that will not respond: %q", i.Input.Caveat)
	}
	if !strings.Contains(i.Input.String(), i.Input.Caveat) {
		t.Errorf("the caveat is not printed where anyone would read it: %q", i.Input.String())
	}
}

// X11 with the tools installed is the case everything else is measured against.
func TestX11WithTheToolsPresentIsUsable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("X11 only applies on Linux")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	// And no compositor listening on the default socket either, which is a
	// separate question from the variable and is asked separately.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

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
	// Isolated so that moving DISPLAY is the only thing that can move the
	// answer. Without this the test ran on a machine with a compositor socket
	// in XDG_RUNTIME_DIR, where the display question is decided before DISPLAY
	// is ever read — so the ground did not move, and a test about caching
	// failed for a reason that had nothing to do with caching.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	noXServer(t)

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

// The accessibility answer must come from the bus, not from a binary existing.
//
// The first version reported "AT-SPI is reachable here through busctl" whenever
// busctl was installed. Those are two independent facts glued together: systemd
// ships busctl on machines with no accessibility registry running at all, and
// at-spi2-core ships a registry on machines with neither busctl nor gdbus.
// Measured both ways in a container, which is the only reason the difference was
// visible at all.
func TestAccessibilityIsAnsweredByTheBusNotByABinary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("AT-SPI is the Linux answer")
	}
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")

	c := Probe().Accessibility
	_, missingGdbus := exec.LookPath("gdbus")
	answered := a11yBusAddress() != ""

	switch {
	case answered:
		// It reads the tree now. This assertion used to be the reverse — that
		// nothing could possibly claim to read one — and it stayed that way
		// while desktop_inspect, desktop_click, desktop_type_into and
		// desktop_menu were all built on top and verified against four
		// toolkits. The one tool whose job is to answer "what can I do here"
		// went on saying no, and two call sites worked around it by matching a
		// word inside the refusal.
		if !c.Available {
			t.Errorf("the bus answered and the capability still says no: %q", c.Why)
		}
		if c.How == "" {
			t.Error("accessibility is available and does not say through what")
		}
	case missingGdbus != nil:
		if c.Available {
			t.Error("accessibility claims to work with no gdbus to reach the bus")
		}
		if !strings.Contains(c.Why, "not installed") {
			t.Errorf("gdbus is absent and the reason does not say so: %q", c.Why)
		}
	default:
		if c.Available {
			t.Error("accessibility claims to work with nothing answering on the bus")
		}
		if !strings.Contains(c.Why, "no accessibility registry answered") {
			t.Errorf("the bus is silent and the reason blames something else: %q", c.Why)
		}
	}
}

// And the address parser has to survive the shape gdbus actually returns, which
// is a D-Bus tuple rather than a bare string.
func TestTheA11yAddressIsEmptyWhenNothingAnswers(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no gdbus anywhere
	if got := a11yBusAddress(); got != "" {
		t.Errorf("an address came back with no gdbus on PATH: %q", got)
	}
}

// A compositor that is running without saying so is still a compositor.
//
// Unsetting WAYLAND_DISPLAY does not select X11: libwayland falls back to the
// socket name "wayland-0", so a toolkit connects to whatever is listening there
// regardless of the variable. Measured on WSLg — a GTK 3 application started
// with the variable unset and DISPLAY pointing at a running X server became a
// Wayland client and put its window where wmctrl and xdotool could not see it,
// while the probe would have called that machine a plain X11 session and
// offered synthetic input into an X server with no application windows on it.
func TestACompositorIsFoundWithoutTheEnvironmentVariable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Wayland socket only exists on Linux")
	}
	dir := t.TempDir()
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("XDG_RUNTIME_DIR", dir)

	// Nothing listening: this is a plain X11 session and must stay one.
	noXServer(t)
	if got := Probe().Display; got != X11 {
		t.Fatalf("display is %q with no compositor socket, want x11", got)
	}

	// The socket appears, and the answer changes even though no variable did.
	if err := os.WriteFile(filepath.Join(dir, "wayland-0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Probe().Display; got != Wayland {
		t.Errorf("display is %q with a compositor socket and no X server, want wayland", got)
	}
	anXServer(t)
	if got := Probe().Display; got != XWayland {
		t.Errorf("display is %q with a compositor socket and an X server, want xwayland", got)
	}
}
