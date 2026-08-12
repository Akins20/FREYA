package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/a11y"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/platform"
)

// Desktop control for X11, built on xdotool and wmctrl.
//
// # Why synthetic input is gated harder than shell commands
//
// A shell command is legible: guard can read `rm -rf /tmp/x` and reason about
// it. Synthetic keystrokes are not. `xdotool type "rm -rf /"` looks identical
// to typing a search query — the danger lives entirely in which window happens
// to hold focus at that instant, which no static analysis can see.
//
// So every input action routes through guard as KindInput, which never
// auto-approves, and the confirmation shows both the keystrokes and the window
// that will receive them.

// RegisterDesktop adds window, screenshot and input skills.
func RegisterDesktop(r *Registry, g *guard.Guard) {
	// The native counterpart to browser_inspect. See registerDesktopInspect.
	registerDesktopInspect(r)
	// And the other half: reading a control she cannot press stops short of
	// useful. See registerDesktopClick.
	registerDesktopClick(r, g)
	// And filling one in. See registerDesktopTypeInto: keystrokes at whatever has
	// focus cannot say afterwards whether they landed where they were aimed.
	registerDesktopTypeInto(r, g)
	// And the menus, which are the thing a person uses constantly and the one
	// place keystrokes reach least. See registerDesktopMenu.
	registerDesktopMenu(r, g)

	if g == nil {
		return
	}
	d := &desktop{guard: g}

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "desktop_windows",
			Description: "List open windows with their titles and which one has focus.",
			Params:      llm.ObjectSchema(nil),
		},
		Handler: d.listWindows,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_focus",
			Description: "Bring a window to the front by matching its title. " +
				"Use desktop_windows first to see what is open.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"title": {Type: "string", Description: "Substring of the window title."},
			}, "title"),
		},
		Serial:  true,
		Handler: d.focusWindow,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_screenshot",
			Description: "Capture the screen to a file so you can see what the user sees. " +
				"Returns the path.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"window": {Type: "boolean", Description: "Capture only the focused window " +
					"rather than the whole screen."},
			}),
		},
		Handler: d.screenshot,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_type",
			Description: "Type text into whatever window currently has focus. " +
				"Always confirm the right window is focused first — the keystrokes go " +
				"wherever focus happens to be. Never use this for passwords.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"text":   {Type: "string", Description: "The text to type."},
				"reason": {Type: "string", Description: "Why, shown to the user when confirming."},
			}, "text"),
		},
		Serial:  true,
		Handler: d.typeText,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_key",
			Description: "Send a key combination to the focused window, e.g. 'ctrl+s', " +
				"'alt+Tab', 'Return', 'Escape'.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"keys":   {Type: "string", Description: "xdotool key syntax, e.g. 'ctrl+shift+t'."},
				"reason": {Type: "string", Description: "Why, shown to the user when confirming."},
			}, "keys"),
		},
		Serial:  true,
		Handler: d.sendKeys,
	})
}

type desktop struct{ guard *guard.Guard }

// requireX11 gives a precise reason when the display is unavailable, rather
// than letting xdotool fail with something cryptic.
func requireX11() error {
	// Asked of internal/platform rather than of the environment directly, so
	// there is one answer to "can a window be driven here" and one place that
	// knows what to tell someone when it cannot. The reasons it hands back name
	// the fix, which a bare DISPLAY check cannot: a Wayland session is the system
	// working as designed and no amount of installing xdotool will change it.
	if in := platform.Current().Input; !in.Available {
		return fmt.Errorf("%s", in.Why)
	}
	return nil
}

func (d *desktop) listWindows(ctx context.Context, _ map[string]any) (string, error) {
	if err := requireX11(); err != nil {
		return "", err
	}
	if !have("wmctrl") {
		return "", fmt.Errorf("wmctrl is not installed")
	}

	out, err := run(ctx, 10*time.Second, "wmctrl", "-l")
	if err != nil {
		return "", err
	}

	active := ""
	if id, err := run(ctx, 5*time.Second, "xdotool", "getactivewindow"); err == nil {
		if name, err := run(ctx, 5*time.Second, "xdotool", "getwindowname", id); err == nil {
			active = strings.TrimSpace(name)
		}
	}

	var sb strings.Builder
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		title := wmctrlTitle(line)
		if title == "" {
			continue
		}
		marker := " "
		if active != "" && title == active {
			marker = "*"
		}
		fmt.Fprintf(&sb, "%s %s\n", marker, title)
		count++
	}
	if count == 0 {
		return "No windows open.", nil
	}
	return fmt.Sprintf("%d windows (* = focused):\n%s", count, strings.TrimSpace(sb.String())), nil
}

func (d *desktop) focusWindow(ctx context.Context, args map[string]any) (string, error) {
	if err := requireX11(); err != nil {
		return "", err
	}
	title := argString(args, "title")
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	action := guard.Action{
		Kind:    guard.KindSystem,
		Command: "wmctrl",
		Args:    []string{"-a", title},
		Reason:  "focus the window matching " + title,
	}
	return d.guard.Run(ctx, action, func(ctx context.Context) (string, error) {
		if _, err := run(ctx, 10*time.Second, "wmctrl", "-a", title); err != nil {
			return "", fmt.Errorf("no window matching %q: %w", title, err)
		}
		return "Focused " + title + ".", nil
	})
}

func (d *desktop) screenshot(ctx context.Context, args map[string]any) (string, error) {
	if err := requireX11(); err != nil {
		return "", err
	}
	if !have("scrot") {
		return "", fmt.Errorf("scrot is not installed")
	}

	path := filepath.Join(os.TempDir(), fmt.Sprintf("freya-shot-%d.png", time.Now().Unix()))
	cmdArgs := []string{path}
	if argBool(args, "window") {
		cmdArgs = append([]string{"-u"}, cmdArgs...)
	}

	if _, err := run(ctx, 15*time.Second, "scrot", cmdArgs...); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("screenshot was not written: %w", err)
	}
	// Says what kind of knowledge this is, because the two ways she can learn
	// about a native window produce claims of very different strength and read
	// identically in a reply.
	//
	// desktop_inspect asks the application what it is; a screenshot asks the
	// screen what it looks like. "The Save button is greyed out" read from the
	// tree is a fact the toolkit stated. Inferred from pixels it is a guess about
	// a colour, and it is wrong on any theme with low contrast. Whoever reads her
	// answer cannot tell those apart unless she says, and she has no reason to
	// say unless something tells her the difference matters here.
	return fmt.Sprintf("Screenshot saved to %s (%.0f KB).\n\n[This is pixels, not the "+
		"application's own description of itself. Anything you conclude from it is "+
		"inferred from how it looks — say so when you report it. desktop_inspect asks "+
		"the window what its controls are and what they are called, which is the "+
		"stronger answer where the toolkit supports it.]",
		path, float64(info.Size())/1024), nil
}

// focusedWindowName reports what will receive synthetic input, so the
// confirmation can name it rather than leaving the user to guess.
func focusedWindowName(ctx context.Context) string {
	id, err := run(ctx, 5*time.Second, "xdotool", "getactivewindow")
	if err != nil {
		return "unknown window"
	}
	name, err := run(ctx, 5*time.Second, "xdotool", "getwindowname", strings.TrimSpace(id))
	if err != nil {
		return "unknown window"
	}
	return strings.TrimSpace(name)
}

func (d *desktop) typeText(ctx context.Context, args map[string]any) (string, error) {
	if err := requireX11(); err != nil {
		return "", err
	}
	text := argString(args, "text")
	if text == "" {
		return "", fmt.Errorf("text is required")
	}
	if !have("xdotool") {
		return "", fmt.Errorf("xdotool is not installed")
	}

	target := focusedWindowName(ctx)
	reason := argString(args, "reason")
	if reason == "" {
		reason = "type into the focused window"
	}

	action := guard.Action{
		Kind:    guard.KindInput,
		Command: "xdotool type",
		Args:    []string{text},
		// Naming the receiving window is the whole point: the same keystrokes
		// are harmless in a text editor and destructive in a terminal.
		Reason: fmt.Sprintf("%s — will be typed into: %s", reason, target),
	}

	return d.guard.Run(ctx, action, func(ctx context.Context) (string, error) {
		if _, err := run(ctx, 30*time.Second, "xdotool", "type", "--delay", "12", "--", text); err != nil {
			return "", err
		}
		return fmt.Sprintf("Typed %d characters into %s.", len(text), target), nil
	})
}

func (d *desktop) sendKeys(ctx context.Context, args map[string]any) (string, error) {
	if err := requireX11(); err != nil {
		return "", err
	}
	keys := argString(args, "keys")
	if keys == "" {
		return "", fmt.Errorf("keys is required")
	}
	if !have("xdotool") {
		return "", fmt.Errorf("xdotool is not installed")
	}

	target := focusedWindowName(ctx)
	reason := argString(args, "reason")
	if reason == "" {
		reason = "send a key combination"
	}

	action := guard.Action{
		Kind:    guard.KindInput,
		Command: "xdotool key",
		Args:    []string{keys},
		Reason:  fmt.Sprintf("%s — will be sent to: %s", reason, target),
	}

	return d.guard.Run(ctx, action, func(ctx context.Context) (string, error) {
		if _, err := run(ctx, 15*time.Second, "xdotool", "key", "--", keys); err != nil {
			return "", err
		}
		return fmt.Sprintf("Sent %s to %s.", keys, target), nil
	})
}

// onScreenTitles lists the window titles the window manager knows about.
//
// Separate from listWindows because the interesting question here is not what to
// show the user, it is whether a particular title exists at all — which is what
// distinguishes a window that publishes nothing from a window that is not there.
func onScreenTitles(ctx context.Context) ([]string, error) {
	if !have("wmctrl") {
		return nil, fmt.Errorf("wmctrl is not installed, so what is on screen is unknown")
	}
	out, err := run(ctx, 10*time.Second, "wmctrl", "-l")
	if err != nil {
		// A failure here is not an empty desktop. Measured: with no EWMH window
		// manager running, wmctrl exits non-zero with "Cannot get client list
		// properties", and treating that as an empty list produced the message
		// "no window matching XtermTarget is open, and nothing else is either"
		// while the xterm was on screen the whole time. Reporting a tool's failure
		// as a fact about the world is the mistake this file's own comments are
		// about.
		return nil, fmt.Errorf("the window manager could not be asked what is open: %w", err)
	}
	var titles []string
	for _, line := range strings.Split(out, "\n") {
		if t := wmctrlTitle(line); t != "" {
			titles = append(titles, t)
		}
	}
	return titles, nil
}

// wmctrlTitle pulls the window title out of one line of `wmctrl -l`.
//
// The columns are id, desktop, host, then the title, and they are separated by
// runs of spaces rather than single ones: wmctrl pads the desktop number to a
// fixed width. Splitting on a single space therefore produced an empty second
// field and left the host glued to the front of every title.
//
// Two things broke on that, both quietly. Every listing reported windows as
// "N/A Some Window" or "<hostname> Some Window", which puts a machine name into
// an answer nobody asked one about. And the focused marker never appeared at
// all, because the focused title comes from xdotool as the bare title and could
// never equal the padded one — so "* = focused" was printed above a list in
// which nothing was ever marked.
//
// The title is everything after the third field, because a window title can
// contain any number of spaces. Same reason system_status anchors on trailing
// columns: the variable-width thing has to be the remainder, never a field
// index.
func wmctrlTitle(line string) string {
	rest := strings.TrimSpace(line)
	for range 3 {
		i := strings.IndexAny(rest, " \t")
		if i < 0 {
			return ""
		}
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	return strings.TrimSpace(rest)
}

// registerDesktopInspect adds the native-window counterpart to browser_inspect.
//
// # Three answers, not two
//
// The tree exists, or the window is on screen and publishes nothing, or no such
// window is open. Collapsing the middle case into "no elements" is the failure
// this whole package keeps producing: a Tk window, an xterm and anything under
// Wine are all on screen, focusable and typeable, and completely invisible here.
// Reported as an empty result, she would describe an empty window.
//
// And the middle case has two causes that cannot be told apart from outside.
// Measured across four identical runs, which applications appear on the
// accessibility bus varied every time: GTK and Qt, then Qt alone, then GTK alone
// twice. An application that started before the accessibility service can miss
// its chance to register and never retry. So the message names both causes
// rather than asserting the one that sounds more likely.
func registerDesktopInspect(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_inspect",
			Description: "Read the elements of a native application window: its buttons, " +
				"fields, labels and what they are called. This is browser_inspect for " +
				"things outside the browser.\n\n" +
				"Use it before typing or pressing keys at a native app, so you are aiming " +
				"at something you have actually seen rather than guessing from a " +
				"screenshot. Not every application publishes this — when one does not, " +
				"say so and fall back to desktop_screenshot with keystrokes.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"window": {Type: "string", Description: "Part of the window title, as shown " +
					"by desktop_windows. Omit to read the first window that publishes anything."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if c := platform.Current().Accessibility; !c.Available && !strings.Contains(c.Why, "answering") {
				return "", fmt.Errorf("%s", c.Why)
			}
			reader, err := a11y.Open(ctx)
			if err != nil {
				return "", fmt.Errorf("%w — start it with at-spi-bus-launcher, or fall back "+
					"to desktop_screenshot and keystrokes", err)
			}

			title := argString(args, "window")
			node, err := reader.Window(ctx, title)
			if err == nil {
				body := a11y.Describe(node)
				if strings.TrimSpace(body) == "" {
					body = "(the window is on the bus and reports no elements inside it)"
				}
				// A partial tree read as a whole one is a false statement about the
				// window, and it is the tool's job to say which it handed over. A
				// window publishing nothing inside itself is the other half of the
				// same duty: it renders as one line and reads as an empty
				// application, which it almost never is.
				return body + emptyWindowNote(ctx, reader, node) +
					unreadNote(reader.Incomplete()), nil
			}

			// A window can also be missing because the list of applications was
			// itself read short, and every sentence below is a claim about what is
			// on the bus. The same parser reads the application list and the tree
			// inside a window, so when it stops early it takes both with it.
			if gap := reader.Incomplete(); gap != "" {
				return "", fmt.Errorf("%s was not found on the accessibility bus, but the bus "+
					"did not read fully (%s), so this is not an answer about that window. "+
					"Try again", quoteOrAny(title), gap)
			}

			// Not on the bus. Whether that window exists at all decides which of
			// two very different things to say — and whether that question could be
			// answered at all is a third thing, which must not be reported as a no.
			on, screenErr := onScreenTitles(ctx)
			if screenErr != nil {
				return "", fmt.Errorf("%s is not on the accessibility bus, and %s — so I "+
					"cannot tell you whether it is on screen at all. desktop_screenshot "+
					"will show you what is actually there",
					quoteOrAny(title), screenErr)
			}
			if matchesTitle(on, title) {
				return "", fmt.Errorf("that window is on screen but its application is not on " +
					"the accessibility bus, so there is nothing to read. Two things cause " +
					"this and they cannot be told apart from here: the toolkit may publish " +
					"nothing at all (Tk, xterm, Wine and some Java apps never do), or the " +
					"application may have started before the accessibility service and " +
					"missed its chance to register. Either way, desktop_screenshot with " +
					"keystrokes still works on it")
			}
			if len(on) == 0 {
				return "", fmt.Errorf("no window matching %q is open, and nothing else is "+
					"either", title)
			}
			return "", fmt.Errorf("no window matching %q is open. On screen right now: %s",
				title, strings.Join(on, ", "))
		},
	})
}

// matchesTitle reports whether any on-screen title contains the fragment. An
// empty fragment matches anything that exists, which is what omitting the
// argument means.
func matchesTitle(titles []string, fragment string) bool {
	want := strings.ToLower(strings.TrimSpace(fragment))
	for _, t := range titles {
		if want == "" || strings.Contains(strings.ToLower(t), want) {
			return true
		}
	}
	return false
}

// quoteOrAny renders a window fragment for a message, or says "any window" when
// none was given.
func quoteOrAny(title string) string {
	if strings.TrimSpace(title) == "" {
		return "no window"
	}
	return fmt.Sprintf("%q", title)
}
