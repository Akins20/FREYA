package skills

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/term"
)

func projectSkills(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FREYA_WORK_DIR", root)
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterProjects(r, g, term.NewManager())
	return r, root
}

// The heap this exists to prevent: a six-page site, two reports and two PDFs in
// one flat directory, including a _v2 suffix where a second attempt had nowhere
// else to go.
func TestWorkGetsItsOwnFolderAndSheMovesIntoIt(t *testing.T) {
	r, root := projectSkills(t)
	scope := NewScope(NewWorkspace(root), "", "")
	ctx := WithScope(context.Background(), scope)

	out, err := r.Execute(ctx, "project_new", map[string]any{"name": "Gisada Replica"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "Gisada-Replica")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("no folder created: %v", err)
	}
	// The whole point: she is IN it, so every relative write lands there.
	if scope.Dir() != dir {
		t.Errorf("she is still in %s, not the new project — relative writes would "+
			"keep landing in the root", scope.Dir())
	}
	if !strings.Contains(out, "relative path") {
		t.Errorf("the result does not explain what changed: %s", out)
	}
}

// Starting a second project while inside the first must put it beside that one,
// not nested inside it — otherwise organising makes the mess deeper.
func TestASecondProjectDoesNotNestInsideTheFirst(t *testing.T) {
	r, root := projectSkills(t)
	scope := NewScope(NewWorkspace(root), "", "")
	ctx := WithScope(context.Background(), scope)

	if _, err := r.Execute(ctx, "project_new", map[string]any{"name": "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(ctx, "project_new", map[string]any{"name": "second"}); err != nil {
		t.Fatal(err)
	}
	if scope.Dir() != filepath.Join(root, "second") {
		t.Errorf("second project landed at %s", scope.Dir())
	}
	if _, err := os.Stat(filepath.Join(root, "first", "second")); err == nil {
		t.Error("the second project nested inside the first")
	}
}

// Reopening one must say what is already in it, or she overwrites her own work
// believing the folder is empty.
func TestReopeningAProjectSaysWhatIsInIt(t *testing.T) {
	r, root := projectSkills(t)
	dir := filepath.Join(root, "report")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("x"), 0o644)

	ctx := WithScope(context.Background(), NewScope(NewWorkspace(root), "", ""))
	out, err := r.Execute(ctx, "project_new", map[string]any{"name": "report"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already existed") || !strings.Contains(out, "index.html") {
		t.Errorf("reopening did not report the contents: %s", out)
	}
}

// A port is only free until something takes it, so the check has to be a real
// bind rather than a guess.
func TestPortHelpersUseRealBinds(t *testing.T) {
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if busy(p) {
		t.Errorf("port %d was reported busy immediately after being found free", p)
	}
	if waitListening(p, 200*1e6) {
		t.Errorf("nothing is listening on %d but waitListening said there was", p)
	}
}

// Stopping when nothing runs must be a plain answer, not an error.
func TestStoppingNothingIsNotAFailure(t *testing.T) {
	r, _ := projectSkills(t)
	out, err := r.Execute(context.Background(), "serve_stop", nil)
	if err != nil {
		t.Fatalf("stopping with no servers errored: %v", err)
	}
	if !strings.Contains(out, "No servers") {
		t.Errorf("unclear answer: %s", out)
	}
}

// Starting a project must hand over the design rules, not merely make them
// available. They were available the whole time a build came back with nine
// cards and two emoji, and the skill tool was never called.
func TestStartingAProjectHandsOverTheDesignRules(t *testing.T) {
	brief := designBrief()
	if brief == "" {
		t.Fatal("project_new says nothing about design")
	}
	for _, want := range []string{
		"THREE-CARD ROW",       // the rule the last build broke nine times
		"A NAV ITEM IS A PAGE", // the one that keeps her off single-page sites
		"EM DASHES",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the rules handed over do not include %q", want)
		}
	}
}

// She has to be able to tell a live URL from one she handed over before a
// restart, because nothing else will tell her.
//
// Servers are tied to her process, which is right — they should not outlive her
// — but it means every localhost URL she has ever given out goes dead the moment
// she is restarted, silently, with the session record gone too. Liveness is
// therefore asked of the PORT, not inferred from the bookkeeping.
func TestServeListReportsWhetherAPortStillAnswers(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if busy(port) {
		t.Fatalf("precondition: %d should be free", port)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	if !busy(port) {
		t.Error("a port with something listening on it reported as free")
	}
	ln.Close()
	if busy(port) {
		t.Error("a port reported as answering after its listener closed — a dead URL " +
			"would be reported as live, which is the whole failure")
	}
}

// The tool exists and is offered without waiting on a routing decision, the same
// as serve and serve_stop.
func TestServeListIsRegisteredAndInCore(t *testing.T) {
	r := everything(t)
	found := false
	for _, n := range r.Names() {
		if n == "serve_list" {
			found = true
		}
	}
	if !found {
		t.Fatal("serve_list is not registered")
	}
	if !coreTools["serve_list"] {
		t.Error("serve_list is not in the core kit, so 'what have I got running' waits " +
			"on the request happening to route to dev")
	}
}
