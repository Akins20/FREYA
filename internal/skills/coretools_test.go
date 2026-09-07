package skills

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/claude"
	"github.com/Akins20/FREYA/internal/defect"
	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/memory"
	"github.com/Akins20/FREYA/internal/playbook"
	"github.com/Akins20/FREYA/internal/routes"
	"github.com/Akins20/FREYA/internal/schedule"
	"github.com/Akins20/FREYA/internal/term"
)

// everything registers the families a real session has, so a name in the core
// kit can be checked against a tool that exists.
func everything(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)

	RegisterSystem(r)
	RegisterShell(r, g)
	RegisterBrowser(r, g, NewTabs())
	RegisterWeb(r, "")
	RegisterDocWriting(r, g)
	RegisterFinder(r)
	RegisterSyntax(r)
	RegisterProjects(r, g, term.NewManager())
	RegisterPDFDesign(r, g)
	RegisterSlides(r, g)
	RegisterSiteCheck(r)
	RegisterPlan(r)
	learned, err := playbook.OpenLearned(dir)
	if err != nil {
		t.Fatal(err)
	}
	RegisterSkillbook(r, g, learned)

	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	RegisterMemory(r, store, memory.BuildIndex(store))

	journal, err := defect.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	RegisterDefects(r, journal)
	places, perr := RegisterPlaces(r, dir)
	if perr != nil {
		t.Fatal(perr)
	}
	RegisterFiles(r, g, places)
	if _, err := RegisterNotes(r, dir); err != nil {
		t.Fatal(err)
	}
	tasks, terr := schedule.Open(dir)
	if terr != nil {
		t.Fatal(terr)
	}
	RegisterSchedule(r, tasks)

	// The families that were missing. Without them the fixture held 102 tools
	// against a real session's 147, so a third of the registry — every desktop
	// tool, every dev tool, the terminal, the clipboard, telemetry — was invisible
	// to every test in this file, including the one whose whole job is to notice
	// a core-kit entry naming a tool that does not exist.
	RegisterArrange(r, g)
	RegisterClipboard(r, g)
	RegisterDesktop(r, g)
	RegisterDev(r, dir)
	RegisterTelemetry(r, dir)
	RegisterTerminal(r, g, term.NewManager())
	RegisterProactive(r, nil)
	RegisterReflection(r, nil)
	routeStore, rerr := routes.Open(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	RegisterServices(r, g, NewTabs(), routeStore)
	claudeClient := claude.New("")
	RegisterClaude(r, g, claudeClient)
	RegisterClaudeAdvice(r, g, claudeClient)
	RegisterDocuments(r, g)
	return r
}

// fixtureTools is how many tools everything() registers.
//
// # Why a number is pinned here and nowhere else
//
// The tool count has been written down in prose three times and been wrong every
// time: CLAUDE.md said 136, the README said 151, the registry held 152. Nothing
// ever compared the sentence to the code, so each number was correct for about a
// fortnight and then quietly was not.
//
// The documents no longer carry it — they point at /tools, which counts the live
// registry. This constant is the one place a number lives, and its job is not to
// be right about the product: it is to FAIL when the registry changes, so that
// whoever added or removed a tool is made to look at what the docs claim.
//
// Bumped from 142 as the document family arrived — list, read, write and save,
// then replace — the test doing exactly its job: it failed, and made
// somebody look at what the documents claim she can do.
//
// Four families are absent because they register nothing without a real
// dependency: work needs a running pool, review and the vision tools need a
// provider that can see, and voice_adjust needs a synthesiser. Passing them nil
// registers zero tools rather than erroring, which is exactly the kind of silent
// nothing this file exists to catch — so they are named here instead of counted.
const fixtureTools = 147

var registeredOnlyWithARealDependency = []string{
	"work_start", "work_list", "work_cancel", // a running pool
	"review",       // a provider that can see
	"voice_adjust", // a synthesiser
}

func TestTheToolCountIsAssertedRatherThanWrittenDown(t *testing.T) {
	r := everything(t)
	got := len(r.Names())
	if got != fixtureTools {
		t.Errorf("the registry holds %d tools, this test expects %d.\n"+
			"That is not a bug — it means tools were added or removed. Update "+
			"fixtureTools, and while you are here check that nothing in README.md, "+
			"CLAUDE.md or docs/ has gone stale about what she can do. The count has "+
			"been wrong in prose three times because nothing made anyone look.",
			got, fixtureTools)
	}

	// And the ones that need hardware really are absent, so the list above stays
	// honest rather than becoming folklore.
	have := map[string]bool{}
	for _, n := range r.Names() {
		have[n] = true
	}
	for _, n := range registeredOnlyWithARealDependency {
		if have[n] {
			t.Errorf("%s is named as needing a real dependency but the fixture "+
				"registered it; move it into the count", n)
		}
	}
}

// The documents must not carry a tool count, because a number in prose is a
// number nothing checks. /tools prints the live one.
func TestTheDocsDoNotWriteTheToolCountDown(t *testing.T) {
	for _, doc := range []string{"../../README.md", "../../CLAUDE.md"} {
		b, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		// "147 tools", "152 tools registered", "136 tools" — a number immediately
		// before the word. Deliberately narrow: prose about "43 of those tools are
		// Chrome" is a proportion, not a total, and does not go stale the same way.
		re := regexp.MustCompile(`\b(\d{2,4}) tools\b`)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			t.Errorf("%s writes the tool count down as %q. It has been wrong three "+
				"times this way. Say what she can do and point at /tools for how many.",
				doc, m[0])
		}
	}
}

// A core entry naming a tool that does not exist is worse than useless: it reads
// as "this is always available" while the real tool falls through to whatever
// kit its prefix implies.
//
// It was not hypothetical. The core kit listed read_file, write_file and
// list_dir; the tools are file_read, file_write and folder_list. So the three
// most basic file operations were routed to the FILES kit, and on any exchange
// that did not mention files — a portal, a quiz, a download — she could not read
// or write a file at all. Nothing failed; the tools simply were not offered.
//
// And the test that was supposed to cover this asserted kitOf("read_file") ==
// KitCore, which passes for any unknown name, because unclassified falls through
// to core. It validated ghosts.
func TestEveryCoreToolExists(t *testing.T) {
	r := everything(t)
	have := map[string]bool{}
	for _, n := range r.Names() {
		have[n] = true
	}
	// Registered only when their dependency is present, which a unit test has no
	// cheap way to build. Their names are checked by the daemon at startup.
	optional := map[string]bool{
		"work_start": true, "work_list": true, "work_cancel": true,
		// Registered only against a provider that can see. A text-only backend has
		// nothing to review a rendered page with, so offering the tool would
		// promise something that cannot happen.
		"review": true,
	}

	var ghosts []string
	for n := range coreTools {
		if !have[n] && !optional[n] {
			ghosts = append(ghosts, n)
		}
	}
	sort.Strings(ghosts)
	if len(ghosts) > 0 {
		t.Errorf("the core kit names %d tools that do not exist: %s\n"+
			"Each one reads as 'always offered' while the real tool falls through to "+
			"its prefix's kit and is invisible for most requests.",
			len(ghosts), strings.Join(ghosts, ", "))
	}
}

// The tools she reaches for most, by their REAL names, asserted against the
// registry rather than against kitOf — which returns core for any unknown string
// and so cannot tell a core tool from a typo.
func TestTheToolsSheUsesMostAreReallyInCore(t *testing.T) {
	r := everything(t)
	have := map[string]bool{}
	for _, n := range r.Names() {
		have[n] = true
	}
	for _, name := range []string{
		"browser_click_text", "browser_read", "browser_open", "memory_recall",
		"run_shell", "file_read", "file_write", "folder_list", "web_search",
	} {
		if !have[name] {
			t.Errorf("%s is not a registered tool at all", name)
			continue
		}
		if !coreTools[name] {
			t.Errorf("%s is one of the tools she uses most but is not in the core kit, "+
				"so it waits on a routing decision", name)
		}
	}
}

// The consequence, stated as behaviour rather than as configuration.
//
// "open my portal and do the quizzes" routes to browsing. Before the core kit
// was corrected, that exchange offered no way to read or write a file — not
// because anything failed, but because file_read and file_write were sitting in
// a kit the request never asked for.
func TestABrowsingRequestCanStillTouchFiles(t *testing.T) {
	r := everything(t)
	kits := Route("open my portal and do the quizzes")

	var browsing bool
	for _, k := range kits {
		if k == KitBrowsing {
			browsing = true
		}
	}
	if !browsing {
		t.Fatalf("precondition: that request should route to browsing, got %v", kits)
	}

	offered := map[string]bool{}
	for _, tool := range r.ToolsFor(kits) {
		offered[tool.Name] = true
	}
	for _, name := range []string{"file_read", "file_write", "folder_list"} {
		if !offered[name] {
			t.Errorf("%s is not offered on a browsing request — she has no way to save "+
				"or read anything while working a page", name)
		}
	}
}

// The chart engine was in internal/docs from the day it was written — bar, line
// and pie, drawn beside the data — and no tool ever offered a way to ask for
// one. Built, tested, and unreachable, which is the fourth time today.
func TestAChartCanActuallyBeAskedFor(t *testing.T) {
	sheets := parseSheets("---SHEET: Revenue---\n" +
		"---CHART: bar | Revenue by month | categories=0 | values=1,2---\n" +
		"Month,Online,Retail\nJan,100,50\nFeb,120,60\n")
	if len(sheets) != 1 {
		t.Fatalf("got %d sheets", len(sheets))
	}
	c := sheets[0].Chart
	if c == nil {
		t.Fatal("the chart directive produced no chart")
	}
	if c.Kind != "bar" || c.Title != "Revenue by month" {
		t.Errorf("kind=%q title=%q", c.Kind, c.Title)
	}
	if c.CategoryColumn != 0 || len(c.ValueColumns) != 2 {
		t.Errorf("categories=%d values=%v", c.CategoryColumn, c.ValueColumns)
	}
	// Rows are counted once the sheet is fully read, less the header.
	if c.Rows != 2 {
		t.Errorf("rows=%d, want 2 (three lines less the header)", c.Rows)
	}
	if c.SheetName != "Revenue" {
		t.Errorf("sheet=%q", c.SheetName)
	}
	// A sheet with no directive stays chartless — this must not fire on its own.
	plain := parseSheets("a,b\n1,2\n")
	if plain[0].Chart != nil {
		t.Error("a sheet with no chart directive grew one")
	}
}
