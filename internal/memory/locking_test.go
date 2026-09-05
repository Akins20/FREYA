package memory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// No method that holds the store lock may call a helper that takes it again.
//
// sync.RWMutex is not reentrant, so this deadlocks — and not merely the one
// call: the lock is never released, so every later read and write to the archive
// blocks behind it forever. Nothing panics, nothing logs, the request simply
// never returns. In the window it wedged the whole daemon and it had to be
// killed.
//
// It is a one-word difference (saveJSON against saveJSONLocked) with that as the
// penalty, which is exactly the kind of thing a person gets right by attention
// and a machine gets right by reading the file. It has already happened twice:
// once in NewSession, and once when a careless search-and-replace swapped the
// two in Advance and WorkingSet, where the only symptom was one test in the
// suite hanging until the ten-minute timeout.
//
// Read from the source rather than exercised at runtime, because the bad path is
// often conditional — Advance returns early unless the anchor actually moves, so
// most tests never reach the deadlock at all.
func TestNothingHoldingTheLockCallsSomethingThatTakesIt(t *testing.T) {
	// Helpers that acquire s.mu themselves. Anything here is forbidden inside a
	// function that has already locked.
	locking := map[string]bool{"saveJSON": true}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var offences []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		var locked bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := selector(call.Fun)
			switch name {
			case "s.mu.Lock", "s.mu.RLock":
				locked = true
			}
			if !locked {
				return true
			}
			if fname, ok := strings.CutPrefix(name, "s."); ok && locking[fname] {
				offences = append(offences, fn.Name.Name+" calls "+fname+
					" at "+fset.Position(call.Pos()).String())
			}
			return true
		})
	}

	sort.Strings(offences)
	if len(offences) > 0 {
		t.Errorf("%d place(s) call a locking helper while already holding the lock:\n  %s\n"+
			"Each one deadlocks the store permanently — not just that call, every "+
			"later read and write behind it. Use the Locked variant.",
			len(offences), strings.Join(offences, "\n  "))
	}
}

// And the guard against the guard: if saveJSON ever stops locking, the test
// above quietly stops meaning anything.
func TestSaveJSONStillTakesTheLock(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "saveJSON" || fn.Body == nil {
			continue
		}
		var locks bool
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && selector(call.Fun) == "s.mu.Lock" {
				locks = true
			}
			return true
		})
		if !locks {
			t.Error("saveJSON no longer takes the lock, so the rule above now forbids " +
				"something harmless and permits the real thing")
		}
		return
	}
	t.Error("saveJSON is gone; the rule above is checking for a name that no longer exists")
}

// selector renders a call target as dotted text: s.mu.Lock, s.saveJSON, Open.
func selector(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if base := selector(v.X); base != "" {
			return base + "." + v.Sel.Name
		}
		return v.Sel.Name
	}
	return ""
}
