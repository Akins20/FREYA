package skills

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Where a thread of work is happening.
//
// # Why the process working directory could not stay
//
// Every file tool resolved a relative path through os.Getwd and every shell tool
// defaulted to the same place, which was fine while exactly one thing happened at
// a time. It also caused a whole class of bug on its own: file tools once used
// the process directory while shell tools defaulted to home, so a file she wrote
// and a command she ran landed in different places and any write-then-run task
// failed silently. That was cured by making both use os.Chdir — one directory for
// both — which works, and cannot survive concurrency: os.Chdir is process-global,
// so two threads of work cannot be in two directories, and one changing folders
// would silently move the other.
//
// A Scope is that directory, carried per thread of work in the context instead of
// in the process. change_dir moves the scope, not the process. Nothing else about
// the tools changes, and the write-then-run cure is preserved — file and shell
// tools still read the same single source of truth, it is just no longer global.
type Scope struct {
	ws *Workspace
	// TabPrefix namespaces browser tabs, so two threads driving unnamed tabs
	// cannot steal each other's page.
	TabPrefix string
	// JobID identifies a background job, empty for the foreground conversation.
	JobID string
	// kits are the tool groups offered for this thread of work. Empty means every
	// tool was offered, which is what every path that has not adopted routing
	// gets — so adding kits can never silently narrow an existing caller.
	kits []Kit
	// ledger records the identifiers this thread of work has actually been shown,
	// so a composed one can be told apart from an observed one.
	ledger *Ledger
	// found is what find_tools has pulled up for this thread of work. A pointer,
	// because a tool found in round three has to be callable in round four — the
	// scope is copied into each call, so the set behind it must be shared.
	found *found
	// plan is what this piece of work consists of. A pointer for the same reason
	// as found: a step written in round two is marked in round nine, and the
	// finish gate reads it after the last round of all.
	plan *Plan
}

// Plan is the checklist for this thread of work. Nil when nobody set one up,
// which every check reads as "no opinion" rather than "nothing to do".
func (s Scope) Plan() *Plan { return s.plan }

// Ledger is what this thread of work has been shown. Nil when unset, which the
// checks read as "no opinion" rather than "refuse".
func (s Scope) Ledger() *Ledger { return s.ledger }

// Workspace is the mutable directory a Scope points at.
//
// A pointer, not a value, because change_dir has to move the directory for
// everything else running in that scope — and because a Scope copied into a
// goroutine must still see the move.
type Workspace struct {
	mu  sync.Mutex
	dir string
}

// NewWorkspace creates a workspace rooted at dir. An empty dir means the process
// working directory, which is what a plain interactive session wants.
func NewWorkspace(dir string) *Workspace {
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	return &Workspace{dir: dir}
}

// Dir reports the current directory.
func (w *Workspace) Dir() string {
	if w == nil {
		wd, _ := os.Getwd()
		return wd
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dir
}

// SetDir moves the workspace.
func (w *Workspace) SetDir(dir string) {
	if w == nil || dir == "" {
		return
	}
	w.mu.Lock()
	w.dir = dir
	w.mu.Unlock()
}

// NewScope builds a scope over a workspace.
func NewScope(ws *Workspace, tabPrefix, jobID string) Scope {
	return Scope{ws: ws, TabPrefix: tabPrefix, JobID: jobID,
		ledger: NewLedger(), found: &found{}, plan: NewPlan()}
}

// Dir is where relative paths resolve for this thread of work.
func (s Scope) Dir() string { return s.ws.Dir() }

// OrDefault returns s, or the process-wide default when s is the zero value.
//
// Worth the indirection: a zero Scope reads as the process directory, which looks
// harmless, but writing to its nil workspace silently does nothing — so
// change_dir would report a move that never happened. Resolving to a real,
// mutable workspace keeps the zero value honest.
func (s Scope) OrDefault() Scope {
	if s.ws != nil {
		return s
	}
	return ScopeFrom(context.Background())
}

// SetDir moves this thread of work, and only this one.
func (s Scope) SetDir(dir string) { s.ws.SetDir(dir) }

// Resolve turns a path into an absolute one against this scope. Absolute paths
// and ~ are left to the caller's own expansion; this only anchors the relative.
func (s Scope) Resolve(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.Dir(), path)
}

type scopeKey struct{}

// WithScope attaches a scope to a context, for everything the call fans out to.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// defaultWorkspace backs any call made without a scope — a test, a tool invoked
// directly. It tracks the process directory, so behaviour with no scope set is
// exactly what it was before scopes existed.
var (
	defaultOnce   sync.Once
	defaultWS     *Workspace
	defaultLedger *Ledger
	defaultFound  *found
)

// ScopeFrom returns the scope carried by ctx, or the process-wide default.
func ScopeFrom(ctx context.Context) Scope {
	if ctx != nil {
		if s, ok := ctx.Value(scopeKey{}).(Scope); ok && s.ws != nil {
			return s
		}
	}
	defaultOnce.Do(func() {
		defaultWS = NewWorkspace("")
		defaultLedger = NewLedger()
		defaultFound = &found{}
	})
	return Scope{ws: defaultWS, ledger: defaultLedger, found: defaultFound}
}

// expandIn resolves ~, environment variables and relative paths, anchoring a
// relative path to the caller's scope rather than to the process.
func expandIn(ctx context.Context, p string) string {
	p = os.ExpandEnv(strings.TrimSpace(p))
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(ScopeFrom(ctx).Dir(), p)
}

// Kits are the tool groups offered for this thread of work.
//
// Carried on the scope because it is already the per-thread execution context,
// and because a background job routes on its own goal rather than inheriting the
// foreground's.
func (s Scope) Kits() []Kit { return s.kits }

// WithKits returns a scope that offers these kits.
func (s Scope) WithKits(kits []Kit) Scope {
	s.kits = kits
	return s
}

// offers reports whether a tool was among those shown for this scope.
//
// True when nothing was routed, so an unrouted caller — a test, the one-shot
// CLI, a path that has not adopted kits — never records a spurious miss.
func (s Scope) offers(name string) bool {
	if len(s.kits) == 0 {
		return true
	}
	if s.found != nil && s.found.has(name) {
		return true
	}
	for _, k := range s.kits {
		if kitOf(name) == k {
			return true
		}
	}
	return false
}

// surface records tools find_tools pulled up for this thread of work.
func (s Scope) surface(names ...string) {
	if s.found == nil {
		return
	}
	s.found.add(names...)
}

// Surfaced lists what find_tools has pulled up this exchange, sorted.
//
// The agent reads this between rounds and adds these declarations to the set it
// offers. Handing back a schema she then cannot call would be a cruel joke: a
// provider only emits a call for a function it was declared, so finding a tool
// has to actually put it on the table.
func (s Scope) Surfaced() []string {
	if s.found == nil {
		return nil
	}
	return s.found.list()
}

// found is the set of tools find_tools has surfaced, shared by pointer so a
// scope copied into a goroutine sees the same set.
type found struct {
	mu    sync.Mutex
	names map[string]bool
}

func (f *found) add(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.names == nil {
		f.names = map[string]bool{}
	}
	for _, n := range names {
		f.names[n] = true
	}
}

func (f *found) has(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.names[name]
}

func (f *found) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.names))
	for n := range f.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// NewScopeWithPlan builds a scope around an existing plan. For tests and for
// anything that needs to inspect the list from outside the tools that write it.
func NewScopeWithPlan(ws *Workspace, plan *Plan) Scope {
	s := NewScope(ws, "", "")
	s.plan = plan
	return s
}
