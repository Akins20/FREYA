package skills

import (
	"strings"
	"testing"
)

// The geometry is injected because getting it slightly wrong fails silently: the
// deck still renders, it just has a white gutter down one side or slides split
// across two pages, and nobody sees that until they open it.
func TestTheSlideGeometryIsInjectedBeforeHerStyles(t *testing.T) {
	out := withSlideBase(`<html><head><style>.slide{background:navy}</style></head><body></body></html>`)

	base := strings.Index(out, "freya-slide-base")
	hers := strings.Index(out, "background:navy")
	if base < 0 {
		t.Fatal("the base geometry was not injected")
	}
	if !(base < hers) {
		t.Error("the base came after her styles, so it would override them — hers must win")
	}
	for _, want := range []string{"13.333in 7.5in", "margin: 0", "page-break-after: always"} {
		if !strings.Contains(out, want) {
			t.Errorf("the base is missing %q", want)
		}
	}
}

// A fragment with no head must still get the geometry, or a deck written as bare
// divs comes out as one long page.
func TestAFragmentStillGetsTheGeometry(t *testing.T) {
	for _, in := range []string{
		`<div class="slide">one</div>`,
		`<html><body><div class="slide">one</div></body></html>`,
	} {
		if !strings.Contains(withSlideBase(in), "13.333in") {
			t.Errorf("no geometry added to %q", in)
		}
	}
}

// The base must never be added twice — a second copy after her styles would
// silently undo her overrides.
func TestTheGeometryIsAddedOnce(t *testing.T) {
	out := withSlideBase(`<html><head></head><body></body></html>`)
	if n := strings.Count(out, "freya-slide-base"); n != 1 {
		t.Errorf("the base appears %d times, want 1", n)
	}
}
