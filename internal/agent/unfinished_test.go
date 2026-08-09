package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The gate reads the file, not the tool's opinion of the file.
//
// This is the bug that shipped for one run: she wrote index.html with an
// href="#", was told, and repaired it with file_edit. The first version worked
// out "still broken" by matching the write tool's wording in the trail, saw no
// second file_write, and ended the exchange with "[Not finished: href=\"#\" —
// goes nowhere. It was flagged twice and left as it is]" against a file that was
// clean on disk. Accusing her of leaving work undone when she has just done it
// is worse than not checking at all.
func TestAPageFixedByAnyToolIsNotStillOpen(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "site", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}

	broken := `<body><a href="#" class="logo">Crumb &amp; Crust</a></body>`
	if err := os.WriteFile(page, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &trail{}
	w.add(step{tool: "file_write", round: 1,
		output: "Created " + page + " (7200 bytes).\n\n[This page makes promises it does not keep: " +
			`href="#" — goes nowhere.]`})

	ends := stillOpen(w)
	if len(ends) != 1 {
		t.Fatalf("precondition: the broken page should be open, got %v", ends)
	}
	if !strings.Contains(ends[0], "site/index.html") {
		t.Errorf("open end does not name the page: %q", ends[0])
	}

	// She repairs it with file_edit, which reports only a base name.
	fixed := `<body id="top"><a href="#top" class="logo">Crumb &amp; Crust</a></body>`
	if err := os.WriteFile(page, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	w.add(step{tool: "file_edit", round: 12, output: "Edited index.html: 1 replacement(s), +12 bytes."})

	if ends := stillOpen(w); len(ends) != 0 {
		t.Errorf("a page she repaired is still reported as unfinished: %v\n"+
			"The verdict has to come from the file, not from what a tool said earlier.", ends)
	}
}

// A page that is genuinely still broken at the end must still be caught, or the
// fix above has simply turned the gate off.
func TestAPageLeftBrokenIsStillCaught(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "shop", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte(`<nav><a href="#gallery">Gallery</a></nav>`), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &trail{}
	w.add(step{tool: "file_write", output: "Created " + page + " (40 bytes)."})
	w.add(step{tool: "file_edit", output: "Edited style.css: 1 replacement(s), +4 bytes."})

	ends := stillOpen(w)
	if len(ends) != 1 {
		t.Fatalf("want the dead anchor caught, got %v", ends)
	}
	if !strings.Contains(ends[0], "#gallery") {
		t.Errorf("the end does not name the anchor: %q", ends[0])
	}
	if brief := finishBrief(ends); !strings.Contains(brief, "#gallery") ||
		!strings.Contains(brief, "site_check") {
		t.Errorf("the push does not say what to fix or how to verify:\n%s", brief)
	}
}

// A failed write never happened, and a file deleted since cannot be judged.
// Both would be false accusations.
func TestNothingIsClaimedAboutFilesThatAreNotThere(t *testing.T) {
	w := &trail{}
	w.add(step{tool: "file_write", failed: true, output: "Created /nope/index.html (10 bytes)."})
	w.add(step{tool: "file_write", output: "Created /also-nope/index.html (10 bytes)."})
	if ends := stillOpen(w); len(ends) != 0 {
		t.Errorf("claimed dead links in files that cannot be read: %v", ends)
	}
}

// Only pages. A stylesheet or a script is not a set of promises to click.
func TestOnlyPagesAreJudged(t *testing.T) {
	dir := t.TempDir()
	css := filepath.Join(dir, "p", "style.css")
	if err := os.MkdirAll(filepath.Dir(css), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(css, []byte(`a { color: red }`), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &trail{}
	w.add(step{tool: "file_write", output: "Created " + css + " (16 bytes)."})
	if ends := stillOpen(w); len(ends) != 0 {
		t.Errorf("judged a stylesheet: %v", ends)
	}
}
