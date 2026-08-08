package skills

import (
	"context"
	"strings"
	"testing"
)

// The whole point. A tool missing from the catalogue is a capability she cannot
// know she has — which is exactly how browser_sync_logins sat unused for a
// hundred sessions while she told the user signing in was impossible.
func TestTheCatalogueNamesEveryTool(t *testing.T) {
	r := fullRegistry(t)
	RegisterFinder(r)
	cat := r.Catalogue()

	for _, name := range r.Names() {
		if !strings.Contains(cat, name) {
			t.Errorf("%s is registered but not in the catalogue — she has no way to "+
				"learn it exists", name)
		}
	}
}

// The catalogue leads the cached prefix, so identical registries must produce
// identical bytes. Deriving it from map iteration would reshuffle it every
// process start and cost a cold prefix on the first request of every run.
func TestTheCatalogueIsByteStable(t *testing.T) {
	r := fullRegistry(t)
	first := r.Catalogue()
	for i := 0; i < 8; i++ {
		if got := r.Catalogue(); got != first {
			t.Fatalf("catalogue changed between calls at %d — the prefix would never cache", i)
		}
	}
}

// Naming everything must stay cheap, or the argument for doing it collapses.
// Full declarations were measured at ~11,600 tokens; the catalogue has to be a
// small fraction of that or we may as well declare them all.
func TestTheCatalogueIsCheaperThanDeclaringEverything(t *testing.T) {
	r := fullRegistry(t)
	RegisterFinder(r)

	catChars := len(r.Catalogue())
	var declChars int
	for _, tool := range r.Tools() {
		declChars += len(tool.Name) + len(tool.Description)
		for k, p := range tool.Params.Properties {
			declChars += len(k) + len(p.Type) + len(p.Description)
		}
	}
	if catChars*3 > declChars {
		t.Errorf("the catalogue is %d chars against %d for full declarations — "+
			"less than a 3× saving does not justify carrying both", catChars, declChars)
	}
	t.Logf("catalogue ~%d tokens, full declarations ~%d tokens (%.0f%% of the cost)",
		catChars/4, declChars/4, 100*float64(catChars)/float64(declChars))
}

// find_tools has to actually retrieve the tool, from a description of the WORK
// rather than the tool's own name. Every case here is a failure that really
// happened, phrased the way she would think about it at the time.
func TestFindToolsRetrievesTheToolTheTaskNeeds(t *testing.T) {
	r := fullRegistry(t)
	RegisterFinder(r)

	cases := []struct {
		task string
		want string
		why  string
	}{
		{"download a file when there is no download button on the page",
			"browser_right_click", "the Drive failure: forty rounds, no route to Download"},
		{"check whether a download finished or is still going",
			"browser_downloads", "clicking again was the trap"},
		{"attach a file from my disk to a form on the page",
			"browser_upload", "the OS file chooser cannot be driven"},
		{"keep a copy of this receipt page, it has no download link",
			"browser_save_pdf", "'keep this' for a non-file had no answer at all"},
		{"chrome keeps filling the wrong saved password and I cannot type it myself",
			"browser_sync_logins", "existed a hundred sessions, called zero times"},
		{"scroll a chat panel that has its own scrollbar",
			"browser_scroll_within", "she concluded she had seen every message"},
		{"select two files at once so I can act on both",
			"browser_select_also", "two files is one action"},
	}

	for _, c := range cases {
		found := r.find(c.task, 5)
		var names []string
		hit := false
		for _, tool := range found {
			names = append(names, tool.Name)
			if tool.Name == c.want {
				hit = true
			}
		}
		if !hit {
			t.Errorf("%q did not surface %s (got %v) — %s", c.task, c.want, names, c.why)
			continue
		}
		t.Logf("%-56.56q → %v", c.task, names)
	}
}

// A tool find_tools surfaced must become callable, or the whole mechanism hands
// back a schema for something she still cannot invoke.
func TestASurfacedToolBecomesOffered(t *testing.T) {
	r := fullRegistry(t)
	RegisterFinder(r)

	scope := NewScope(NewWorkspace(t.TempDir()), "", "").WithKits([]Kit{KitCore, KitVoice})
	ctx := WithScope(context.Background(), scope)

	if scope.offers("browser_right_click") {
		t.Fatal("precondition: a browser tool should not be offered to a voice kit")
	}

	out, err := r.Execute(ctx, "find_tools",
		map[string]any{"task": "download a file with no download button"})
	if err != nil {
		t.Fatalf("find_tools failed: %v", err)
	}
	if !strings.Contains(out, "browser_right_click") {
		t.Fatalf("find_tools did not surface the tool:\n%s", out)
	}
	if !scope.offers("browser_right_click") {
		t.Error("the tool was surfaced but is still not offered — she cannot call it")
	}

	// And it joins the declarations the next round will send.
	tools := r.ToolsWith([]Kit{KitCore, KitVoice}, scope.Surfaced())
	var declared bool
	for _, tool := range tools {
		if tool.Name == "browser_right_click" {
			declared = true
		}
	}
	if !declared {
		t.Error("the surfaced tool is not in the declarations, so the provider will " +
			"never emit a call for it")
	}
}

// Promotion must add, never remove: a kit tool that was already offered has to
// survive the rebuild.
func TestPromotionOnlyAdds(t *testing.T) {
	r := fullRegistry(t)
	kits := []Kit{KitCore, KitBrowsing}
	before := r.ToolsFor(kits)
	after := r.ToolsWith(kits, []string{"voice_adjust"})

	if len(after) < len(before) {
		t.Fatalf("promotion shrank the set: %d → %d", len(before), len(after))
	}
	have := map[string]bool{}
	for _, tool := range after {
		have[tool.Name] = true
	}
	for _, tool := range before {
		if !have[tool.Name] {
			t.Errorf("%s was offered before promotion and is gone after it", tool.Name)
		}
	}
}

// The declaration she reads must name the argument KEYS. A value filed under the
// wrong key was fourteen consecutive identical failures once; the cheapest cure
// is telling her what the keys are.
func TestAFoundToolShowsItsArgumentKeys(t *testing.T) {
	r := fullRegistry(t)
	RegisterFinder(r)

	out, err := r.Execute(WithScope(context.Background(),
		NewScope(NewWorkspace(t.TempDir()), "", "")),
		"find_tools", map[string]any{"task": "search this long page for a phrase"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "phrase:") || !strings.Contains(out, "required") {
		t.Errorf("the result does not spell out the arguments:\n%s", out)
	}
}

// Rarity weighting is what makes the search usable. "browser" appears in forty
// descriptions and discriminates nothing; without the weight, a query mentioning
// it would rank the whole family above the one tool that solves the problem.
func TestACommonWordDoesNotDrownTheRareOne(t *testing.T) {
	r := fullRegistry(t)
	found := r.find("browser page file chooser", 3)
	if len(found) == 0 {
		t.Fatal("no results at all")
	}
	if found[0].Name != "browser_upload" {
		var names []string
		for _, tool := range found {
			names = append(names, tool.Name)
		}
		t.Errorf("ranked %v — 'chooser' is the rare word and should decide it", names)
	}
}

// gist has to survive the description styles actually in the tree.
func TestGistTakesTheFirstUsefulClause(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Save the current page as a PDF. This is how you keep something that is not a file.",
			"Save the current page as a PDF"},
		{"What is downloading right now — with progress and where each file landed.",
			"What is downloading right now"},
		{"", "(no description)"},
	}
	for _, c := range cases {
		if got := gist(c.in); got != c.want {
			t.Errorf("gist(%.40q) = %q, want %q", c.in, got, c.want)
		}
	}
}
