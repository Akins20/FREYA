package skills

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

// Putting windows where the user wants them.
//
// # What was missing
//
// She could list windows, focus one and photograph it, and could not move one.
// So "put the docs on the left and the editor on the right" was a request she
// understood and could not act on, which is a strange gap in something that
// otherwise drives the machine.
//
// # Why named places rather than coordinates
//
// The obvious signature is x, y, width, height, and it is the wrong one to lead
// with. Nobody asks for a window at 640,0. They ask for the left half, or the
// other screen, or maximised, and a model handed pixels has to fetch the screen
// size, do arithmetic, and get the arithmetic wrong at the edges: off by the
// panel height, off by a window border, or laid out for the wrong monitor.
//
// So the vocabulary is the one people use, the arithmetic happens here once, and
// explicit geometry stays available for the cases a name cannot express.
//
// # It reports where the window actually ended up
//
// A window manager is free to ignore this. Tiling managers override placement
// entirely, a maximised window will not move until it is unmaximised, and size
// hints mean a terminal snaps to whole character cells and lands a few pixels
// off what was asked. All three look identical to a caller that assumes success.
// So the geometry is read back afterwards and the answer says where the window
// is, not where it was sent.
func RegisterArrange(r *Registry, g *guard.Guard) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "desktop_arrange",
			Description: "Move or resize a window: left or right half, top or bottom half, " +
				"a quarter, maximised, centred, or exact pixels.\n\n" +
				"This is how you lay out a screen for someone: 'docs on the left, editor on " +
				"the right' is two calls. Use desktop_windows first to see the titles.\n\n" +
				"It reports where the window actually ended up rather than where it was " +
				"sent, because a window manager is free to ignore this: a maximised window " +
				"will not move until it is restored, a tiling manager overrides placement " +
				"entirely, and a terminal snaps to whole character cells.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"window": {Type: "string", Description: "Part of the window title, as " +
					"desktop_windows showed it."},
				"where": {Type: "string", Description: "left, right, top, bottom, " +
					"top-left, top-right, bottom-left, bottom-right, maximise, restore, " +
					"centre, or full. Omit when giving exact pixels."},
				"x":      {Type: "number", Description: "Exact left edge, with y, width and height."},
				"y":      {Type: "number", Description: "Exact top edge."},
				"width":  {Type: "number", Description: "Exact width in pixels."},
				"height": {Type: "number", Description: "Exact height in pixels."},
			}, "window"),
		},
		Mutates: true,
		Serial:  true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if err := requireX11(); err != nil {
				return "", err
			}
			if !have("xdotool") {
				return "", fmt.Errorf("moving windows needs xdotool on PATH and it is not installed")
			}
			title := strings.TrimSpace(argString(args, "window"))
			if title == "" {
				return "", fmt.Errorf("which window? desktop_windows lists what is open")
			}

			id, matched, err := windowByTitle(ctx, title)
			if err != nil {
				return "", err
			}

			where := strings.ToLower(strings.TrimSpace(argString(args, "where")))
			explicit := args["x"] != nil || args["width"] != nil
			if where == "" && !explicit {
				return "", fmt.Errorf("say where: left, right, top, bottom, a quarter like " +
					"top-left, maximise, restore, centre, full, or exact x, y, width and height")
			}

			action := guard.Action{
				Kind:    guard.KindSystem,
				Command: "xdotool windowmove/windowsize",
				Args:    []string{matched, where},
				Reason:  fmt.Sprintf("move %q to %s", matched, describeTarget(where, args)),
			}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				before := windowGeometry(ctx, id)

				switch where {
				case "maximise", "maximize", "full":
					// Through the window manager, because a maximised state is
					// something it owns; setting the geometry by hand leaves a
					// window that looks maximised and is not.
					if err := wmState(ctx, matched, "add", "maximized_vert,maximized_horz"); err != nil {
						return "", err
					}
				case "restore":
					if err := wmState(ctx, matched, "remove", "maximized_vert,maximized_horz"); err != nil {
						return "", err
					}
				default:
					// Anything that sets geometry has to leave the maximised state
					// first, or the window manager keeps the old size and the move
					// silently does nothing.
					_ = wmState(ctx, matched, "remove", "maximized_vert,maximized_horz")
					rect, err := targetRect(ctx, where, args)
					if err != nil {
						return "", err
					}
					if _, err := run(ctx, 5*time.Second, "xdotool", "windowsize", id,
						fmt.Sprint(rect.w), fmt.Sprint(rect.h)); err != nil {
						return "", err
					}
					if _, err := run(ctx, 5*time.Second, "xdotool", "windowmove", id,
						fmt.Sprint(rect.x), fmt.Sprint(rect.y)); err != nil {
						return "", err
					}
				}

				// Window managers animate and apply size hints, so the geometry is
				// not final the instant the call returns.
				time.Sleep(350 * time.Millisecond)
				after := windowGeometry(ctx, id)

				if after == "" {
					return fmt.Sprintf("Moved %q, and its geometry could not be read back, so "+
						"where it ended up is unknown.", matched), nil
				}
				if after == before {
					return fmt.Sprintf("%q is still at %s, unchanged. The window manager "+
						"refused the move: a tiling manager overrides placement, and a "+
						"maximised or fullscreen window will not move until it is restored.",
						matched, after), nil
				}
				return fmt.Sprintf("%q is now at %s (it was at %s).", matched, after, before), nil
			})
		},
	})
}

// rect is a target in pixels.
type rect struct{ x, y, w, h int }

// windowByTitle finds one window id by title substring, and refuses an ambiguous
// match rather than picking one.
//
// Refusing matters here more than in a read: moving the wrong window rearranges
// something the user was looking at, and "Untitled" matches three editors.
func windowByTitle(ctx context.Context, title string) (id, matched string, err error) {
	if !have("wmctrl") {
		return "", "", fmt.Errorf("wmctrl is not installed, so what is open is unknown")
	}
	out, err := run(ctx, 10*time.Second, "wmctrl", "-l")
	if err != nil {
		return "", "", fmt.Errorf("the window manager could not be asked what is open: %w", err)
	}
	want := strings.ToLower(title)
	var ids, titles []string
	for _, line := range strings.Split(out, "\n") {
		t := wmctrlTitle(line)
		if t == "" || !strings.Contains(strings.ToLower(t), want) {
			continue
		}
		if f := strings.Fields(strings.TrimSpace(line)); len(f) > 0 {
			ids = append(ids, f[0])
			titles = append(titles, t)
		}
	}
	switch len(ids) {
	case 0:
		var open []string
		for _, line := range strings.Split(out, "\n") {
			if t := wmctrlTitle(line); t != "" {
				open = append(open, t)
			}
		}
		if len(open) == 0 {
			return "", "", fmt.Errorf("no window matching %q is open, and nothing else is either", title)
		}
		return "", "", fmt.Errorf("no window matching %q. On screen right now: %s",
			title, strings.Join(open, ", "))
	case 1:
		return ids[0], titles[0], nil
	default:
		return "", "", fmt.Errorf("%q matches %d windows (%s), and moving the wrong one "+
			"rearranges what they were looking at. Name it more precisely",
			title, len(ids), strings.Join(titles, ", "))
	}
}

// screen returns the usable desktop area, which is not the same as the screen.
//
// wmctrl reports a work area that already excludes panels and docks. Using the
// raw screen size instead puts the bottom half of every window behind a taskbar,
// which is the sort of detail that makes an assistant feel broken rather than
// wrong.
func screen(ctx context.Context) (rect, error) {
	if out, err := run(ctx, 5*time.Second, "wmctrl", "-d"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			// wmctrl -d: ... WA: <x>,<y> <w>x<h> ...
			i := strings.Index(line, "WA:")
			if i < 0 {
				continue
			}
			f := strings.Fields(line[i+3:])
			if len(f) < 2 {
				continue
			}
			var x, y, w, h int
			if _, err := fmt.Sscanf(f[0], "%d,%d", &x, &y); err != nil {
				continue
			}
			if _, err := fmt.Sscanf(f[1], "%dx%d", &w, &h); err != nil {
				continue
			}
			if w > 0 && h > 0 {
				return rect{x, y, w, h}, nil
			}
		}
	}
	// No work area published, so fall back to the whole screen and accept that a
	// panel may overlap.
	out, err := run(ctx, 5*time.Second, "xdotool", "getdisplaygeometry")
	if err != nil {
		return rect{}, fmt.Errorf("the screen size could not be read, so there is nothing "+
			"to lay a window out against: %w", err)
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d %d", &w, &h); err != nil || w == 0 {
		return rect{}, fmt.Errorf("the screen size came back as %q, which is not a size", out)
	}
	return rect{0, 0, w, h}, nil
}

// targetRect turns a named place into pixels, against this machine's work area.
func targetRect(ctx context.Context, where string, args map[string]any) (rect, error) {
	s, err := screen(ctx)
	if err != nil {
		return rect{}, err
	}
	return placeIn(s, where, args)
}

// placeIn is the arithmetic, separated from asking the machine how big the
// screen is.
//
// A seam rather than one function, because the arithmetic is where the bugs are
// and the screen size is where the dependency is. The halves have to tile
// exactly: on an odd-width screen a naive w/2 twice leaves a one-pixel stripe of
// desktop down the middle, which is the sort of thing nobody notices in review
// and everybody notices on screen.
func placeIn(s rect, where string, args map[string]any) (rect, error) {
	if where == "" {
		return rect{
			x: argInt(args, "x", s.x),
			y: argInt(args, "y", s.y),
			w: argInt(args, "width", s.w/2),
			h: argInt(args, "height", s.h),
		}, nil
	}

	halfW, halfH := s.w/2, s.h/2
	switch where {
	case "left":
		return rect{s.x, s.y, halfW, s.h}, nil
	case "right":
		return rect{s.x + halfW, s.y, s.w - halfW, s.h}, nil
	case "top":
		return rect{s.x, s.y, s.w, halfH}, nil
	case "bottom":
		return rect{s.x, s.y + halfH, s.w, s.h - halfH}, nil
	case "top-left", "topleft":
		return rect{s.x, s.y, halfW, halfH}, nil
	case "top-right", "topright":
		return rect{s.x + halfW, s.y, s.w - halfW, halfH}, nil
	case "bottom-left", "bottomleft":
		return rect{s.x, s.y + halfH, halfW, s.h - halfH}, nil
	case "bottom-right", "bottomright":
		return rect{s.x + halfW, s.y + halfH, s.w - halfW, s.h - halfH}, nil
	case "centre", "center":
		w, h := s.w*2/3, s.h*2/3
		return rect{s.x + (s.w-w)/2, s.y + (s.h-h)/2, w, h}, nil
	}
	return rect{}, fmt.Errorf("%q is not a place. Use left, right, top, bottom, a quarter "+
		"like top-left, maximise, restore, centre or full, or give exact pixels", where)
}

// describeTarget is what the confirmation prompt says will happen.
func describeTarget(where string, args map[string]any) string {
	if where != "" {
		return "the " + where
	}
	return fmt.Sprintf("%d,%d at %dx%d", argInt(args, "x", 0), argInt(args, "y", 0),
		argInt(args, "width", 0), argInt(args, "height", 0))
}

// windowGeometry reads where a window is, as "x,y 800x600", or empty when it
// cannot be read.
func windowGeometry(ctx context.Context, id string) string {
	out, err := run(ctx, 5*time.Second, "xdotool", "getwindowgeometry", "--shell", id)
	if err != nil {
		return ""
	}
	var x, y, w, h string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "X":
			x = v
		case "Y":
			y = v
		case "WIDTH":
			w = v
		case "HEIGHT":
			h = v
		}
	}
	if x == "" || w == "" {
		return ""
	}
	return fmt.Sprintf("%s,%s %sx%s", x, y, w, h)
}

// wmState asks the window manager to add or remove a state, which is how a
// maximised window is maximised properly rather than merely resized to fill.
func wmState(ctx context.Context, title, verb, states string) error {
	if !have("wmctrl") {
		return fmt.Errorf("wmctrl is not installed, and maximising is something the window " +
			"manager has to do rather than something xdotool can fake")
	}
	if _, err := run(ctx, 5*time.Second, "wmctrl", "-r", title, "-b", verb+","+states); err != nil {
		return fmt.Errorf("the window manager refused to %s %s: %w", verb, states, err)
	}
	return nil
}
