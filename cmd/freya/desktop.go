package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// A launcher, so the window opens from the desktop rather than a terminal.
//
// # Why this is worth code rather than a line in the README
//
// `freya -gui` is the whole command, and typing it means opening a terminal
// first — which is the one thing a window is for not doing. A desktop entry is
// how every other application on the machine is started, and she should be no
// different.
//
// # Why it points at the installed binary and not at $PATH
//
// A .desktop Exec= is run by the desktop environment, whose PATH is whatever the
// session started with — not the shell's. ~/.local/bin is on a login shell's
// PATH on most distributions and is absent from the session's on some, and the
// failure mode is a menu entry that does nothing at all with no error anywhere.
// So the absolute path is written in.

//go:embed freya.svg
var iconSVG []byte

// desktopEntry is the launcher. NoDisplay stays false: the point is that it
// appears in the menu.
//
// StartupWMClass matters more than it looks. Her window is a Chrome --app frame,
// so without it the taskbar shows a generic Chrome button that does not group
// with this launcher, and a person who clicks the icon twice gets two windows
// and no clue which is which.
const desktopEntry = `[Desktop Entry]
Type=Application
Version=1.0
Name=Freya
GenericName=Personal assistant
Comment=Open Freya's window
Exec=%s -gui
Icon=freya
Terminal=false
Categories=Utility;
Keywords=assistant;ai;freya;voice;
StartupNotify=true
StartupWMClass=%s
`

// installDesktopEntry writes the launcher and its icon, and refreshes the caches
// that decide whether either is seen.
//
// Errors here are returned rather than swallowed, but the caller treats them as
// a warning: a machine with no desktop environment is a machine where the daemon
// still works perfectly and a missing menu entry is not a failed install.
func installDesktopEntry(exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	iconDir := filepath.Join(home, ".local", "share", "icons", "hicolor", "scalable", "apps")
	if err := os.MkdirAll(iconDir, 0o755); err != nil {
		return fmt.Errorf("create icon directory: %w", err)
	}
	iconPath := filepath.Join(iconDir, "freya.svg")
	if err := os.WriteFile(iconPath, iconSVG, 0o644); err != nil {
		return fmt.Errorf("write icon: %w", err)
	}

	appDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return fmt.Errorf("create applications directory: %w", err)
	}
	entryPath := filepath.Join(appDir, "freya.desktop")
	entry := fmt.Sprintf(desktopEntry, exe, windowWMClass)
	if err := os.WriteFile(entryPath, []byte(entry), 0o644); err != nil {
		return fmt.Errorf("write desktop entry: %w", err)
	}

	// Both caches are advisory: the entry works without them on most desktops and
	// takes until the next login to appear on some. Neither is worth failing for.
	for _, c := range [][]string{
		{"update-desktop-database", appDir},
		{"gtk-update-icon-cache", "-f", "-t", filepath.Join(home, ".local", "share", "icons", "hicolor")},
	} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		_ = exec.Command(c[0], c[1:]...).Run()
	}

	fmt.Printf("wrote %s\n", entryPath)
	fmt.Printf("wrote %s\n", iconPath)
	return nil
}

// windowWMClass is what Chrome names her window.
//
// Measured, not guessed. The obvious assumption is that an --app frame takes its
// class from the profile directory; it does not. Chrome derives it from the HOST
// of the app URL, so `xprop` on a live window reports:
//
//	WM_CLASS(STRING) = "127.0.0.1", "Google-chrome"
//
// The instance is the one to match on. Matching the class instead would group
// her window with every other Chrome window on the machine, which is the
// opposite of what StartupWMClass is for.
//
// It is stable because the address always is: the window is served on loopback
// by whoever owns the archive, and only the port moves.
const windowWMClass = "127.0.0.1"
