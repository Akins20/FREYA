package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akins/jarvis/internal/skills"
)

// The gate reads the file, not the tool's opinion of the file.
//
// This is the bug that shipped for one run: she wrote index.html with an
// href="#", was told, and repaired it with file_edit. The first version worked
// out "still broken" by matching the write tool's wording in the trail, saw no
// second file_write, and ended the exchange with "[Not finished: href=\"#\" —
// goes nowhere. It was flagged twice and left as it is]" against a file that was
// clean on disk. Accusing her of leaving work undone when she has just done it
// is worse than not checking at all.
func TestAPageFixedByAnyToolIsNotStillOpen(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "site", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}

	broken := `<body><a href="#" class="logo">Crumb &amp; Crust</a></body>`
	if err := os.WriteFile(page, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &trail{}
	w.add(step{tool: "file_write", round: 1,
		output: "Created " + page + " (7200 bytes).\n\n[This page makes promises it does not keep: " +
			`href="#" — goes nowhere.]`})

	ends := stillOpen(context.Background(), w)
	if len(ends) != 1 {
		t.Fatalf("precondition: the broken page should be open, got %v", ends)
	}
	if !strings.Contains(ends[0], "site/index.html") {
		t.Errorf("open end does not name the page: %q", ends[0])
	}

	// She repairs it with file_edit, which reports only a base name.
	fixed := `<body id="top"><a href="#top" class="logo">Crumb &amp; Crust</a></body>`
	if err := os.WriteFile(page, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	w.add(step{tool: "file_edit", round: 12, output: "Edited index.html: 1 replacement(s), +12 bytes."})

	if ends := stillOpen(context.Background(), w); len(ends) != 0 {
		t.Errorf("a page she repaired is still reported as unfinished: %v\n"+
			"The verdict has to come from the file, not from what a tool said earlier.", ends)
	}
}

// A page that is genuinely still broken at the end must still be caught, or the
// fix above has simply turned the gate off.
func TestAPageLeftBrokenIsStillCaught(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "shop", "index.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte(`<nav><a href="#gallery">Gallery</a></nav>`), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &trail{}
	w.add(step{tool: "file_write", output: "Created " + page + " (40 bytes)."})
	w.add(step{tool: "file_edit", output: "Edited style.css: 1 replacement(s), +4 bytes."})

	ends := stillOpen(context.Background(), w)
	if len(ends) != 1 {
		t.Fatalf("want the dead anchor caught, got %v", ends)
	}
	if !strings.Contains(ends[0], "#gallery") {
		t.Errorf("the end does not name the anchor: %q", ends[0])
	}
	if brief := finishBrief(ends); !strings.Contains(brief, "#gallery") ||
		!strings.Contains(brief, "site_check") {
		t.Errorf("the push does not say what to fix or how to verify:\n%s", brief)
	}
}

// A failed write never happened, and a file deleted since cannot be judged.
// Both would be false accusations.
func TestNothingIsClaimedAboutFilesThatAreNotThere(t *testing.T) {
	w := &trail{}
	w.add(step{tool: "file_write", failed: true, output: "Created /nope/index.html (10 bytes)."})
	w.add(step{tool: "file_write", output: "Created /also-nope/index.html (10 bytes)."})
	if ends := stillOpen(context.Background(), w); len(ends) != 0 {
		t.Errorf("claimed dead links in files that cannot be read: %v", ends)
	}
}

// Only pages. A stylesheet or a script is not a set of promises to click.
func TestOnlyPagesAreJudged(t *testing.T) {
	dir := t.TempDir()
	css := filepath.Join(dir, "p", "style.css")
	if err := os.MkdirAll(filepath.Dir(css), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(css, []byte(`a { color: red }`), 0o644); err != nil {
		t.Fatal(err)
	}
	w := &trail{}
	w.add(step{tool: "file_write", output: "Created " + css + " (16 bytes)."})
	if ends := stillOpen(context.Background(), w); len(ends) != 0 {
		t.Errorf("judged a stylesheet: %v", ends)
	}
}

// A step she wrote down and never settled must stop the answer, whatever else
// happened. This is the domain-general half of the gate: a dead link only exists
// on a web page, but an unfinished step exists on research, a multi-part
// question, a task with four items in it.
func TestAnUnfinishedPlanStepHoldsTheAnswer(t *testing.T) {
	plan := skills.NewPlan()
	plan.Set([]string{
		"search for three suppliers",
		"compare their pricing",
		"write up the recommendation",
	})
	if err := plan.Mark(1, skills.StepDone, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := plan.Mark(2, skills.StepDoing, "", ""); err != nil {
		t.Fatal(err)
	}
	ctx := skills.WithScope(context.Background(),
		skills.NewScopeWithPlan(skills.NewWorkspace(t.TempDir()), plan))

	ends := stillOpen(ctx, &trail{})
	if len(ends) != 2 {
		t.Fatalf("want the started-not-finished step and the untouched one, got %d: %v",
			len(ends), ends)
	}
	if !strings.Contains(ends[0], "compare their pricing") ||
		!strings.Contains(ends[0], "started, not finished") {
		t.Errorf("the in-progress step is not described usefully: %q", ends[0])
	}
	if !strings.Contains(ends[1], "write up the recommendation") {
		t.Errorf("the untouched step is missing: %q", ends[1])
	}

	// Settling them — including honestly dropping one — clears the gate.
	if err := plan.Mark(2, skills.StepDone, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := plan.Mark(3, skills.StepDropped, "they only wanted the shortlist", ""); err != nil {
		t.Fatal(err)
	}
	if ends := stillOpen(ctx, &trail{}); len(ends) != 0 {
		t.Errorf("a settled plan still blocks the answer: %v", ends)
	}
}

// Citing a page that was only ever a search result.
//
// harvest() records every URL that appears in any tool output, so a hit in a
// result list is "seen". A source is not a search hit — it is a title and two
// lines of snippet — and the whole point of the second record is telling them
// apart.
func TestASearchHitIsNotASourceSheRead(t *testing.T) {
	ws := skills.NewWorkspace(t.TempDir())
	scope := skills.NewScope(ws, "", "")
	ctx := skills.WithScope(context.Background(), scope)

	// She searched, saw three results, and opened one of them.
	scope.Ledger().ObserveText("1. https://example.com/opened\n2. https://example.com/glimpsed")
	scope.Ledger().Retrieved("https://example.com/opened")

	w := &trail{}
	w.add(step{tool: "web_search", output: "1. https://example.com/opened\n2. https://example.com/glimpsed"})
	w.add(step{tool: "web_fetch", output: "…the page text…"})

	reply := "Two thirds of them do this (https://example.com/opened), and margins have " +
		"narrowed since (https://example.com/glimpsed)."

	bad := unopenedSources(ctx, reply, w)
	if len(bad) != 1 {
		t.Fatalf("want only the unopened one, got %d: %v", len(bad), bad)
	}
	if !strings.Contains(bad[0], "glimpsed") {
		t.Errorf("flagged the wrong URL: %q", bad[0])
	}
	if !strings.Contains(sourcesBrief(bad), "search results is not reading it") {
		t.Error("the push does not explain the distinction it is enforcing")
	}
}

// Trailing punctuation is writing, not part of the address. A URL at the end of
// a sentence must still match the fetch that retrieved it.
func TestAFullStopDoesNotMakeACitationUnopened(t *testing.T) {
	ws := skills.NewWorkspace(t.TempDir())
	scope := skills.NewScope(ws, "", "")
	ctx := skills.WithScope(context.Background(), scope)
	scope.Ledger().Retrieved("https://www.example.com/report/")

	w := &trail{}
	w.add(step{tool: "web_fetch", output: "…"})

	for _, written := range []string{
		"See https://www.example.com/report/.",
		"See https://example.com/report,",
		"See (http://www.example.com/report)",
		"See https://example.com/report#findings",
	} {
		if bad := unopenedSources(ctx, written, w); len(bad) != 0 {
			t.Errorf("%q was called an unopened source: %v", written, bad)
		}
	}
}

// An answer that did no reading is not accused of anything. The URL might have
// come from the user, from memory, or from a page she read yesterday.
func TestNoReadingThisTurnMeansNoAccusation(t *testing.T) {
	ws := skills.NewWorkspace(t.TempDir())
	ctx := skills.WithScope(context.Background(), skills.NewScope(ws, "", ""))

	w := &trail{}
	w.add(step{tool: "memory_recall", output: "you bookmarked https://example.com/thing"})

	if bad := unopenedSources(ctx, "It's at https://example.com/thing.", w); len(bad) != 0 {
		t.Errorf("accused an answer that did no research: %v", bad)
	}
}

// The failure the citation check could not see, because there was nothing to
// cite.
//
// Asked to compare the three most popular UK family cargo bikes and pick one,
// she ran two searches, opened nothing, and wrote several hundred words naming
// suspension layouts and implying prices. Zero URLs in the answer, so
// unopenedSources had no work to do — and that is the worse shape, because there
// is nothing for the reader to check either.
func TestALongAnswerBuiltOnSnippetsAloneIsCaught(t *testing.T) {
	long := strings.Repeat("The Tern GSD parks vertically and the Load has full suspension. ", 14)
	if len(long) <= snippetAnswerFloor {
		t.Fatalf("precondition: the fixture must be long enough, got %d", len(long))
	}

	searchedOnly := &trail{}
	searchedOnly.add(step{tool: "web_search", output: "1. …\n2. …"})
	searchedOnly.add(step{tool: "web_search", output: "1. …\n2. …"})
	if !snippetsOnly(long, searchedOnly) {
		t.Error("two searches, nothing opened, a long answer — and it passed")
	}

	// Opening even one page takes it out of scope: the citation check owns the
	// question of whether she opened the RIGHT ones.
	andRead := &trail{}
	andRead.add(step{tool: "web_search", output: "…"})
	andRead.add(step{tool: "web_fetch", output: "…the page…"})
	if snippetsOnly(long, andRead) {
		t.Error("fired even though she read a page")
	}
}

// A quick lookup answered from its own result line is normal and must stay
// silent, or the push becomes noise and she learns to write past it.
func TestAShortLookupIsLeftAlone(t *testing.T) {
	w := &trail{}
	w.add(step{tool: "web_search", output: "Kick-off is 20:00 BST."})
	if snippetsOnly("Kick-off is at 8pm tonight.", w) {
		t.Error("a one-line answer to a one-line question was treated as shallow research")
	}
}

// And an answer with no web work at all is never in scope, whatever its length.
func TestAnAnswerWithNoSearchingIsNotShallowResearch(t *testing.T) {
	long := strings.Repeat("Here is a long explanation from what I already know. ", 20)
	w := &trail{}
	w.add(step{tool: "memory_recall", output: "…"})
	if snippetsOnly(long, w) {
		t.Error("accused an answer that never claimed to be research")
	}
}
