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
	"time"
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
	// gaps are the reasons this reader knows its own answer is incomplete.
	//
	// A tree that stopped early is not a smaller tree, it is a false statement
	// about what the window contains, and everything built on top inherits the
	// lie: desktop_type_into answers "nothing in this window is called Full
	// Name" about a window that has a field called Full Name, and she believes
	// it, because the tool sounded certain. Measured on the run that found the
	// parser bug below: five wasted rounds and a wrong conclusion, from a tool
	// that never returned an error.
	gaps []string
	// invoke replaces the gdbus subprocess in tests, and is nil in every real
	// Reader.
	invoke func(ctx context.Context, bus, path, method string, args ...string) (string, error)
}

// cannot records a reason the tree is incomplete, once.
func (r *Reader) cannot(why string) {
	for _, g := range r.gaps {
		if g == why {
			return
		}
	}
	r.gaps = append(r.gaps, why)
}

// Incomplete says what this reader could not read, and is empty when it read
// everything. Callers that report a tree, or report something missing from one,
// have to say this or their answer is unearned.
func (r *Reader) Incomplete() string {
	if len(r.gaps) == 0 {
		return ""
	}
	return strings.Join(r.gaps, "; ")
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
//
// invoke, when set, replaces the subprocess. It is how the wire contract is
// tested: the members and reply shapes below are not guessable, they were read
// off a live bus, and a bug in one of them looks from every layer above like an
// application that does not support the feature. A test that answers exactly
// what a GTK application answers catches that; nothing else does short of
// running a desktop.
func (r *Reader) call(ctx context.Context, bus, path, method string, args ...string) (string, error) {
	if r.invoke != nil {
		return r.invoke(ctx, bus, path, method, args...)
	}
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
//
// The type tag is optional because gdbus prints it once. GVariant's textual
// form annotates a value only where the type would otherwise be ambiguous, so
// the first element of an array carries objectpath and every later one is a
// bare string:
//
//	([(':1.1', objectpath '/org/a11y/atspi/accessible/1'), (':1.1', '/org/a11y/atspi/accessible/2')],)
//
// Requiring the keyword therefore matched exactly one child of every node. The
// walk still descended, so it followed the leftmost path to the first leaf and
// returned that as the window: a GTK window with a menu bar, a text field and a
// Submit button came back as a frame containing one separator, while pyatspi
// standing next to it listed all three. Nothing failed and nothing was logged.
// The expression found a match, and nobody checked whether it had found all of
// them.
var reChild = regexp.MustCompile(`\('([^']+)',\s*(?:objectpath\s*)?'([^']+)'\)`)

// children lists a node's children, and says so when it cannot read them all.
//
// The count is checked against the reply rather than trusted, because this
// failure is silent by construction. A parser that returns fewer children than
// the toolkit sent produces a smaller tree, not an error, and a smaller tree is
// indistinguishable from a simpler window. One regular expression that had been
// right about its own first match for weeks is what taught this.
func (r *Reader) children(ctx context.Context, n *Node) []*Node {
	out, err := r.call(ctx, n.Bus, n.Path, iface+".GetChildren")
	if err != nil {
		r.cannot("part of this window would not answer when asked what it contains")
		return nil
	}
	kids, sent := parseChildren(out)
	if sent != len(kids) {
		r.cannot(fmt.Sprintf("%d of %d elements in one part of this window could not be read",
			sent-len(kids), sent))
	}
	return kids
}

// parseChildren reads a GetChildren reply and separately counts what was in it.
//
// Two ways of counting on purpose. The second does not go through the
// expression it is checking, so an expression that quietly stops matching some
// shape of reply shows up as a disagreement rather than as a smaller window.
// Every child is one ('bus', path) tuple, and neither a D-Bus bus name nor an
// object path can contain "('".
func parseChildren(out string) (kids []*Node, sent int) {
	for _, m := range reChild.FindAllStringSubmatch(out, -1) {
		kids = append(kids, &Node{Bus: m[1], Path: m[2]})
	}
	return kids, strings.Count(out, "('")
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
//
// Each bound says which one it was. They existed to stop a runaway and returned
// quietly when they fired, which means a window large enough or deep enough to
// hit one was reported as a window that small — the same silent truncation the
// parser above was guilty of, sitting one function away from it.
func (r *Reader) walk(ctx context.Context, n *Node, depth int) {
	if ctx.Err() != nil {
		r.cannot("reading this window ran out of time before it was finished")
		return
	}
	if depth > maxDepth {
		r.cannot(fmt.Sprintf("this window nests deeper than %d levels and the rest was not read", maxDepth))
		return
	}
	if r.nodes > maxNodes {
		r.cannot(fmt.Sprintf("this window has more than %d elements and the rest was not read", maxNodes))
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
//
// # How many actions is a property, not a method
//
// org.a11y.atspi.Action publishes a read-only NActions property. There is no
// GetNActions method and calling one errors with UnknownMethod, which is where
// this went wrong: the error was read as "this node has no actions", so every
// node on every toolkit came back with none. desktop_click silently fell back
// to aiming a pointer at coordinates, desktop_menu refused every menu as
// unactionable, and the feature whose entire point was to stop working from a
// photograph had never once run.
//
// What made it survive a probe: the probe asked GetNActions of a desktop frame,
// an application, a dialog, some panels and a label. Every one of those really
// does lack the Action interface, so every reply was an error, and an error was
// the expected answer. It proved nothing and read as confirmation. A probe has
// to include the case that should succeed or it cannot fail.
func (r *Reader) Actions(ctx context.Context, n *Node) []string {
	if n == nil {
		return nil
	}
	out, err := r.call(ctx, n.Bus, n.Path, propsGet, "org.a11y.atspi.Action", "NActions")
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
// through the bus with no pointer involved: the file appeared. That verification
// was run with gdbus by hand, against DoAction directly, and this function
// gates on Actions first — which was asking for a method that does not exist,
// so for as long as the claim above sat here nothing had ever reached DoAction
// through this path. Proving the primitive is not proving the caller.
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
// # Why the length is asked for rather than passed as -1
//
// AT-SPI takes -1 as "to the end", and gdbus never sees it: its own option
// parser takes a leading dash as a flag, prints its usage and exits, so the
// read came back as a failure on every field on every toolkit. The symptom was
// desktop_type_into setting a field successfully and then reporting that it
// could not tell whether the text had landed — a true statement produced by a
// broken instrument, which is the hardest kind to notice.
//
// CharacterCount is a property on the same interface, so this costs one extra
// call and passes no argument gdbus can mistake for an option.
func (r *Reader) Text(ctx context.Context, n *Node) (string, bool) {
	if n == nil {
		return "", false
	}
	out, err := r.call(ctx, n.Bus, n.Path, propsGet, "org.a11y.atspi.Text", "CharacterCount")
	if err != nil {
		return "", false
	}
	count := 0
	if _, err := fmt.Sscanf(unquote(out), "%d", &count); err != nil {
		return "", false
	}
	if count <= 0 {
		// An empty field is readable and empty, which is not the same as a field
		// that cannot be read, and the caller distinguishes them.
		return "", true
	}
	out, err = r.call(ctx, n.Bus, n.Path, "org.a11y.atspi.Text.GetText", "0", fmt.Sprint(count))
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

// Refresh re-reads a node's children from the bus, discarding what was there.
//
// Needed because a menu does not have its items until it is opened. Toolkits
// populate a submenu when it is first shown, so a tree walked before the click
// shows an empty menu and a walk after it shows the contents — and the same node
// legitimately answers differently at two moments. Anything that opens something
// has to look again rather than reuse what it read.
// # Why it reads more than one level
//
// One level is what a menu looks like on GTK, where the items hang directly off
// the menu. Qt puts an unnamed "popup menu" in between, so a one-level refresh
// of an opened Qt menu returns exactly one anonymous child and there is nothing
// to match a name against: "nothing in File is called Save As", about a File
// menu containing Save As. Measured against a live Qt 5 window, and it is the
// same lesson as GTK calling its window a dialog while Qt calls it a filler,
// one more level down.
//
// The depth is small and fixed. A menu is shallow by construction, so three
// levels covers a wrapper Qt inserts and the wrapper some other toolkit has not
// invented yet, while a full walk from an opened menu would pay for every
// submenu under it on every step of the path.
const refreshDepth = 3

func (r *Reader) Refresh(ctx context.Context, n *Node) {
	if n == nil {
		return
	}
	r.refresh(ctx, n, refreshDepth)
}

func (r *Reader) refresh(ctx context.Context, n *Node, depth int) {
	if depth <= 0 || ctx.Err() != nil {
		return
	}
	n.Children = r.children(ctx, n)
	for _, c := range n.Children {
		r.describe(ctx, c)
		r.refresh(ctx, c, depth-1)
	}
}

// Named reports whether anything under this node has a name worth matching.
//
// The emptiness test for an opened menu, because "has children" is satisfied by
// Qt's anonymous popup wrapper the instant the menu opens, and the items arrive
// after it. Waiting for children therefore stopped waiting too early and then
// searched a wrapper with nothing in it.
func Named(n *Node) bool {
	if n == nil {
		return false
	}
	for _, c := range n.Children {
		if strings.TrimSpace(c.Name) != "" || Named(c) {
			return true
		}
	}
	return false
}

// Activating reports whether an action name means "do this" rather than "get
// ready to do this".
//
// The vocabularies do not overlap and one of them contains a trap. GTK offers a
// button one action, "click". Qt offers "Press" and also "SetFocus", and a Qt
// text field offers SetFocus alone — so a click that takes whatever action came
// first would focus the field, succeed, and report that it had pressed
// something. That is a false success of exactly the kind this package exists to
// prevent, and it would have been invisible: the tool returns, the guard logs an
// action, and nothing anywhere says the click did not happen.
//
// Unknown names are not activating. A verb nobody here has seen is more likely
// to be another SetFocus than another Press, and the cost of being wrong the
// cautious way is a pointer click that works.
//
// Chromium adds a third vocabulary and is not even consistent inside one tree:
// its menu bar entries answer doDefault, its buttons press, its text fields
// activate, a clickable div click, and a text node inside that div
// clickAncestor — which does the useful thing, since the ancestor is what
// carries the handler. showContextMenu is on every node and is deliberately not
// here: it is the right button, and treating it as a click would open a context
// menu and call it a press.
func Activating(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "click", "press", "activate", "invoke", "do", "open", "showmenu", "toggle",
		"dodefault", "clickancestor":
		return true
	}
	return false
}

// ChromiumLike reports whether a node's actions look like Blink's.
//
// showContextMenu is published by Chromium on every node and by neither GTK nor
// Qt, which makes it a usable signature. It is only ever used to choose what to
// say — never to change what is done — so a false positive costs a sentence of
// advice and nothing else.
func ChromiumLike(acts []string) bool {
	for _, a := range acts {
		if strings.EqualFold(strings.TrimSpace(a), "showContextMenu") {
			return true
		}
	}
	return false
}

// PreferredAction picks the action that means "do this", and reports false when
// the node publishes none worth performing.
func PreferredAction(names []string) (int, bool) {
	for i, n := range names {
		if Activating(n) {
			return i, true
		}
	}
	return 0, false
}

// OpenAndRefresh performs a node's action and waits for children to appear.
//
// Population is asynchronous: the action returns as soon as the toolkit accepts
// it, and the items exist a moment later. Reading immediately finds an empty
// menu and concluding it is empty would be wrong for the commonest menu there
// is. Bounded, because a menu that genuinely has no items must not hang the turn
// waiting for some to arrive.
func (r *Reader) OpenAndRefresh(ctx context.Context, n *Node) error {
	acts := r.Actions(ctx, n)
	i, ok := PreferredAction(acts)
	if !ok {
		return fmt.Errorf("%q is a %s and publishes no way to open it%s", n.Name, n.Role,
			listActions(acts))
	}
	if err := r.Do(ctx, n, i); err != nil {
		return err
	}
	for range 10 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(60 * time.Millisecond):
		}
		r.Refresh(ctx, n)
		// Named rather than non-empty: Qt's popup wrapper appears immediately
		// and its items a moment later, so counting children stopped the wait
		// before there was anything to read.
		if Named(n) {
			return nil
		}
	}
	return nil
}

// stateShowing is SHOWING in the AT-SPI state bitmap, which is published as two
// 32-bit words and read here for the first one only. Confirmed against a live Qt
// popup rather than taken from the header: closed it reads ENABLED, RESIZABLE
// and SENSITIVE; opened it gains exactly SHOWING and VISIBLE.
const stateShowing = 25

// reState pulls the first word out of a GetState reply. gdbus tags the array
// element type once, as everywhere else here, so the reply is ([uint32 N, M],).
var reState = regexp.MustCompile(`uint32\s+(\d+)`)

func (r *Reader) showing(ctx context.Context, n *Node) bool {
	out, err := r.call(ctx, n.Bus, n.Path, iface+".GetState")
	if err != nil {
		return false
	}
	m := reState.FindStringSubmatch(out)
	if m == nil {
		return false
	}
	v, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return false
	}
	return v&(1<<stateShowing) != 0
}

// StillOpen reports whether what this node opened is on screen.
//
// # Why a menu has to be asked rather than assumed closed
//
// Choosing an item does not always close the menu it was in. Measured on Qt 5:
// after the item's own Press action fires and the application's handler runs,
// the popup's state is bit-for-bit what it was while open, SHOWING and VISIBLE
// intact. The popup still holds a pointer grab, so the next click anywhere goes
// to dismissing it and never reaches what it was aimed at — and the click
// reports success, because a synthetic click has no way to know it was eaten.
// Measured: click, menu, click, click, and the middle click is the one that
// vanishes. GTK closes its own menus and never showed this.
//
// The children are asked, not the node. Qt's menu bar entry is on screen
// whether or not its menu is down, so asking the entry answers yes forever; the
// popup underneath it is the thing whose visibility means anything. On GTK
// there is no wrapper and the items themselves answer, which is the same
// question one level up. No role names are involved, deliberately.
func (r *Reader) StillOpen(ctx context.Context, n *Node) bool {
	if n == nil {
		return false
	}
	for _, c := range n.Children {
		if r.showing(ctx, c) {
			return true
		}
	}
	return false
}

// GrabFocus asks the toolkit to move focus to a node.
//
// The way in when a field has no writable interface: focus it and use the
// keyboard. Component is implemented far more widely than EditableText, and on
// Chromium it is implemented where EditableText is not there at all.
func (r *Reader) GrabFocus(ctx context.Context, n *Node) error {
	if n == nil {
		return fmt.Errorf("nothing to focus")
	}
	if _, err := r.call(ctx, n.Bus, n.Path, component+".GrabFocus"); err != nil {
		return fmt.Errorf("%q would not take focus: %w", n.Name, err)
	}
	return nil
}

// WindowPaths is the set of windows currently on the bus, for noticing one that
// appears.
func (r *Reader) WindowPaths(ctx context.Context) map[string]bool {
	seen := map[string]bool{}
	apps, err := r.Applications(ctx)
	if err != nil {
		return seen
	}
	for _, a := range apps {
		for _, w := range a.Children {
			seen[w.Bus+w.Path] = true
		}
	}
	return seen
}

// OpenedElsewhere finds a window that appeared since the snapshot was taken,
// walked and ready to be searched.
//
// # Why a menu can be nowhere near the thing that opened it
//
// GTK puts a menu's items directly under the menu. Qt puts them behind an
// unnamed popup wrapper under the menu. Chromium puts them in a second
// top-level window and leaves the button that opened them childless forever —
// measured on Electron 31, where pressing File returns true, the button's
// children stay an empty array, and an unnamed frame appears beside the
// application's real window holding the entire menu.
//
// So following the node that was opened finds nothing, which is exactly what it
// reported: "File opened and is empty". Three toolkits, three different places
// to look, and no way to tell which from the node itself.
func (r *Reader) OpenedElsewhere(ctx context.Context, before map[string]bool) *Node {
	apps, err := r.Applications(ctx)
	if err != nil {
		return nil
	}
	for _, a := range apps {
		for _, w := range a.Children {
			if before[w.Bus+w.Path] {
				continue
			}
			r.walk(ctx, w, 0)
			if Named(w) {
				return w
			}
		}
	}
	return nil
}

// listActions renders what a node did offer, for a refusal that says why.
func listActions(acts []string) string {
	if len(acts) == 0 {
		return ""
	}
	return fmt.Sprintf(" (it offers only %s)", strings.Join(acts, ", "))
}
