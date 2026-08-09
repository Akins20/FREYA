package wiring

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The real page, reduced to the shape that mattered.
//
// She built a flower shop site well — no cards, no borders, no emoji, a palette
// that suited it — with a nav offering Weddings and Contact, neither of which
// existed anywhere on the page or on disk. code_check passed all three files,
// because none of this is a syntax error.
func TestFindsTheNavThatPromisesPagesThatDoNotExist(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "index.html")
	write(t, page, `<!doctype html><html><head>
	  <link rel="stylesheet" href="style.css">
	</head><body>
	  <nav>
	    <a href="#bouquets">Bouquets</a>
	    <a href="#weddings">Weddings</a>
	    <a href="#contact">Contact</a>
	    <a href="about.html">About</a>
	    <a href="#">Order</a>
	  </nav>
	  <section id="bouquets">Seasonal bouquets, cut that morning.</section>
	</body></html>`)
	write(t, filepath.Join(dir, "style.css"), "body{margin:0}")

	got := strings.Join(Page(page), "\n")

	// Every promise the page makes and does not keep.
	for _, want := range []string{"#weddings", "#contact", "about.html", `href="#"`} {
		if !strings.Contains(got, want) {
			t.Errorf("did not report %s as a dead end:\n%s", want, got)
		}
	}
	// And nothing about the two that resolve, or this cries wolf and gets ignored.
	if strings.Contains(got, "#bouquets") {
		t.Errorf("reported #bouquets, which is right there on the page:\n%s", got)
	}
	if strings.Contains(got, "style.css") {
		t.Errorf("reported style.css, which is on disk beside it:\n%s", got)
	}
}

// A page where everything resolves must come back silent. A checker that always
// finds something teaches her to stop reading it.
func TestSaysNothingWhenEverythingResolves(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "index.html")
	write(t, page, `<!doctype html><html><body>
	  <a href="#top">Top</a>
	  <a href="https://example.com">Elsewhere</a>
	  <a href="mailto:someone@example.com">Mail</a>
	  <a href="second.html">Next</a>
	  <img src="logo.png" alt="">
	  <form action="/subscribe"><button>Join</button></form>
	  <div id="top"></div>
	</body></html>`)
	write(t, filepath.Join(dir, "second.html"), "<html></html>")
	write(t, filepath.Join(dir, "logo.png"), "\x89PNG")

	if got := Page(page); len(got) > 0 {
		t.Errorf("a fully wired page was reported as broken:\n%s", strings.Join(got, "\n"))
	}
}

// The button rule fires only when there is no script at all. A listener attached
// by selector is the normal way to wire a button, and reporting those would make
// every real page noisy.
func TestButtonsWithAScriptAreLeftAlone(t *testing.T) {
	dir := t.TempDir()

	bare := filepath.Join(dir, "bare.html")
	write(t, bare, `<html><body><button>Buy</button></body></html>`)
	if !strings.Contains(strings.Join(Page(bare), "\n"), "button") {
		t.Error("a button on a page with no script anywhere went unreported")
	}

	wired := filepath.Join(dir, "wired.html")
	write(t, wired, `<html><body><button id="b">Buy</button>
	  <script>document.getElementById('b').addEventListener('click', () => {});</script>
	</body></html>`)
	if got := Page(wired); len(got) > 0 {
		t.Errorf("a button wired by addEventListener was reported:\n%s", strings.Join(got, "\n"))
	}
}

// name= is as good as id= for jumping to, and a query or hash on a file
// reference is not part of the filename. Both would otherwise be false alarms.
func TestNamedAnchorsAndQueryStringsAreNotDeadEnds(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "index.html")
	write(t, page, `<html><body>
	  <a name="chapter1"></a>
	  <a href="#chapter1">Chapter 1</a>
	  <a href="notes.html?from=index#intro">Notes</a>
	</body></html>`)
	write(t, filepath.Join(dir, "notes.html"), "<html></html>")

	if got := Page(page); len(got) > 0 {
		t.Errorf("false alarms on a page that is fine:\n%s", strings.Join(got, "\n"))
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The note rides on file_write, because that is the call she actually makes.
// Given the rule and the tool, a fresh build ran project_new, file_write twice,
// code_check twice, serve and system_open — and never site_check.
func TestWritingAPageReportsItsOwnDeadEnds(t *testing.T) {
	note := Note("index.html", `<nav>
	  <a href="#services">Services</a>
	  <a href="#gallery">Gallery</a>
	  <a href="#">Book</a>
	</nav><section id="services">Wash and trim.</section>`)

	if note == "" {
		t.Fatal("a page with two dead links wrote silently")
	}
	for _, want := range []string{"#gallery", `href="#"`} {
		if !strings.Contains(note, want) {
			t.Errorf("note does not mention %s:\n%s", want, note)
		}
	}
	if strings.Contains(note, "#services") {
		t.Errorf("note flagged #services, which is on the page:\n%s", note)
	}
}

// The one thing that would make the note wrong more often than right: a page
// written before the stylesheet beside it. Cross-file checks belong to
// site_check, once the folder is finished.
func TestWriteNoteIgnoresFilesNotWrittenYet(t *testing.T) {
	if note := Note("index.html", `<html><head>
	  <link rel="stylesheet" href="style.css">
	</head><body><a href="about.html">About</a><script src="app.js"></script></body></html>`); note != "" {
		t.Errorf("flagged files that are simply written later:\n%s", note)
	}
	if note := Note("style.css", `a { color: red }`); note != "" {
		t.Errorf("fired on a stylesheet:\n%s", note)
	}
}

// The failure that motivated the network pass, reproduced against a local
// server so the test does not depend on Unsplash still serving anything.
//
// The pottery site passed every local check — four pages, fifty-seven links,
// none dead — and rendered with two blank gallery tiles, because two of its six
// background images were photo IDs she had invented. Well-formed URLs, in a
// stylesheet, referring to nothing.
func TestAnInventedImageURLIsADeadEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "real") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	write(t, filepath.Join(dir, "index.html"),
		`<html><head><link rel="stylesheet" href="style.css"></head>
		 <body><img src="`+srv.URL+`/real-hero.jpg"></body></html>`)
	write(t, filepath.Join(dir, "style.css"),
		`.tile-1 { background-image: url('`+srv.URL+`/real-mug.jpg'); }
		 .tile-2 { background-image: url("`+srv.URL+`/invented-bowl.jpg"); }`)

	broken, unknown := Remote(dir, 5*time.Second, 40)
	if unknown != 0 {
		t.Fatalf("precondition: the local server should answer everything, %d did not", unknown)
	}
	if len(broken) != 1 {
		t.Fatalf("want the one invented URL, got %d: %v", len(broken), broken)
	}
	if !strings.Contains(broken[0], "invented-bowl") || !strings.Contains(broken[0], "404") {
		t.Errorf("the report does not identify the dead image: %q", broken[0])
	}
	// The CSS is where it was found, and saying so is the difference between a
	// finding she can act on and one she has to go hunting for.
	if !strings.Contains(broken[0], "style.css") {
		t.Errorf("the report does not say which file wants it: %q", broken[0])
	}
	// And the two that answer are not mentioned at all.
	if strings.Contains(broken[0], "real-hero") || strings.Contains(broken[0], "real-mug") {
		t.Errorf("reported an image that loads fine: %q", broken[0])
	}
}

// A host that never answers is unknown, not broken. Accusing a page of a dead
// image because the network was slow is the false accusation this package spent
// the day learning not to make.
func TestAnUnreachableHostIsNotCalledBroken(t *testing.T) {
	dir := t.TempDir()
	// Reserved for documentation, so it never resolves to anything real.
	write(t, filepath.Join(dir, "index.html"),
		`<html><body><img src="https://example.invalid/photo.jpg"></body></html>`)

	broken, unknown := Remote(dir, 2*time.Second, 40)
	if len(broken) != 0 {
		t.Errorf("called an unreachable host a dead link: %v", broken)
	}
	if unknown != 1 {
		t.Errorf("want 1 unknown, got %d", unknown)
	}
}
