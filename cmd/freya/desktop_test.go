package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The launcher lands where the desktop looks for it, with an icon beside it.
func TestTheLauncherIsWrittenWhereTheMenuReadsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := installDesktopEntry("/home/someone/.local/bin/freya"); err != nil {
		t.Fatal(err)
	}

	entry := filepath.Join(home, ".local", "share", "applications", "freya.desktop")
	icon := filepath.Join(home, ".local", "share", "icons", "hicolor", "scalable", "apps", "freya.svg")
	for _, p := range []string{entry, icon} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s was not written: %v", p, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", p)
		}
	}
}

// A .desktop Exec is run by the desktop environment, whose PATH is the session's
// and not a login shell's. ~/.local/bin is missing from it on some
// distributions, and the failure is a menu entry that does nothing at all with
// no error anywhere — so the absolute path is written in.
func TestTheLauncherDoesNotRelyOnPATH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const exe = "/home/someone/.local/bin/freya"

	if err := installDesktopEntry(exe); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".local", "share", "applications", "freya.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)

	if !strings.Contains(text, "Exec="+exe+" -gui") {
		t.Errorf("Exec does not name the binary by absolute path:\n%s", text)
	}
	// Every key the spec requires, plus the ones that decide whether it shows up.
	for _, want := range []string{
		"[Desktop Entry]", "Type=Application", "Name=Freya", "Icon=freya",
		"Terminal=false",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the entry is missing %q:\n%s", want, text)
		}
	}
	// Two main categories and the menu lists her twice; desktop-file-validate
	// says so as a hint, which is easy to miss.
	cats := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Categories=") {
			cats = strings.TrimPrefix(line, "Categories=")
		}
	}
	main := map[string]bool{"AudioVideo": true, "Audio": true, "Video": true,
		"Development": true, "Education": true, "Game": true, "Graphics": true,
		"Network": true, "Office": true, "Science": true, "Settings": true,
		"System": true, "Utility": true}
	n := 0
	for _, c := range strings.Split(cats, ";") {
		if main[c] {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Categories=%q names %d main categories; one appears once in the "+
			"menu and two appear twice", cats, n)
	}
}

// StartupWMClass has to be what Chrome ACTUALLY calls the window, which is not
// what it looks like it should be.
//
// The obvious assumption is that an --app frame takes its class from the profile
// directory. It does not: Chrome derives it from the host of the app URL, and
// xprop on a live window reports WM_CLASS = "127.0.0.1", "Google-chrome". The
// instance is the one to match — matching the class would group her window with
// every other Chrome window on the machine, which is the opposite of the point.
func TestTheLauncherClaimsTheWindowItActuallyOpens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := installDesktopEntry("/bin/freya"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".local", "share", "applications", "freya.desktop"))
	text := string(b)

	if !strings.Contains(text, "StartupWMClass=127.0.0.1") {
		t.Errorf("StartupWMClass is not the instance name Chrome gives the window:\n%s", text)
	}
	if strings.Contains(text, "StartupWMClass=Google-chrome") {
		t.Error("StartupWMClass matches the Chrome class, so her window would group " +
			"with every other Chrome window")
	}
}

// The icon has to be a real drawing with its own colours. The window's copy
// strokes with currentColor, which resolves to nothing outside a page.
func TestTheIconStandsOnItsOwn(t *testing.T) {
	// Comments stripped first. The file's own comment explains that the window's
	// copy of this mark strokes with currentColor, and a naive search found that
	// sentence and failed on it — the same trap as app.css, where a comment
	// mentioning a deleted class kept it looking alive.
	svg := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(string(iconSVG), " ")
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "xmlns=") {
		t.Fatal("the icon is not a standalone SVG")
	}
	if strings.Contains(svg, "currentColor") {
		t.Error("the icon strokes with currentColor, which has nothing to inherit " +
			"from outside a web page — it would draw as black or not at all")
	}
	if !strings.Contains(svg, "#f0a437") {
		t.Error("the icon is not drawn in her amber")
	}
}
