package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
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
		Handler: d.sendKeys,
	})
}

type desktop struct{ guard *guard.Guard }

// requireX11 gives a precise reason when the display is unavailable, rather
// than letting xdotool fail with something cryptic.
func requireX11() error {
	if os.Getenv("DISPLAY") == "" {
		return fmt.Errorf("no X display available (DISPLAY unset) — desktop control " +
			"needs a graphical session")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		return fmt.Errorf("this is a Wayland session; xdotool only works under X11")
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
		// wmctrl -l: <id> <desktop> <host> <title...>
		parts := strings.SplitN(strings.TrimSpace(line), " ", 4)
		if len(parts) < 4 {
			continue
		}
		title := strings.TrimSpace(parts[3])
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
	return fmt.Sprintf("Screenshot saved to %s (%.0f KB).", path, float64(info.Size())/1024), nil
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
