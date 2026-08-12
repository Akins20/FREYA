// Package platform answers what this machine can actually do, and says what is
// missing when it cannot.
//
// # Why capabilities rather than an operating system
//
// No call site wants to know whether this is Linux. They want to know whether a
// window can be driven right now, and what to tell the user when it cannot. The
// operating system is one input to that. Whether a display server is running,
// and whether xdotool was ever installed, matter exactly as much, and a check
// that only asks runtime.GOOS gets both of them wrong on the same machine.
//
// # Why it is centralised
//
// Because the answers were scattered, and inconsistent in the way scattered
// answers always are. Voice said "install sox or alsa-utils". Desktop said
// "xdotool only works under X11". The notifier said nothing at all: a missing
// notify-send meant every observation the daemon raised was dropped in silence,
// which was a real bug fixed by making it speak. One of those three is not like
// the others, and the only reason is that each was written where it was needed
// rather than once.
//
// So a capability is never just absent. It is absent with a reason, and the
// reason names what would fix it — the same argument as the affordances attached
// to a failing tool: a refusal that says what to do instead costs one round, and
// a bare refusal costs five.
//
// # What it deliberately does not do
//
// It does not abstract over the platforms. There is no Driver interface with one
// implementation, because an abstraction shaped around the only platform you have
// is an abstraction you will rewrite. It reports what is available and why, and
// the tools decide. When macOS and Windows arrive they add probes, not layers.
package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// OS is the operating system family, as far as anything here cares.
type OS string

const (
	Linux   OS = "linux"
	MacOS   OS = "macos"
	Windows OS = "windows"
	Unknown OS = "unknown"
)

// Display is the window system in front of the user, when there is one.
type Display string

const (
	// X11 is the only display server the desktop tools can currently drive.
	X11 Display = "x11"
	// Wayland is a session with no way in: xdotool and wmctrl both refuse, by
	// design, because synthetic input is exactly what Wayland set out to stop.
	Wayland Display = "wayland"
	// XWayland is a Wayland session running an X server beside it, which is what
	// nearly every Wayland desktop actually is.
	//
	// It matters because the refusal above is only true of the native half. An
	// XWayland client is an ordinary X11 client, and xdotool and wmctrl drive it
	// exactly as they would on a plain X11 login. Reading WAYLAND_DISPLAY and
	// stopping meant refusing every window on a machine where most of them were
	// drivable — found on WSLg, where WAYLAND_DISPLAY and DISPLAY are both set
	// out of the box and the desktop tools declined to touch an X server that
	// was answering the whole time.
	XWayland Display = "xwayland"
	// Quartz is macOS. Driving it needs the Accessibility permission, which is
	// granted per application by a human and cannot be asked for from code.
	Quartz Display = "quartz"
	// DesktopWindows is the Windows desktop.
	DesktopWindows Display = "windows"
	// Headless is a machine with no session attached: a server, a container, or
	// a daemon started before anyone logged in.
	Headless Display = "headless"
)

// Capability is one thing she may or may not be able to do here.
type Capability struct {
	// Available is whether it works right now.
	Available bool
	// How names the mechanism, for a report. Empty when unavailable.
	How string
	// Why explains the absence in terms the user can act on, and names what
	// would fix it. Empty when available.
	Why string
	// Caveat is where something works but not everywhere, and is empty in the
	// ordinary case.
	//
	// Available and Why were the whole vocabulary, and a yes/no vocabulary
	// forces a partial answer to round to one of them. Both roundings are
	// wrong: rounding down refuses work that would have succeeded, rounding up
	// promises work that will fail on half the windows and gives no hint why.
	Caveat string
}

// String renders a capability the way a status line wants it.
func (c Capability) String() string {
	if c.Available {
		s := "yes (" + c.How + ")"
		if c.Caveat != "" {
			s += " — " + c.Caveat
		}
		return s
	}
	if c.Why == "" {
		return "no"
	}
	return "no — " + c.Why
}

// Info is what this machine can do, probed once.
type Info struct {
	OS      OS
	Display Display
	// Windows is listing, focusing and reading window titles.
	Windows Capability
	// Input is synthetic keyboard and pointer events into whatever has focus.
	Input Capability
	// Screenshot is capturing the screen or the focused window to a file.
	Screenshot Capability
	// Notify is putting a message in front of the user without speaking.
	Notify Capability
	// Record is capturing audio from the microphone.
	Record Capability
	// Speak is text to speech.
	Speak Capability
	// Browser is a Chrome or Chromium that the DevTools Protocol can drive.
	Browser Capability
	// Accessibility is reading an application's own element tree, which is what
	// the desktop tools need to stop working from a photograph. Nothing provides
	// it yet on any platform; the probe exists so the answer is "no, and here is
	// what would give it to you" rather than nothing at all.
	Accessibility Capability
}

var (
	once   sync.Once
	cached Info
)

// Current returns what this machine can do. Probed once: the answers involve
// looking for binaries on disk, and they do not change under a running process.
func Current() Info {
	once.Do(func() { cached = probe() })
	return cached
}

// Probe re-examines the machine, ignoring the cache. For tests, and for the
// rare case where something was installed while she was running.
func Probe() Info { return probe() }

func probe() Info {
	i := Info{OS: currentOS()}
	i.Display = currentDisplay(i.OS)

	i.Browser = probeBrowser()
	i.Notify = probeNotify(i.OS)
	i.Record = probeRecord(i.OS)
	i.Speak = probeSpeak(i.OS)

	i.Windows, i.Input, i.Screenshot = probeDesktop(i.OS, i.Display)
	i.Accessibility = probeAccessibility(i.OS, i.Display)
	return i
}

func currentOS() OS {
	switch runtime.GOOS {
	case "linux":
		return Linux
	case "darwin":
		return MacOS
	case "windows":
		return Windows
	default:
		return Unknown
	}
}

// currentDisplay reports the window system, or that there is nobody looking.
//
// The Linux answer comes from the environment rather than from a library,
// because that is where it actually lives and because a daemon started by
// systemd before login legitimately has neither variable set. Headless is a real
// answer, not a failure: it is what a container and a server both look like, and
// several tools should behave differently there rather than fail.
func currentDisplay(os_ OS) Display {
	switch os_ {
	case MacOS:
		return Quartz
	case Windows:
		return DesktopWindows
	case Linux:
		if os.Getenv("WAYLAND_DISPLAY") != "" || waylandSocket() {
			// Asked, not assumed. This is the one branch where the environment
			// is genuinely ambiguous, and it is the branch the package doc is
			// about: two variables are set, only one of them decides whether a
			// window can be driven, and the answer is a question for the X
			// server rather than for os.Getenv.
			if xAnswers() {
				return XWayland
			}
			return Wayland
		}
		if os.Getenv("DISPLAY") != "" {
			return X11
		}
		return Headless
	default:
		return Headless
	}
}

// have reports whether a binary is on PATH.
func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// waylandSocket looks for a compositor that is running without announcing itself.
//
// Unsetting WAYLAND_DISPLAY does not select X11. libwayland falls back to the
// socket name "wayland-0" in XDG_RUNTIME_DIR when the variable is absent, so a
// toolkit will still connect to a compositor that is there.
//
// Measured: a GTK 3 application started with WAYLAND_DISPLAY unset and DISPLAY
// pointing at a running X server connected to the compositor anyway and put its
// window where neither wmctrl nor xdotool could see it. Reading the variable and
// stopping would have reported that machine as a plain X11 session and offered
// synthetic input into an X server with no application windows on it — the
// mirror image of the bug above, and the same mistake.
func waylandSocket() bool {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "wayland-0"))
	return err == nil
}

// xAnswers is how currentDisplay asks, as a variable so a test can decide the
// answer instead of the machine deciding it.
//
// The same seam as review's renderPage, for the same reason: without it the
// XWayland branch is only observable on a machine that happens to be running
// XWayland, and the pure-Wayland branch is only observable on one that is not.
// Both are real configurations and neither is the machine this is developed on.
var xAnswers = xServerAnswers

// xServerAnswers asks whether DISPLAY points at an X server that will talk to us.
//
// getdisplaygeometry because it needs nothing but a connection: no window has to
// exist, none has to be focused, and it cannot be confused by an empty desktop.
// Bounded, because an X server that accepts the socket and then never replies
// would otherwise hang the one-time probe and every tool waiting behind it.
func xServerAnswers() bool {
	if os.Getenv("DISPLAY") == "" || !have("xdotool") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "xdotool", "getdisplaygeometry").Run() == nil
}

// firstAvailable returns the first binary of the list that exists.
func firstAvailable(bins ...string) string {
	for _, b := range bins {
		if have(b) {
			return b
		}
	}
	return ""
}

// missing renders the standard "install one of these" refusal.
func missing(what string, bins ...string) string {
	return fmt.Sprintf("%s needs one of %s on PATH and none is installed",
		what, strings.Join(bins, ", "))
}

func probeBrowser() Capability {
	if bin := firstAvailable("google-chrome", "google-chrome-stable", "chromium", "chromium-browser"); bin != "" {
		return Capability{Available: true, How: bin}
	}
	return Capability{Why: missing("driving a browser",
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser")}
}

func probeNotify(os_ OS) Capability {
	switch os_ {
	case Linux:
		if have("notify-send") {
			return Capability{Available: true, How: "notify-send"}
		}
		return Capability{Why: "desktop notifications need notify-send, which is in " +
			"libnotify-bin — without it she notices things and cannot tell you"}
	case MacOS:
		if have("osascript") {
			return Capability{Available: true, How: "osascript"}
		}
		return Capability{Why: "notifications need osascript, which ships with macOS; " +
			"its absence means something is very wrong with this machine"}
	case Windows:
		return Capability{Why: "no notifier is implemented for Windows yet; " +
			"observations are journalled but nothing pops"}
	}
	return Capability{Why: "no notifier is known for this platform"}
}

func probeRecord(os_ OS) Capability {
	if bin := firstAvailable("sox", "rec", "arecord"); bin != "" {
		return Capability{Available: true, How: bin}
	}
	if os_ == MacOS {
		return Capability{Why: "recording needs sox (brew install sox)"}
	}
	return Capability{Why: missing("recording", "sox", "rec", "arecord")}
}

func probeSpeak(os_ OS) Capability {
	if os_ == MacOS && have("say") {
		return Capability{Available: true, How: "say"}
	}
	if bin := firstAvailable("espeak-ng", "espeak", "piper"); bin != "" {
		return Capability{Available: true, How: bin}
	}
	return Capability{Why: missing("offline speech", "espeak-ng", "espeak", "piper") +
		" (the hosted voice still works and sounds better)"}
}

// probeDesktop answers the three window questions together, because on every
// platform they stand or fall on the same thing.
func probeDesktop(os_ OS, d Display) (windows, input, screenshot Capability) {
	switch {
	case d == Headless:
		why := "there is no graphical session attached to this machine"
		return Capability{Why: why}, Capability{Why: why}, Capability{Why: why}

	case d == Wayland:
		// Not an omission. Wayland exists partly to stop one application
		// synthesising input into another, so this is the system working.
		why := "this is a Wayland session, where one application cannot drive " +
			"another's windows; log into an X11 session to enable it"
		return Capability{Why: why}, Capability{Why: why},
			shotCapability(os_)

	case d == XWayland:
		// A qualified yes, because the truth is qualified. X11 clients under
		// XWayland are drivable and native Wayland clients are not, and nothing
		// here can tell which a given window is without trying it — so the
		// caveat travels with the yes rather than being resolved by guessing.
		caveat := "this is a Wayland session with an X server beside it, so X11 " +
			"windows respond and native Wayland ones do not; a window that ignores " +
			"input is the likely sign of the second kind"
		w := Capability{Why: missing("listing and focusing windows", "wmctrl", "xdotool")}
		if bin := firstAvailable("wmctrl", "xdotool"); bin != "" {
			w = Capability{Available: true, How: bin, Caveat: caveat}
		}
		in := Capability{Why: missing("synthetic keyboard input", "xdotool")}
		if have("xdotool") {
			in = Capability{Available: true, How: "xdotool", Caveat: caveat}
		}
		return w, in, shotCapability(os_)

	case os_ == Linux:
		w := Capability{Why: missing("listing and focusing windows", "wmctrl", "xdotool")}
		if bin := firstAvailable("wmctrl", "xdotool"); bin != "" {
			w = Capability{Available: true, How: bin}
		}
		in := Capability{Why: missing("synthetic keyboard input", "xdotool")}
		if have("xdotool") {
			in = Capability{Available: true, How: "xdotool"}
		}
		return w, in, shotCapability(os_)

	case os_ == MacOS:
		why := "driving windows on macOS needs the Accessibility permission, " +
			"granted per application in System Settings, and no backend is implemented yet"
		return Capability{Why: why}, Capability{Why: why}, shotCapability(os_)

	case os_ == Windows:
		why := "no window backend is implemented for Windows yet"
		return Capability{Why: why}, Capability{Why: why}, shotCapability(os_)
	}
	why := "this platform is not known here"
	return Capability{Why: why}, Capability{Why: why}, Capability{Why: why}
}

func shotCapability(os_ OS) Capability {
	switch os_ {
	case MacOS:
		if have("screencapture") {
			return Capability{Available: true, How: "screencapture"}
		}
	case Linux:
		// grim is the Wayland one, and it works where xdotool cannot, which is
		// why screenshots survive a Wayland session even though input does not.
		if bin := firstAvailable("scrot", "grim", "import", "gnome-screenshot"); bin != "" {
			return Capability{Available: true, How: bin}
		}
		return Capability{Why: missing("screenshots", "scrot", "grim", "import", "gnome-screenshot")}
	}
	return Capability{Why: "no screenshot tool is known for this platform"}
}

// probeAccessibility looks for a way to read an application's own element tree.
//
// This is the capability that would let the desktop tools stop working from a
// photograph. In the browser she reads the DOM, pierces shadow roots and is told
// when a dialog opened; outside it she has a screenshot and a keystroke. An
// accessibility tree is the same information for native applications, and every
// platform exposes one: AT-SPI over D-Bus on Linux, the Accessibility API on
// macOS, UI Automation on Windows.
//
// Nothing reads any of them yet. The probe exists so the answer is "no, and here
// is what would provide it" rather than nothing at all, and so the first backend
// has somewhere to announce itself.
func probeAccessibility(os_ OS, d Display) Capability {
	if d == Headless {
		return Capability{Why: "there is no graphical session attached to this machine"}
	}
	switch os_ {
	case Linux:
		// Asked of the bus rather than inferred from a binary being installed.
		//
		// The first version of this said "AT-SPI is reachable here through busctl"
		// whenever busctl existed, which is two independent facts glued together:
		// systemd ships busctl on machines with no accessibility registry running
		// at all, and at-spi2-core ships a registry on machines with neither
		// busctl nor gdbus. Measured in a container with the registry up and both
		// binaries absent, and again with both present, which is the only reason
		// the difference is visible.
		//
		// No hand-written D-Bus client. She already shells out to twenty-odd
		// binaries, so one more is consistent, and the protocol is binary.
		if !have("gdbus") {
			return Capability{Why: "reading an application's element tree needs AT-SPI, " +
				"reached through gdbus, which is in libglib2.0-bin and is not installed"}
		}
		if addr := a11yBusAddress(); addr == "" {
			return Capability{Why: "gdbus is here but no accessibility registry answered on " +
				"the session bus — install at-spi2-core, and on a bare session start it with " +
				"/usr/libexec/at-spi-bus-launcher --launch-immediately"}
		}
		return Capability{Why: "AT-SPI is up and answering, and nothing reads it yet"}
	case MacOS:
		return Capability{Why: "the macOS Accessibility API needs a permission granted " +
			"per application in System Settings, and nothing reads it yet"}
	case Windows:
		return Capability{Why: "Windows UI Automation is available to any process " +
			"and nothing reads it yet"}
	}
	return Capability{Why: "no accessibility backend is known for this platform"}
}

// Describe renders the whole picture, for a status command and for the line the
// daemon prints when it starts.
func (i Info) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s / %s\n", i.OS, i.Display)
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
		fmt.Fprintf(&b, "  %-14s %s\n", row.name, row.c)
	}
	return strings.TrimRight(b.String(), "\n")
}

// a11yBusAddress asks the session bus where the accessibility bus lives, and
// returns empty when nothing answers.
//
// Two hops, which is the part that is easy to get wrong: the accessibility tree
// is not on the session bus. The session bus only knows the address of a second,
// private bus, and everything interesting is over there. A probe that stops after
// finding gdbus has established nothing at all.
func a11yBusAddress() string {
	out, err := exec.Command("gdbus", "call", "--session",
		"--dest", "org.a11y.Bus",
		"--object-path", "/org/a11y/bus",
		"--method", "org.a11y.Bus.GetAddress").Output()
	if err != nil {
		return ""
	}
	// gdbus wraps a reply in a tuple: ('unix:path=/run/user/1000/at-spi/bus',)
	s := strings.TrimSpace(string(out))
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	s = strings.TrimSuffix(strings.TrimSpace(s), ",")
	s = strings.Trim(strings.TrimSpace(s), "'")
	if !strings.HasPrefix(s, "unix:") {
		return ""
	}
	return s
}
