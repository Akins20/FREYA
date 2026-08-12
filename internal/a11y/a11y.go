// Package a11y reads what an application says about itself.
//
// # Why this exists
//
// In the browser she reads the DOM: elements, their text, whether a thing is a
// button. Outside it she has a screenshot and a keystroke, which is working from
// a photograph. Every desktop toolkit publishes the same information the DOM
// gives her, through AT-SPI, and nothing here read it.
//
// # Two hops, and the first one is easy to miss
//
// The tree is not on the session bus. The session bus knows only the address of
// a second, private bus, and everything is over there. Code that finds gdbus and
// stops has established nothing — which is a mistake this file's own probe made
// before a container with the registry up and gdbus absent made the difference
// visible.
//
// # Measured, across four toolkits on one screen
//
// Two of them expose anything at all, and the two are not the same shape:
//
//	GTK   app 'zenity'  window role 'dialog'  buttons five levels down,
//	                                          each containing its own label
//	Qt    app ''        window role 'filler'  button two levels down
//	Tk    nothing
//	xterm nothing
//
// Every assumption a reader might reasonably make is wrong in at least one of
// those. Matching an application by name fails on Qt, whose name is empty.
// Looking for a window role of "frame" or "window" finds neither. Walking to a
// fixed depth finds Qt and misses GTK. Flattening naively reports every GTK
// button twice, once as itself and once as its label.
//
// So: windows are matched by title against what wmctrl already knows, the walk
// has no depth assumption, no role is privileged, and a label that merely repeats
// its parent is dropped.
//
// # The answer that matters most is "nothing here"
//
// A Tk or xterm window is on screen, focusable, and typeable, and exposes no
// tree whatsoever. If that came back as an empty result she would read it as an
// empty window, which is the silent capability loss this codebase keeps being
// caught by. It is a distinct, named outcome instead.
package a11y

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Node is one element of an application's own account of itself.
type Node struct {
	// Bus and Path together address the node on the accessibility bus.
	Bus, Path string
	// Role is the toolkit's word for what this is: button, label, dialog,
	// filler, panel. The vocabulary differs between toolkits and nothing here
	// privileges any of it.
	Role string
	// Name is the text a person would use to refer to it.
	Name     string
	Children []*Node
}

// ErrNoRegistry means nothing is answering on the accessibility bus.
var ErrNoRegistry = fmt.Errorf("no accessibility registry is answering")

// ErrNoTree means the window is real and exposes nothing.
//
// Distinct from an empty result on purpose. A Tk window, an xterm and anything
// under Wine are all on screen and all invisible here, and reporting that as
// "no elements" would read as an empty window rather than a blind one.
var ErrNoTree = fmt.Errorf("this window publishes no accessibility tree")

const (
	root      = "/org/a11y/atspi/accessible/root"
	registry  = "org.a11y.atspi.Registry"
	iface     = "org.a11y.atspi.Accessible"
	propsGet  = "org.freedesktop.DBus.Properties.Get"
	component = "org.a11y.atspi.Component"
	maxNodes  = 4000
	maxDepth  = 40
)

// Address asks the session bus where the accessibility bus lives.
func Address(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "gdbus", "call", "--session",
		"--dest", "org.a11y.Bus", "--object-path", "/org/a11y/bus",
		"--method", "org.a11y.Bus.GetAddress").Output()
	if err != nil {
		return "", ErrNoRegistry
	}
	addr := unquote(strings.TrimSpace(string(out)))
	if !strings.HasPrefix(addr, "unix:") {
		return "", ErrNoRegistry
	}
	return addr, nil
}

// Reader walks the accessibility bus.
type Reader struct {
	addr  string
	nodes int
}

// Open connects to the accessibility bus.
func Open(ctx context.Context) (*Reader, error) {
	addr, err := Address(ctx)
	if err != nil {
		return nil, err
	}
	return &Reader{addr: addr}, nil
}

// call runs one method against a node and returns gdbus's rendering of the reply.
func (r *Reader) call(ctx context.Context, bus, path, method string, args ...string) (string, error) {
	argv := []string{"call", "--address", r.addr, "--dest", bus,
		"--object-path", path, "--method", method}
	argv = append(argv, args...)
	out, err := exec.CommandContext(ctx, "gdbus", argv...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// reChild pulls ('bus.name', objectpath '/path') pairs out of a GetChildren
// reply. Parsed with a regular expression rather than a D-Bus type decoder
// because the reply shape is fixed and this is the only place it is read.
var reChild = regexp.MustCompile(`\('([^']+)',\s*objectpath\s*'([^']+)'\)`)

// children lists a node's children.
func (r *Reader) children(ctx context.Context, n *Node) []*Node {
	out, err := r.call(ctx, n.Bus, n.Path, iface+".GetChildren")
	if err != nil {
		return nil
	}
	var kids []*Node
	for _, m := range reChild.FindAllStringSubmatch(out, -1) {
		kids = append(kids, &Node{Bus: m[1], Path: m[2]})
	}
	return kids
}

// describe fills in a node's role and name.
func (r *Reader) describe(ctx context.Context, n *Node) {
	if out, err := r.call(ctx, n.Bus, n.Path, iface+".GetRoleName"); err == nil {
		n.Role = unquote(out)
	}
	if out, err := r.call(ctx, n.Bus, n.Path, propsGet, iface, "Name"); err == nil {
		n.Name = unquote(out)
	}
}

// walk fills a subtree, bounded so a pathological application cannot hang a turn.
func (r *Reader) walk(ctx context.Context, n *Node, depth int) {
	if depth > maxDepth || r.nodes > maxNodes || ctx.Err() != nil {
		return
	}
	r.nodes++
	r.describe(ctx, n)
	n.Children = r.children(ctx, n)
	for _, c := range n.Children {
		r.walk(ctx, c, depth+1)
	}
}

// Applications returns every application currently on the accessibility bus,
// each walked one level so its windows are known.
func (r *Reader) Applications(ctx context.Context) ([]*Node, error) {
	top := &Node{Bus: registry, Path: root}
	apps := r.children(ctx, top)
	if len(apps) == 0 {
		return nil, ErrNoTree
	}
	for _, a := range apps {
		r.describe(ctx, a)
		a.Children = r.children(ctx, a)
		for _, w := range a.Children {
			r.describe(ctx, w)
		}
	}
	return apps, nil
}

// Window finds the window whose title matches, and walks it in full.
//
// Matched on the window's own name rather than on the application's, because
// Qt reports an empty application name and GTK reports a real one. The title is
// the one thing both agree on, and it is also what wmctrl and xdotool already
// know, so this joins to the tools she has rather than sitting beside them.
func (r *Reader) Window(ctx context.Context, title string) (*Node, error) {
	apps, err := r.Applications(ctx)
	if err != nil {
		return nil, err
	}
	want := strings.ToLower(strings.TrimSpace(title))
	for _, app := range apps {
		for _, w := range app.Children {
			if want != "" && !strings.Contains(strings.ToLower(w.Name), want) {
				continue
			}
			r.walk(ctx, w, 0)
			return w, nil
		}
	}
	return nil, ErrNoTree
}

// Describe renders a subtree the way browser_inspect renders a page: what is
// there, what it is called, and what can be aimed at.
//
// A label whose text merely repeats its parent is dropped. GTK gives every
// button a label child carrying the same string, so keeping both reports every
// button twice and doubles the length of the thing she has to read.
func Describe(n *Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	render(&b, n, 0)
	return strings.TrimRight(b.String(), "\n")
}

func render(b *strings.Builder, n *Node, depth int) {
	if n == nil || depth > maxDepth {
		return
	}
	name := strings.TrimSpace(n.Name)
	// Anonymous containers carry nothing a reader can aim at, and GTK stacks them
	// four deep. Skip the node, keep the children.
	//
	// Recognised by having no name and having children, not by matching a list of
	// role names. Measured: the same GTK node that pyatspi calls "panel" comes
	// back from the D-Bus GetRoleName as "generic", so a role list written against
	// either vocabulary silently stops working against the other. This is the same
	// lesson as GTK calling its window a dialog and Qt calling it a filler, one
	// level further down.
	if name == "" && len(n.Children) > 0 {
		for _, c := range n.Children {
			render(b, c, depth)
		}
		return
	}
	fmt.Fprintf(b, "%s%s", strings.Repeat("  ", depth), n.Role)
	if name != "" {
		fmt.Fprintf(b, " %q", name)
	}
	b.WriteString("\n")
	for _, c := range n.Children {
		if c != nil && c.Role == "label" && strings.TrimSpace(c.Name) == name {
			continue // GTK's echo of its own button text
		}
		render(b, c, depth+1)
	}
}

// unquote strips gdbus's tuple and variant wrapping from a single-value reply:
// ('zenity',) and (<'zenity'>,) both become zenity.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	s = strings.TrimSuffix(strings.TrimSpace(s), ",")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.Trim(strings.TrimSpace(s), "'")
}

// Rect is where a node is on screen, in pixels.
type Rect struct{ X, Y, W, H int }

// Centre is where to click.
func (r Rect) Centre() (int, int) { return r.X + r.W/2, r.Y + r.H/2 }

// Offscreen reports a rectangle that cannot be clicked.
//
// AT-SPI answers for nodes that are scrolled away, in a collapsed panel, or
// belong to a window that is minimised, and it answers with real numbers rather
// than an error. A zero-sized box and a box at a negative coordinate are both
// "there, but not where a pointer can reach it", and clicking the middle of
// either lands somewhere arbitrary — which is worse than refusing, because it
// looks like it worked.
func (r Rect) Offscreen() bool {
	return r.W <= 0 || r.H <= 0 || r.X+r.W <= 0 || r.Y+r.H <= 0
}

// reExtents pulls the four numbers out of a GetExtents reply, which gdbus
// renders as ((x, y, w, h),).
var reExtents = regexp.MustCompile(`\(\s*(-?\d+),\s*(-?\d+),\s*(-?\d+),\s*(-?\d+)\s*\)`)

// parseExtents reads gdbus's rendering of a (iiii) struct.
//
// Separate from the call so the parsing is testable without a bus, which is the
// only part of this that can be got wrong quietly: a misparse produces a
// plausible rectangle rather than an error, and she clicks a coordinate nobody
// chose.
func parseExtents(reply string) (Rect, bool) {
	m := reExtents.FindStringSubmatch(reply)
	if m == nil {
		return Rect{}, false
	}
	n := make([]int, 4)
	for i := range n {
		v, err := strconv.Atoi(m[i+1])
		if err != nil {
			return Rect{}, false
		}
		n[i] = v
	}
	return Rect{X: n[0], Y: n[1], W: n[2], H: n[3]}, true
}

// Extents asks where a node is, in screen coordinates.
//
// # Why this is not fetched during the walk
//
// The walk already costs three gdbus calls per node against a four thousand node
// ceiling, and every one is a process. Asking every node where it is would add a
// third again to a tree she mostly wants to READ. Position only matters for the
// one node she is about to act on, so it is asked for then.
//
// # Not every node has a position
//
// Component is an optional interface. Labels, panels and whole toolkits do not
// implement it, and the call simply fails — which is why this returns whether it
// could answer rather than a zero rectangle. A zero rectangle is a real answer
// meaning "at the origin, no size", and it is exactly what a node scrolled out
// of view reports.
func (r *Reader) Extents(ctx context.Context, n *Node) (Rect, bool) {
	if n == nil {
		return Rect{}, false
	}
	// coordType 0 is screen coordinates. Window-relative would need the window's
	// own origin to be useful, and xdotool clicks in screen space.
	out, err := r.call(ctx, n.Bus, n.Path, component+".GetExtents", "uint32 0")
	if err != nil {
		return Rect{}, false
	}
	return parseExtents(out)
}

// Find returns the first node in a subtree whose name matches, preferring an
// exact match over a containing one.
//
// # Why the shortest containing match
//
// The same rule the browser uses for clicking by text, for the same reason: on a
// real dialog "Save" is also inside "Save As…" and "Don't Save", and the longest
// match is reliably the wrong one. Exact wins outright; otherwise the shortest
// name that contains what she asked for is the least surprising answer.
//
// Role is compared too, so "the OK button" can be asked for as a button when a
// label beside it carries the same text — which is the normal shape of a GTK
// tree, where every button contains a label with identical text.
func Find(root *Node, name, role string) *Node {
	want := strings.ToLower(strings.TrimSpace(name))
	wantRole := strings.ToLower(strings.TrimSpace(role))
	if want == "" {
		return nil
	}
	var exact, shortest *Node
	var visit func(*Node)
	visit = func(n *Node) {
		if n == nil || exact != nil {
			return
		}
		if wantRole == "" || strings.EqualFold(n.Role, wantRole) {
			got := strings.ToLower(strings.TrimSpace(n.Name))
			switch {
			case got == want:
				exact = n
				return
			case strings.Contains(got, want):
				if shortest == nil || len(got) < len(strings.ToLower(shortest.Name)) {
					shortest = n
				}
			}
		}
		for _, c := range n.Children {
			visit(c)
		}
	}
	visit(root)
	if exact != nil {
		return exact
	}
	return shortest
}

// Fingerprint is a stable summary of a subtree, for telling whether acting on it
// changed anything.
//
// The desktop equivalent of the page signature, and it exists for the same
// reason: a click that did nothing and a click that worked look identical from
// the outside, and the honest sentence "nothing observably changed" is only
// honest if something was actually compared. Roles and names in walk order,
// which changes when a dialog opens, a button's label flips, or a list gains a
// row — and does not change when the pointer merely moves.
func Fingerprint(n *Node) string {
	var b strings.Builder
	var visit func(*Node, int)
	visit = func(n *Node, depth int) {
		if n == nil {
			return
		}
		fmt.Fprintf(&b, "%d:%s:%s\n", depth, n.Role, n.Name)
		for _, c := range n.Children {
			visit(c, depth+1)
		}
	}
	visit(n, 0)
	return b.String()
}

// Actions lists what a node can be asked to do, or nothing when it exposes no
// Action interface.
//
// Most nodes do not, and that is correct rather than a fault. A window, a panel
// and a label are not actionable, and asking them errors with "No such
// interface". Only widgets that do something implement it.
func (r *Reader) Actions(ctx context.Context, n *Node) []string {
	if n == nil {
		return nil
	}
	out, err := r.call(ctx, n.Bus, n.Path, "org.a11y.atspi.Action.GetNActions")
	if err != nil {
		return nil
	}
	count := 0
	if _, err := fmt.Sscanf(unquote(out), "%d", &count); err != nil || count <= 0 {
		return nil
	}
	var names []string
	for i := range count {
		nm, err := r.call(ctx, n.Bus, n.Path, "org.a11y.atspi.Action.GetName", fmt.Sprint(i))
		if err != nil {
			continue
		}
		names = append(names, unquote(nm))
	}
	return names
}

// Do performs a node's own action, which runs the handler the application
// registered rather than aiming a pointer at a rectangle.
//
// # Why this is preferred over clicking the middle of the extents
//
// The browser rule that clicks should go through the input pipeline rather than
// element.click() does not carry over, and reasoning by analogy nearly put the
// wrong primitive in. element.click() is worth avoiding because it bypasses the
// event path a real click would take. DoAction is the opposite: it is the same
// entry point a screen reader uses, and it invokes the widget's registered
// handler.
//
// It also removes the two conditions a coordinate click has to refuse. A widget
// scrolled out of view answers Component.GetExtents with real numbers and no
// error, so clicking its middle lands on whatever is there instead — a click
// somewhere nobody chose, that looks like it worked. And a widget whose toolkit
// publishes no position at all cannot be clicked by coordinates and can still be
// actioned. Neither case needs the window raised, focused, or even visible.
//
// Verified against a GTK button whose handler writes a file, driven entirely
// through the bus with no pointer involved: the file appeared.
func (r *Reader) Do(ctx context.Context, n *Node, index int) error {
	names := r.Actions(ctx, n)
	if len(names) == 0 {
		return fmt.Errorf("%q is a %s and publishes no action, so there is nothing to "+
			"perform on it", n.Name, n.Role)
	}
	if index < 0 || index >= len(names) {
		return fmt.Errorf("%q publishes %d action(s) (%s) and there is no number %d",
			n.Name, len(names), strings.Join(names, ", "), index)
	}
	if _, err := r.call(ctx, n.Bus, n.Path,
		"org.a11y.atspi.Action.DoAction", fmt.Sprint(index)); err != nil {
		return fmt.Errorf("performing %q on %q: %w", names[index], n.Name, err)
	}
	return nil
}

// Text returns what a node currently contains, and whether it could be asked.
//
// The Text interface is optional, like Component and Action. A node that does
// not implement it is not empty, it is unreadable, and those are different
// enough that the caller must be able to tell.
func (r *Reader) Text(ctx context.Context, n *Node) (string, bool) {
	if n == nil {
		return "", false
	}
	out, err := r.call(ctx, n.Bus, n.Path, "org.a11y.atspi.Text.GetText", "0", "-1")
	if err != nil {
		return "", false
	}
	return unquote(out), true
}

// SetText replaces what an editable node contains, then reads it back.
//
// # Why it reads back
//
// SetTextContents returns a boolean the toolkit is free to get wrong, and a
// field can refuse the text for reasons nothing here can see: a validator, a
// maximum length, an input mask that reformats as you go. Reporting the string
// that was sent is reporting an intention. The only fact is what the field holds
// afterwards, so that is what comes back, and a caller can say when the two
// differ rather than claiming the typing worked.
//
// This is the same argument as re-reading a page from disk rather than trusting
// what the write tool said about it.
func (r *Reader) SetText(ctx context.Context, n *Node, text string) (string, error) {
	if n == nil {
		return "", fmt.Errorf("nothing to type into")
	}
	if _, err := r.call(ctx, n.Bus, n.Path,
		"org.a11y.atspi.EditableText.SetTextContents", quoteArg(text)); err != nil {
		return "", fmt.Errorf("%q is a %s and would not take text — it may not be editable, "+
			"or its toolkit may not publish the EditableText interface: %w",
			n.Name, n.Role, err)
	}
	got, ok := r.Text(ctx, n)
	if !ok {
		return "", fmt.Errorf("the text was sent to %q but it cannot be read back, so "+
			"whether it landed is unknown — check with desktop_inspect before relying on it",
			n.Name)
	}
	return got, nil
}

// Secret reports a node that holds a credential.
//
// Its contents must never be read back, quoted in a reply, or written to the
// archive. browser_inspect once labelled an input with its own value, which put
// a real password into the archive and into every request after it, and the same
// mistake is available here for free.
//
// Matched on the role rather than on the name, because the toolkit knows and a
// guess from a label does not: a field called "PIN" may be plain text and a
// field called "Key" may be masked.
func (n *Node) Secret() bool {
	role := strings.ToLower(n.Role)
	return strings.Contains(role, "password")
}

// quoteArg renders a string as a gdbus argument. gdbus parses arguments as GVariant
// text, so a bare string needs quoting and any quote inside it needs escaping, or
// the text silently becomes a different string or a parse error.
func quoteArg(s string) string {
	return "'" + strings.NewReplacer(`\`, `\`, `'`, `\'`).Replace(s) + "'"
}
