package browser

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// The gestures a person makes that are not a plain left click.
//
// # What was missing, and how it was found
//
// Every click in this package hardcoded button:"left", clickCount:1, no
// modifiers. That is one gesture out of the handful a person uses without
// thinking, and the absence only became visible when a real task needed the
// others: forty rounds inside Google Drive with two files located by name and no
// way to say "download these". Right-click, because that is where Download
// lives. Ctrl+click, because "two pictures" means selecting two. Double-click,
// because that is how you open one.
//
// She improvised — clicked text that was not there, guessed a keyboard shortcut,
// pressed F10 — and every route was closed. Not because the page was hard, but
// because her hand only had one finger.
//
// # Why they share one implementation
//
// A gesture is a small number of parameters over the same input sequence: which
// button, how many clicks, which modifiers held. Written separately they drift —
// one grows the hover-first fix, another does not — and the bug then appears on
// some gestures and not others. So there is one place that dispatches, and the
// gestures are data.
//
// # On timing
//
// The pauses here are not decoration. A press and release in the same
// millisecond is not a click to a handler that measures duration; a double-click
// with no gap between the pairs is two single clicks; a menu that opens on hover
// needs the pointer to arrive before the button goes down. These delays are what
// make the gesture legible to ordinary web applications, which is a reliability
// property rather than a disguise — the same reason ClickReal exists at all.

// Button is which mouse button a gesture uses.
type Button string

const (
	Left   Button = "left"
	Right  Button = "right"
	Middle Button = "middle"
)

// Gesture describes one mouse action.
type Gesture struct {
	Button Button
	// Clicks is 1 for a click, 2 for a double-click.
	Clicks int
	// Modifiers are held for the duration: "ctrl", "shift", "alt", "meta". Ctrl
	// adds to a selection, shift extends it — which is how "these three files"
	// is expressed in every file manager ever written.
	Modifiers []string
}

// modifierMask turns names into the bitmask the input domain expects.
func modifierMask(names []string) int {
	mask := 0
	for _, n := range names {
		if bit, ok := modifierBits[strings.ToLower(strings.TrimSpace(n))]; ok {
			mask |= bit
		}
	}
	return mask
}

// buttonsMask is the "which buttons are currently down" field, distinct from
// which button caused this event. Drag handlers read it, and a move dispatched
// without it looks like the mouse is not being held.
func buttonsMask(b Button) int {
	switch b {
	case Right:
		return 2
	case Middle:
		return 4
	}
	return 1
}

// gestureAt performs a gesture at a viewport point.
func (c *Client) gestureAt(ctx context.Context, x, y float64, g Gesture) error {
	if g.Clicks < 1 {
		g.Clicks = 1
	}
	mods := modifierMask(g.Modifiers)

	// Arrive before acting. Hover handlers build menus, grids mark the row under
	// the pointer as the target, and a button that appears on hover does not
	// exist until the pointer is over it.
	if _, err := c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseMoved", "x": x, "y": y, "modifiers": mods,
	}); err != nil {
		return err
	}
	sleepCtx(ctx, 60*time.Millisecond)

	for click := 1; click <= g.Clicks; click++ {
		for _, phase := range []string{"mousePressed", "mouseReleased"} {
			if _, err := c.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
				"type": phase, "x": x, "y": y,
				"button": string(g.Button), "buttons": buttonsMask(g.Button),
				"clickCount": click, "modifiers": mods,
			}); err != nil {
				return err
			}
			if phase == "mousePressed" {
				// A held-then-released press. Zero-duration clicks are ignored by
				// handlers that measure the interval.
				sleepCtx(ctx, time.Duration(40+rand.Intn(35))*time.Millisecond)
			}
		}
		if click < g.Clicks {
			// Inside the double-click threshold, which is 500ms on every platform
			// that has one. Slower than that and it is two separate clicks.
			sleepCtx(ctx, 90*time.Millisecond)
		}
	}
	return nil
}

// GestureAtSelector performs a gesture on an element found by CSS selector.
func (c *Client) GestureAtSelector(ctx context.Context, selector string, g Gesture) error {
	p, err := c.locate(ctx, selector)
	if err != nil {
		return err
	}
	if err := c.gestureAt(ctx, p.X, p.Y, g); err != nil {
		return err
	}
	c.WaitStable(ctx, 4*time.Second)
	return nil
}

// GestureAtText performs a gesture on whatever reads a given text.
//
// The label it actually hit comes back, because fuzzy matching means the thing
// she named and the thing she got are not always the same, and a gesture that
// acted on the wrong row is worth knowing about before the next step assumes
// otherwise.
func (c *Client) GestureAtText(ctx context.Context, text string, g Gesture) (string, error) {
	x, y, label, err := c.PointForText(ctx, text)
	if err != nil {
		return "", err
	}
	if err := c.gestureAt(ctx, x, y, g); err != nil {
		return "", err
	}
	c.WaitStable(ctx, 4*time.Second)
	return label, nil
}

// Drag moves something from one place to another.
//
// # Why the intermediate moves matter
//
// A drag is not a press at one point and a release at another. Every drag
// implementation on the web — sortable lists, kanban boards, file drop zones,
// range sliders, Drive's move-to-folder — listens for the movement BETWEEN
// those two events, and decides what it is over as the pointer passes. Jumping
// straight from press to release produces a drag that started and ended with
// nothing in the middle, which every one of them reads as a click.
//
// So the pointer travels in steps, with the button held the whole way.
func (c *Client) Drag(ctx context.Context, fromSel, toSel string) error {
	from, err := c.locate(ctx, fromSel)
	if err != nil {
		return fmt.Errorf("drag source: %w", err)
	}
	to, err := c.locate(ctx, toSel)
	if err != nil {
		return fmt.Errorf("drag target: %w", err)
	}

	send := func(kind string, x, y float64, held bool) error {
		params := map[string]any{
			"type": kind, "x": x, "y": y,
			"button": "left", "clickCount": 1,
		}
		if held {
			params["buttons"] = 1
		}
		_, err := c.Call(ctx, "Input.dispatchMouseEvent", params)
		return err
	}

	if err := send("mouseMoved", from.X, from.Y, false); err != nil {
		return err
	}
	if err := send("mousePressed", from.X, from.Y, true); err != nil {
		return err
	}
	sleepCtx(ctx, 90*time.Millisecond)

	// Enough steps that a drop zone sees the pointer enter it, few enough that
	// the whole gesture stays under a second.
	const steps = 12
	for i := 1; i <= steps; i++ {
		f := float64(i) / steps
		x := from.X + (to.X-from.X)*f
		y := from.Y + (to.Y-from.Y)*f
		if err := send("mouseMoved", x, y, true); err != nil {
			return err
		}
		sleepCtx(ctx, 35*time.Millisecond)
	}

	// A pause on the target before release. Drop zones commonly validate on
	// dragover and only accept once they have seen a few.
	sleepCtx(ctx, 150*time.Millisecond)
	if err := send("mouseReleased", to.X, to.Y, false); err != nil {
		return err
	}
	c.WaitStable(ctx, 5*time.Second)
	return nil
}

// ScrollWithin scrolls inside a specific element rather than the page.
//
// Chat panes, code viewers, long dialogs and virtualised file grids all keep
// their own scroll container. Scrolling the window does nothing to them, so
// content below the fold inside one was simply unreachable — she would read the
// same twenty rows, conclude that was all there was, and act on a partial view.
func (c *Client) ScrollWithin(ctx context.Context, selector string, amount int) error {
	expr := deepPrelude + fmt.Sprintf(`(() => {
      const f = __find(%q);
      if (f.bad) return 'bad-selector';
      if (!f.el) return 'not-found';
      const before = f.el.scrollTop;
      f.el.scrollTop += %d;
      // A container that cannot scroll is worth reporting, because the caller
      // otherwise reads an unchanged view as "no more content".
      return f.el.scrollTop === before ? 'no-change' : 'ok';
    })()`, selector, amount)

	res, err := c.EvalString(ctx, expr)
	if err != nil {
		return err
	}
	switch res {
	case "not-found":
		return fmt.Errorf("no element matches %q", selector)
	case "bad-selector":
		return fmt.Errorf("%q is not a valid CSS selector", selector)
	case "no-change":
		return fmt.Errorf("%q did not scroll — it may already be at the end, or the "+
			"scrolling element may be a different one; inspect the page and try its parent", selector)
	}
	sleepCtx(ctx, 250*time.Millisecond)
	return nil
}
