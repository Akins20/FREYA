package skills

import (
	"context"
	"os"
	"path/filepath"
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
	// ledger records the identifiers this thread of work has actually been shown,
	// so a composed one can be told apart from an observed one.
	ledger *Ledger
}

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
	return Scope{ws: ws, TabPrefix: tabPrefix, JobID: jobID, ledger: NewLedger()}
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
	})
	return Scope{ws: defaultWS, ledger: defaultLedger}
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
