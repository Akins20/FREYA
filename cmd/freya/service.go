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
// session bus for notifications and their X display for screenshots, and has no
// business running before anyone has logged in.

const unitTemplate = `[Unit]
Description=Freya — personal assistant background watcher
Documentation=file://%s/README.md
After=graphical-session.target
PartOf=graphical-session.target

[Service]
Type=simple
ExecStart=%s -daemon
Restart=on-failure
RestartSec=30

# The watchers read the display for idle detection and the session bus for
# notifications; without these the service starts and quietly does neither.
Environment=DISPLAY=:0
Environment=XDG_RUNTIME_DIR=/run/user/%%U

# It watches the machine and speaks. It has no reason to be able to change the
# system, so it is denied the ability.
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-write
PrivateTmp=false

[Install]
WantedBy=graphical-session.target
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

	projectDir := filepath.Dir(filepath.Dir(exe)) // .../bin/freya -> project root
	unit := fmt.Sprintf(unitTemplate, projectDir, exe)
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
	fmt.Println("It starts with your graphical session from now on.")
	return nil
}
