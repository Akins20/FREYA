package skills

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The descriptions are the only prose the model reads every single turn, and
// nothing checked them until an audit read all 140 by hand.
//
// Most of what that audit found cannot be automated. desktop_click claimed it
// refused a control scrolled out of view, months after the action path made
// that work; no test can know whether a sentence about behaviour is still true.
//
// One half can. A description that names a tool is making a checkable claim,
// and it is the half that breaks silently: rename a tool and every sentence
// pointing at the old name still reads perfectly, still ships, and sends her
// after something that is not there. Tool routing makes it worse, because a
// name she reaches for that was never offered is executed anyway if it exists —
// and simply lost if it does not.

// toolLike matches a snake_case identifier, which is the shape of every tool
// name in this package.
var toolLike = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// knownNotATool holds snake_case words that appear in descriptions and are not
// tools. Empty today. It exists so that the first false positive costs one line
// and a reason rather than becoming an argument for deleting the test.
var knownNotATool = map[string]bool{}

func TestNoDescriptionNamesAToolThatDoesNotExist(t *testing.T) {
	tools := toolsInSource(t)

	// The same guard the configuration audit uses. A parser that quietly stops
	// matching returns nothing and passes, which is the failure this whole file
	// is about wearing a different hat.
	if len(tools) < 100 {
		t.Fatalf("found %d tool declarations in the source, and there are well over a "+
			"hundred — this test has stopped looking", len(tools))
	}

	for name, desc := range tools {
		for _, tok := range toolLike.FindAllString(desc, -1) {
			if _, ok := tools[tok]; ok {
				continue
			}
			if knownNotATool[tok] {
				continue
			}
			t.Errorf("the description of %s sends her to %q, which is not a tool. "+
				"Either it was renamed and this sentence was not, or it is a word that "+
				"happens to look like a tool name and belongs in knownNotATool.", name, tok)
		}
	}
}

// toolsInSource reads every llm.Tool declaration out of the repository.
//
// # Why it parses rather than registering
//
// Building the real registry means constructing a guard, a store, an index, a
// browser, a terminal manager and a Claude client, several of which touch disk
// or the network. The declarations are what this is about and they are right
// there in the syntax, so the syntax is what it reads.
//
// # Why the root comes from the working directory
//
// runtime.Caller would be the obvious way and it is a trap: it bakes in the
// path of the machine that compiled the test. A cross-compiled binary carries a
// Windows path that does not exist on the Linux box running it, so the walk
// finds nothing, and the test fails for a reason that has nothing to do with
// the code. That happens today in internal/config and cost time to diagnose
// twice. The working directory is the package directory under `go test`, which
// is true wherever it was built.
func toolsInSource(t *testing.T) map[string]string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Skipf("cannot tell where this is running from, so the source cannot be read: %v", err)
	}
	root := ""
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			root = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("no go.mod above the working directory, so the source is not here to read")
		}
		dir = parent
	}

	tools := map[string]string{}
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			var name, desc string
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				s, ok := literalText(kv.Value)
				if !ok {
					continue
				}
				switch key.Name {
				case "Name":
					name = s
				case "Description":
					desc = s
				}
			}
			// Both, so parameter properties — which have a Description and no
			// Name — do not arrive as nameless tools.
			if name != "" && desc != "" {
				tools[name] = desc
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Skipf("could not read the source under %s: %v", root, err)
	}
	return tools
}

// literalText flattens a string literal, or a chain of them joined by +, which
// is how every description in this package is written.
func literalText(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, okL := literalText(v.X)
		r, okR := literalText(v.Y)
		return l + r, okL && okR
	}
	return "", false
}
