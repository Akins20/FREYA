package skills

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/a11y"
	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/platform"
)

// RegisterSystem adds desktop and machine-control skills.
func RegisterSystem(r *Registry) {
	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "system_status",
			Description: "Report machine health: uptime, memory, disk usage and battery.",
			Params:      llm.ObjectSchema(nil),
		},
		Handler: systemStatus,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "what_can_i_do_here",
			Description: "Report what this machine actually lets you do: whether you can drive " +
				"windows, send keystrokes, take a screenshot, notify, record, speak, reach a " +
				"browser and read an application's own controls, and, where you cannot, " +
				"exactly why and what would fix it.\n\n" +
				"It also says which of the windows open right now can be read, which is a " +
				"different question from whether reading works at all: a Chromium " +
				"application (Electron, so VS Code, Slack, Discord, Teams) publishes nothing " +
				"inside itself unless it was started with a flag, so desktop_inspect shows " +
				"the window and nothing in it. Ask here before concluding an application is " +
				"empty.\n\n" +
				"Reach for this before telling the user something is impossible, and when a " +
				"desktop or voice tool refuses for a reason you do not understand. Saying " +
				"'I cannot do that' when the real answer is 'xdotool is not installed' " +
				"wastes their time and is not true.",
			Params: llm.ObjectSchema(nil),
		},
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			return platform.Current().Describe() + windowsIReadNow(ctx), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "system_volume",
			Description: "Get or set the system output volume. Omit level to read the current value.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"level": {Type: "number", Description: "Target volume 0-100. Omit to query."},
			}),
		},
		Handler: systemVolume,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "system_brightness",
			Description: "Get or set display brightness as a percentage.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"level": {Type: "number", Description: "Target brightness 1-100. Omit to query."},
			}),
		},
		Handler: systemBrightness,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "system_open",
			Description: "Launch an application, or open a file, URL or folder with the " +
				"default handler.\n\n" +
				"THIS IS HOW YOU SHOW THE USER SOMETHING. It opens on THEIR screen, in " +
				"their own applications. When they ask to see something you have made — " +
				"a page, a document, a folder of results — this is the tool, and there is " +
				"no other. Pasting the contents into your reply is not showing them; a " +
				"wall of markup is worse than useless to someone who asked to look at a " +
				"website.\n\n" +
				"browser_open is the opposite of this: it opens in YOUR automation " +
				"browser, which is a separate profile in a window they are not watching. " +
				"Use that to inspect your own work and compare it against the original. " +
				"Use this when the point is for them to see it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"app": {Type: "string", Description: "Application name, file path or URL."},
			}, "app"),
		},
		Handler: systemOpen,
	})
}

func systemStatus(ctx context.Context, _ map[string]any) (string, error) {
	var sb strings.Builder

	if out, err := run(ctx, 5*time.Second, "uptime", "-p"); err == nil {
		sb.WriteString("Uptime: " + out + "\n")
	}

	if out, err := run(ctx, 5*time.Second, "free", "-h"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "Mem:") {
				f := strings.Fields(line)
				if len(f) >= 7 {
					sb.WriteString(fmt.Sprintf("Memory: %s used of %s (%s available)\n", f[2], f[1], f[6]))
				}
			}
		}
	}

	// Report every real mount so a near-full drive is visible, not just root.
	if out, err := run(ctx, 5*time.Second, "df", "-h", "--output=target,size,used,avail,pcent",
		"-x", "tmpfs", "-x", "devtmpfs", "-x", "squashfs"); err == nil {
		lines := strings.Split(out, "\n")
		for i, line := range lines {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue
			}
			// Mount points may contain spaces ("/run/media/akins/Akins Drive1"),
			// so anchor on the four fixed trailing columns and treat everything
			// before them as the target.
			f := strings.Fields(line)
			if len(f) < 5 {
				continue
			}
			n := len(f)
			size, used, avail, pcent := f[n-4], f[n-3], f[n-2], f[n-1]
			target := strings.Join(f[:n-4], " ")
			sb.WriteString(fmt.Sprintf("Disk %s: %s used of %s, %s free (%s)\n",
				target, used, size, avail, pcent))
		}
	}

	if s := batteryStatus(); s != "" {
		sb.WriteString(s)
	}

	if sb.Len() == 0 {
		return "", fmt.Errorf("could not read system status")
	}
	return strings.TrimSpace(sb.String()), nil
}

// batteryStatus reads sysfs directly — no dependency on upower being installed.
func batteryStatus() string {
	matches, err := filepath.Glob("/sys/class/power_supply/BAT*")
	if err != nil || len(matches) == 0 {
		return ""
	}
	base := matches[0]
	capacity, err := os.ReadFile(filepath.Join(base, "capacity"))
	if err != nil {
		return ""
	}
	state, _ := os.ReadFile(filepath.Join(base, "status"))
	return fmt.Sprintf("Battery: %s%% (%s)\n",
		strings.TrimSpace(string(capacity)), strings.TrimSpace(string(state)))
}

func systemVolume(ctx context.Context, args map[string]any) (string, error) {
	if !have("pactl") {
		return "", fmt.Errorf("pactl is not installed; cannot control volume")
	}

	raw, hasLevel := args["level"]
	if !hasLevel || raw == nil {
		out, err := run(ctx, 5*time.Second, "pactl", "get-sink-volume", "@DEFAULT_SINK@")
		if err != nil {
			return "", err
		}
		return "Current volume: " + firstPercent(out), nil
	}

	level := argInt(args, "level", 50)
	level = clamp(level, 0, 100)
	if _, err := run(ctx, 5*time.Second, "pactl", "set-sink-volume",
		"@DEFAULT_SINK@", strconv.Itoa(level)+"%"); err != nil {
		return "", err
	}
	// Setting a level implies audible intent, so lift any existing mute.
	_, _ = run(ctx, 5*time.Second, "pactl", "set-sink-mute", "@DEFAULT_SINK@", "0")
	return fmt.Sprintf("Volume set to %d%%.", level), nil
}

func systemBrightness(ctx context.Context, args map[string]any) (string, error) {
	if have("brightnessctl") {
		if raw, ok := args["level"]; ok && raw != nil {
			level := clamp(argInt(args, "level", 50), 1, 100)
			if _, err := run(ctx, 5*time.Second, "brightnessctl",
				"set", strconv.Itoa(level)+"%"); err != nil {
				return "", err
			}
			return fmt.Sprintf("Brightness set to %d%%.", level), nil
		}
		out, err := run(ctx, 5*time.Second, "brightnessctl", "get")
		if err != nil {
			return "", err
		}
		return "Current brightness (raw): " + out, nil
	}

	// Fall back to sysfs, which needs no extra package but may need permissions.
	devices, _ := filepath.Glob("/sys/class/backlight/*")
	if len(devices) == 0 {
		return "", fmt.Errorf("no backlight device found; install brightnessctl for display control")
	}
	dev := devices[0]
	maxRaw, err := os.ReadFile(filepath.Join(dev, "max_brightness"))
	if err != nil {
		return "", fmt.Errorf("read max_brightness: %w", err)
	}
	max, _ := strconv.Atoi(strings.TrimSpace(string(maxRaw)))

	if raw, ok := args["level"]; ok && raw != nil && max > 0 {
		level := clamp(argInt(args, "level", 50), 1, 100)
		target := max * level / 100
		path := filepath.Join(dev, "brightness")
		if err := os.WriteFile(path, []byte(strconv.Itoa(target)), 0o644); err != nil {
			return "", fmt.Errorf("writing %s needs permission; install brightnessctl instead: %w", path, err)
		}
		return fmt.Sprintf("Brightness set to %d%%.", level), nil
	}

	curRaw, _ := os.ReadFile(filepath.Join(dev, "brightness"))
	cur, _ := strconv.Atoi(strings.TrimSpace(string(curRaw)))
	if max == 0 {
		return "", fmt.Errorf("backlight reports zero maximum")
	}
	return fmt.Sprintf("Current brightness: %d%%", cur*100/max), nil
}

func systemOpen(ctx context.Context, args map[string]any) (string, error) {
	target := argString(args, "app")
	if target == "" {
		return "", fmt.Errorf("nothing specified to open")
	}

	// A bare application name is launched directly; anything else is handed to
	// xdg-open so files, folders and URLs all route to their default handler.
	looksLikePath := strings.ContainsAny(target, "/.:") || strings.Contains(target, "://")
	if !looksLikePath && have(target) {
		// Deliberately not ctx-bound: a launched GUI app should outlive the turn.
		cmd := exec.Command(target)
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("launch %s: %w", target, err)
		}
		// Reap the child in the background so it does not become a zombie.
		go func() { _ = cmd.Wait() }()
		return "Launched " + target + ".", nil
	}

	if !have("xdg-open") {
		return "", fmt.Errorf("xdg-open is not installed and %q is not an executable on PATH", target)
	}
	// The same reasoning as the branch above, which was missing here.
	//
	// xdg-open hands the file to the browser and exits almost immediately — but
	// run() collects output with CombinedOutput, and that waits for every writer
	// of the pipes to close. The browser inherits them, so it waits for the
	// BROWSER to exit. Asked to show him the site he had just had built, she
	// called this and the turn simply stopped: no result, no error, nothing on
	// screen, until something eventually killed it.
	//
	// Detaching the streams rather than firing blind keeps the exit status
	// meaningful — with nothing inheriting a pipe, Run returns when xdg-open
	// returns, which is the thing we actually want to know about.
	launch, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(launch, "xdg-open", target)
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("open %s: %w", target, err)
	}
	return "Opened " + target + " — it is on the user's screen now, in their own " +
		"browser or application, not in yours.", nil
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func firstPercent(s string) string {
	for _, f := range strings.Fields(s) {
		if strings.HasSuffix(f, "%") {
			return f
		}
	}
	return strings.TrimSpace(s)
}

// maxWindowsProbed bounds the live desktop scan. A desktop with more open
// windows than this is real, and reading every one of them costs a gdbus call
// per node; the cap is said out loud rather than silently applied.
const maxWindowsProbed = 12

// windowsIReadNow says which windows can actually be read at this moment.
//
// # Why the capability report was only half an answer
//
// Everything above it is a fact about the machine — xdotool is installed, a
// registry is answering — and those stay true all day. Whether the window in
// front of her can be read is a fact about right now, and the two come apart
// badly. A Chromium application publishes nothing inside itself unless it was
// started with --force-renderer-accessibility, so "accessibility: yes" and "I
// can read that window" are different claims, and the only way to tell them
// apart was to try and be told the window was empty.
//
// That is most of a modern desktop: VS Code, Slack, Discord, Teams. This is the
// tool she is told to reach for before saying something is impossible, so it is
// the right place to answer it.
//
// # Four answers, not two
//
// A window is readable, or withholding because it is Chromium, or on the bus
// with nothing named in it, or not on the accessibility bus at all — which is
// Tk, xterm, Wine and some Java applications, and is not a failure so much as a
// toolkit that never published anything. Each one leads somewhere different,
// and collapsing them loses exactly the part she needed.
func windowsIReadNow(ctx context.Context) string {
	if !platform.Current().Accessibility.Available {
		return ""
	}
	reader, err := a11y.Open(ctx)
	if err != nil {
		return ""
	}
	apps, err := reader.Applications(ctx)
	if err != nil {
		return ""
	}

	var readable, withheld, nameless []string
	onBus := map[string]bool{}
	capped := 0
	for _, app := range apps {
		for _, w := range app.Children {
			name := strings.TrimSpace(w.Name)
			if name == "" {
				continue
			}
			onBus[name] = true
			if len(readable)+len(withheld)+len(nameless) >= maxWindowsProbed {
				capped++
				continue
			}
			// Three levels, the same bounded read a menu uses. One is not enough
			// on GTK, where the first level below a window is an unnamed
			// container and a window full of controls would come back looking
			// like a window with nothing in it.
			reader.Refresh(ctx, w)
			switch {
			case a11y.Named(w):
				readable = append(readable, name)
			case a11y.ChromiumLike(reader.Actions(ctx, w)):
				withheld = append(withheld, name)
			default:
				nameless = append(nameless, name)
			}
		}
	}

	// A window on screen and not on the bus is the fourth answer, and it can
	// only be seen by asking the window manager as well.
	var offBus []string
	if titles, err := onScreenTitles(ctx); err == nil {
		for _, t := range titles {
			if !onBus[t] {
				offBus = append(offBus, t)
			}
		}
	}
	return desktopSection(readable, withheld, nameless, offBus, capped)
}

// desktopSection renders the live half of the report, and is empty when there
// is nothing on the bus to say anything about.
func desktopSection(readable, withheld, nameless, offBus []string, capped int) string {
	if len(readable)+len(withheld)+len(nameless)+len(offBus) == 0 && capped == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nWindows right now:\n")
	if len(readable) > 0 {
		fmt.Fprintf(&b, "  readable: %s\n", strings.Join(readable, ", "))
	}
	if len(withheld) > 0 {
		fmt.Fprintf(&b, "  withholding: %s — Chromium applications (Electron: VS Code, "+
			"Slack, Discord, Teams). They publish nothing inside themselves unless started "+
			"with --force-renderer-accessibility, so desktop_inspect will show the window "+
			"and nothing in it. Restart them with that flag to read them, or use "+
			"desktop_screenshot with desktop_key, which reach them as they are.\n",
			strings.Join(withheld, ", "))
	}
	if len(nameless) > 0 {
		fmt.Fprintf(&b, "  on the bus with nothing named in them: %s — there is nothing to "+
			"aim desktop_click at. Screenshot and keystrokes still work.\n",
			strings.Join(nameless, ", "))
	}
	if len(offBus) > 0 {
		fmt.Fprintf(&b, "  not on the accessibility bus at all: %s — normal for Tk, xterm, "+
			"Wine and some Java applications, which never publish a tree. Screenshot and "+
			"keystrokes are the whole toolkit there.\n", strings.Join(offBus, ", "))
	}
	if capped > 0 {
		fmt.Fprintf(&b, "  [%d more window(s) were not examined, to bound the cost of asking.]\n",
			capped)
	}
	return strings.TrimRight(b.String(), "\n")
}
