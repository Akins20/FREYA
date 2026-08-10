package skills

import (
	"context"
	"strings"
	"testing"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/llm"
)

func fullRegistry(t *testing.T) *Registry {
	t.Helper()
	r := New()
	g := guard.New(func(context.Context, guard.Action, guard.Assessment) bool { return true }, nil)
	RegisterSystem(r)
	RegisterShell(r, g)
	RegisterBrowser(r, g, NewTabs())
	return r
}

// The whole safety argument. A kit that guesses wrong must cost a round, never a
// capability: she cannot ask for a tool she was never shown, so a filtered-out
// tool would be a task quietly not done, with no error and nothing in telemetry.
func TestAToolNotOfferedStillRuns(t *testing.T) {
	r := New()
	ran := false
	r.Register(Skill{
		Tool: llm.Tool{Name: "voice_adjust", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) {
			ran = true
			return "adjusted", nil
		},
	})

	// Routed for browsing, so a voice tool is not offered.
	scope := NewScope(NewWorkspace(t.TempDir()), "", "").WithKits([]Kit{KitCore, KitBrowsing})
	ctx := WithScope(context.Background(), scope)

	if r.Offered("voice_adjust", []Kit{KitCore, KitBrowsing}) {
		t.Fatal("precondition: voice_adjust should not be offered for browsing")
	}
	out, err := r.Execute(ctx, "voice_adjust", nil)
	if err != nil {
		t.Fatalf("a tool she named was refused because it was not offered: %v", err)
	}
	if !ran || out != "adjusted" {
		t.Errorf("the tool did not actually run: ran=%v out=%q", ran, out)
	}

	// And the miss is recorded, because that is the number that says whether
	// narrowing is earning its keep.
	misses := r.Misses()
	if len(misses) != 1 || !strings.Contains(misses[0], "voice_adjust") {
		t.Errorf("the miss was not recorded: %v", misses)
	}
}

// A tool that WAS offered must not be counted as a miss, or the number that
// justifies the whole design becomes noise.
func TestAnOfferedToolIsNotAMiss(t *testing.T) {
	r := New()
	r.Register(Skill{
		Tool:    llm.Tool{Name: "browser_read", Params: llm.ObjectSchema(nil)},
		Handler: func(context.Context, map[string]any) (string, error) { return "page", nil },
	})
	scope := NewScope(NewWorkspace(t.TempDir()), "", "").WithKits([]Kit{KitCore, KitBrowsing})
	if _, err := r.Execute(WithScope(context.Background(), scope), "browser_read", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(r.Misses()); n != 0 {
		t.Errorf("an offered tool was counted as a miss: %v", r.Misses())
	}
}

// Every caller that has not adopted routing keeps every tool. Narrowing is the
// one change here that fails silently, so it must be opted into.
func TestNoRoutingMeansEverything(t *testing.T) {
	r := fullRegistry(t)
	all := r.Tools()
	if got := r.ToolsFor(nil); len(got) != len(all) {
		t.Errorf("an unrouted caller was given %d tools of %d", len(got), len(all))
	}
	// And an unrouted scope records no misses.
	scope := NewScope(NewWorkspace(t.TempDir()), "", "")
	if !scope.offers("anything_at_all") {
		t.Error("an unrouted scope treated a tool as unoffered")
	}
}

// Routing must narrow, or it buys nothing.
func TestRoutingActuallyNarrows(t *testing.T) {
	r := fullRegistry(t)
	all := len(r.Tools())
	browsing := len(r.ToolsFor([]Kit{KitCore, KitBrowsing}))
	if browsing >= all {
		t.Fatalf("routing offered %d of %d tools — it narrows nothing", browsing, all)
	}
	t.Logf("full %d, browsing kit %d (%.0f%% fewer)", all, browsing,
		100*float64(all-browsing)/float64(all))
}

// The order is part of the cached prefix. Making it depend on map iteration
// would reshuffle it every process and cost the cache on every first request.
func TestKitToolsAreSorted(t *testing.T) {
	r := fullRegistry(t)
	tools := r.ToolsFor([]Kit{KitCore, KitBrowsing, KitFiles})
	for i := 1; i < len(tools); i++ {
		if tools[i-1].Name > tools[i].Name {
			t.Fatalf("tools out of order at %d: %q then %q", i, tools[i-1].Name, tools[i].Name)
		}
	}
}

// The requests that actually arrive, and the kit each one needs.
func TestRoutingCoversRealRequests(t *testing.T) {
	cases := []struct {
		request string
		want    Kit
	}{
		{"open my portal and do the quizzes", KitBrowsing},
		{"download the two pictures from my drive", KitBrowsing},
		{"sign in to my school account", KitBrowsing},
		{"write up my notes into a document", KitFiles},
		{"back up my photos folder", KitFiles},
		{"why is the build failing in that repo", KitDev},
		{"commit this and run the tests", KitDev},
		{"speak a bit slower", KitVoice},
		{"how much disk have I got left", KitAdmin},
		{"what have we spent on the api", KitAdmin},
	}
	for _, c := range cases {
		got := Route(c.request)
		found := false
		for _, k := range got {
			if k == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q routed to %v, missing %s", c.request, got, c.want)
		}
		// Core is always there, or she cannot even start.
		core := false
		for _, k := range got {
			if k == KitCore {
				core = true
			}
		}
		if !core {
			t.Errorf("%q routed without the core kit: %v", c.request, got)
		}
	}
}

// Ambiguity resolves towards MORE. A tool she was not offered is a task quietly
// not done; a tool she does not need is a few hundred tokens she ignores.
func TestNothingRecognisedOffersEverything(t *testing.T) {
	for _, vague := range []string{
		"hello", "what do you think", "how are you", "thanks", "",
	} {
		got := Route(vague)
		if len(got) != len(AllKits()) {
			t.Errorf("%q routed to %v — an unrecognised request must get everything",
				vague, got)
		}
	}
}

// A request spanning two areas gets both, rather than a guess between them.
func TestAMixedRequestGetsBothKits(t *testing.T) {
	got := Route("download the invoice from the portal and save it to my documents folder")
	var browsing, files bool
	for _, k := range got {
		if k == KitBrowsing {
			browsing = true
		}
		if k == KitFiles {
			files = true
		}
	}
	if !browsing || !files {
		t.Errorf("a mixed request routed to %v, wanted both browsing and files", got)
	}
}

// A tool nobody classified must stay reachable. Forgetting to classify something
// has to fail towards offering it.
func TestAnUnclassifiedToolLandsInCore(t *testing.T) {
	if got := kitOf("some_brand_new_thing"); got != KitCore {
		t.Errorf("an unclassified tool went to %s, not core — it would be unreachable "+
			"for most requests", got)
	}
}

// Superseded by TestTheToolsSheUsesMostAreReallyInCore in coretools_test.go.
//
// The version that lived here asserted kitOf(name) == KitCore for a list of
// names — which passes for ANY unknown string, because an unclassified tool
// falls through to core by design. So it passed for read_file, write_file and
// list_dir, none of which is a tool, while the real file_read, file_write and
// folder_list sat in the files kit and were invisible on most requests. A test
// that cannot fail is worse than no test, because it reads as coverage.
