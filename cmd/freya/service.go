package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// systemd user service installation.
//
// A user unit rather than a system one: Freya runs as the person, needs their
// session bus for notifications and their X display for idle detection, and has
// no business running before anyone has logged in.
//
// # Why default.target and not graphical-session.target
//
// graphical-session.target is the obviously correct answer and it does not
// work. It is only reached if the desktop environment explicitly starts it for
// user units, and several — XFCE among them, which is what this machine runs —
// never do. The failure is silent and expensive: systemctl reports the unit
// enabled, and it then never starts, because nothing ever pulls in the target
// it is wanted by. default.target is reached on every login regardless of
// desktop, which is the actual requirement.
//
// # Why the environment file
//
// A user unit does not inherit the shell environment, so an API key exported in
// a profile is invisible here. Without one the daemon exits at startup while
// looking, from the outside, exactly like a unit that started and stopped for
// no reason. The file is optional — the leading dash — so a machine running the
// offline provider needs no key at all.

const unitTemplate = `[Unit]
Description=Freya — personal assistant background watcher
Documentation=file://%s/README.md
After=default.target

[Service]
Type=simple
ExecStart=%s -daemon
Restart=on-failure
RestartSec=30

# Set the microphone gain before listening. This machine's default capture gain
# is so high that the mic's own noise floor reaches full scale — a saturated
# signal that no software thresholding can rescue, because it has no silence left
# to detect, and which a transcriber turns into a stream of hallucinated words.
#
# The gain that matters is PulseAudio's source volume, not ALSA's: they share the
# hardware control, and PulseAudio continuously reasserts its own value over
# anything amixer or alsactl set. So it is set here through pactl, to the level
# (see FREYA_MIC_VOLUME in the env file) where speech clears the recorder's onset
# threshold and silence sits below it. The volume curve is steep near the bottom,
# so the right value is specific and worth pinning. Dash-prefixed and guarded, so
# a machine with no pactl or no configured level just skips it.
ExecStartPre=-/bin/sh -c 'test -n "$FREYA_MIC_VOLUME" && pactl set-source-volume @DEFAULT_SOURCE@ "$FREYA_MIC_VOLUME" || true'

# Credentials. Optional: without it the daemon falls back to whatever provider
# needs no key. Keep this file at mode 600; it holds API keys.
EnvironmentFile=-%%h/.config/freya/env

# The watchers read the display for idle detection and the session bus for
# notifications; without these the service starts and quietly does neither.
Environment=DISPLAY=%s
Environment=XDG_RUNTIME_DIR=/run/user/%%U

# Where transient files go: recordings, generated chimes. ProtectSystem=strict
# mounts the whole hierarchy read-only apart from /dev, /proc and /sys, so /tmp
# — the usual home for these — cannot be written. The runtime directory can, and
# is the right place for per-session throwaway audio anyway.
#
# This is pointedly NOT solved with PrivateTmp. A private /tmp is writable, but
# it also hides the real one, and the X server's socket lives at
# /tmp/.X11-unix — so isolating /tmp fixes recording and breaks the screen
# light, which is how those two chased each other for an evening. Redirecting
# TMPDIR keeps the real /tmp visible (a read-only /tmp is fine to *connect*
# through) while giving writes somewhere to land.
Environment=TMPDIR=/run/user/%%U

# It watches the machine and speaks. It has no reason to be able to change the
# system, so it is denied the ability.
#
# ProtectHome is off rather than read-only: memory, notes and the telemetry log
# all live under the data directory in home, and a daemon that cannot write them
# is a daemon that records nothing. "read-write" is not a value systemd accepts
# — it parses as an error and is ignored, which produced the right behaviour for
# the wrong reason and would have silently changed the day that changed.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=no

[Install]
WantedBy=default.target
`

// installService writes a systemd user unit and enables it.
func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the freya binary: %w", err)
	}
	exe, _ = filepath.Abs(exe)

	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemd is not available on this machine; " +
			"run `freya -daemon` from your session autostart instead")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("create unit directory: %w", err)
	}

	// The running session's display, rather than a hardcoded :0. Multi-monitor
	// and multi-seat setups land on :0.0 or :1, and a wrong value here costs
	// idle detection with no error to show for it.
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}

	// Where the docs live. Deriving it from the binary only works when running
	// out of the build tree; once installed to ~/.local/bin it points at
	// nothing. Fall back to the repo if the guess has no README beside it.
	projectDir := filepath.Dir(filepath.Dir(exe))
	if _, err := os.Stat(filepath.Join(projectDir, "README.md")); err != nil {
		if wd, err := os.Getwd(); err == nil {
			if _, err := os.Stat(filepath.Join(wd, "README.md")); err == nil {
				projectDir = wd
			}
		}
	}
	// Documentation= is a space-separated list of URLs, and this machine's repo
	// lives under a mount point with a space in its name, so the path has to be
	// percent-encoded or it parses as two invalid URLs.
	//
	// A percent is also how systemd introduces its own specifiers, so an encoded
	// space has to be written %%20 to survive that pass and arrive as %20. Any
	// literal percent already in the path is doubled first, for the same reason.
	docURL := strings.ReplaceAll(projectDir, "%", "%%")
	docURL = strings.ReplaceAll(docURL, " ", "%%20")

	unit := fmt.Sprintf(unitTemplate, docURL, exe, display)
	unitPath := filepath.Join(unitDir, "freya.service")

	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write unit: %w", err)
	}
	fmt.Printf("wrote %s\n", unitPath)

	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "freya.service"},
		{"--user", "restart", "freya.service"},
	} {
		out, err := exec.Command("systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s: %w: %s",
				strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}

	fmt.Println("enabled and started freya.service")
	fmt.Println()
	fmt.Println("  status:  systemctl --user status freya")
	fmt.Println("  logs:    journalctl --user -u freya -f")
	fmt.Println("  stop:    systemctl --user stop freya")
	fmt.Println()
	fmt.Println("It starts when you log in from now on.")
	fmt.Println()
	fmt.Println("To have it start at boot without logging in:")
	fmt.Println("  loginctl enable-linger $USER")
	fmt.Println("Note that the screen light and desktop notifications need a")
	fmt.Println("logged-in session, so they stay dormant until you sign in.")
	return nil
}
