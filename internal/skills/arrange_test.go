package skills

import (
	"strings"
	"testing"
)

// A named place turns into pixels once, here, rather than in a model doing
// arithmetic against a screen size it had to fetch.
//
// The halves have to tile exactly: left plus right covering the width with no
// gap and no overlap, or two windows laid side by side leave a stripe of desktop
// down the middle on odd-width screens.
func TestNamedPlacesTileTheWorkArea(t *testing.T) {
	// An odd width and height, because even numbers hide the rounding bug.
	s := rect{x: 0, y: 27, w: 1367, h: 741}

	left := place(t, s, "left")
	right := place(t, s, "right")
	if left.w+right.w != s.w {
		t.Errorf("left %d + right %d does not cover %d, so a stripe of desktop shows",
			left.w, right.w, s.w)
	}
	if right.x != s.x+left.w {
		t.Errorf("right starts at %d, want %d, so the halves overlap or gap",
			right.x, s.x+left.w)
	}

	top := place(t, s, "top")
	bottom := place(t, s, "bottom")
	if top.h+bottom.h != s.h {
		t.Errorf("top %d + bottom %d does not cover %d", top.h, bottom.h, s.h)
	}

	// Everything stays inside the work area, which is the screen minus panels.
	// Getting this wrong puts half of every window behind a taskbar.
	for _, where := range []string{"left", "right", "top", "bottom",
		"top-left", "top-right", "bottom-left", "bottom-right", "centre"} {
		r := place(t, s, where)
		if r.x < s.x || r.y < s.y || r.x+r.w > s.x+s.w || r.y+r.h > s.y+s.h {
			t.Errorf("%s lands at %d,%d %dx%d, outside the work area %d,%d %dx%d",
				where, r.x, r.y, r.w, r.h, s.x, s.y, s.w, s.h)
		}
	}

	// The four quarters must cover the whole area between them.
	var area int
	for _, where := range []string{"top-left", "top-right", "bottom-left", "bottom-right"} {
		r := place(t, s, where)
		area += r.w * r.h
	}
	if area != s.w*s.h {
		t.Errorf("the four quarters cover %d pixels of %d", area, s.w*s.h)
	}
}

// An unknown place is a refusal that lists the real ones, because a model that
// guessed "far-left" needs to know the vocabulary rather than a stack trace.
func TestAnUnknownPlaceListsTheRealOnes(t *testing.T) {
	_, err := placeIn(rect{0, 0, 1000, 800}, "far-left", nil)
	if err == nil {
		t.Fatal("an invented place was accepted")
	}
	for _, want := range []string{"left", "maximise", "top-left"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// Explicit pixels still work, for the cases a name cannot express.
func TestExplicitGeometryIsHonoured(t *testing.T) {
	got, err := placeIn(rect{0, 0, 1920, 1080}, "", map[string]any{
		"x": 100.0, "y": 50.0, "width": 640.0, "height": 480.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != (rect{100, 50, 640, 480}) {
		t.Errorf("explicit geometry came out as %+v", got)
	}
}

func place(t *testing.T, s rect, where string) rect {
	t.Helper()
	r, err := placeIn(s, where, nil)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	return r
}
