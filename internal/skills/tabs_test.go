package skills

import "testing"

// The blank/unknown-name fallback matters: models routinely omit the tab name,
// and erroring instead of using the obvious open tab sent her into a loop of
// reopening (reloading) the page every round. These pin the resolution.
func TestTabsGetFallback(t *testing.T) {
	tabs := NewTabs()
	tabs.put(&openTab{name: "uni_portal"})

	// Blank name with one tab open -> that tab.
	if tab, ok := tabs.get(""); !ok || tab.name != "uni_portal" {
		t.Fatalf("blank name should resolve to the only tab, got ok=%v", ok)
	}
	// Unknown name with one tab open -> that tab (no real ambiguity).
	if tab, ok := tabs.get("whatever"); !ok || tab.name != "uni_portal" {
		t.Fatalf("unknown name with one tab should resolve to it, got ok=%v", ok)
	}

	// Two tabs: blank name resolves to the most recently opened/used.
	tabs.put(&openTab{name: "second"})
	if tab, ok := tabs.get(""); !ok || tab.name != "second" {
		t.Fatalf("blank name should resolve to the last-active tab, got %v ok=%v", tab, ok)
	}
	// An exact name still wins, and updates last-active.
	if tab, ok := tabs.get("uni_portal"); !ok || tab.name != "uni_portal" {
		t.Fatalf("exact name should win, got ok=%v", ok)
	}
	if tab, ok := tabs.get(""); !ok || tab.name != "uni_portal" {
		t.Fatalf("blank name should now follow the last-used tab, got %v ok=%v", tab, ok)
	}
}
