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
	root     = "/org/a11y/atspi/accessible/root"
	registry = "org.a11y.atspi.Registry"
	iface    = "org.a11y.atspi.Accessible"
	propsGet = "org.freedesktop.DBus.Properties.Get"
	maxNodes = 4000
	maxDepth = 40
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
