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

	"github.com/akins/jarvis/internal/llm"
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
			Name:        "system_open",
			Description: "Launch an application or open a file, URL or folder with the default handler.",
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
	if _, err := run(ctx, 10*time.Second, "xdg-open", target); err != nil {
		return "", err
	}
	return "Opened " + target + ".", nil
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
