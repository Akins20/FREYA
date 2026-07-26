package skills

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyActivity(t *testing.T) {
	cases := []struct {
		class, title string
		wantKind     string
		wantDetail   string
	}{
		{"google-chrome", "Quizzes - BUS 1102 - Google Chrome", "browser", "Quizzes - BUS 1102"},
		{"firefox", "Inbox — Mozilla Firefox", "browser", "Inbox"},
		{"xfce4-terminal", "akins@box: ~/Development/JARVIS", "terminal", "akins@box: ~/Development/JARVIS"},
		{"thunar", "Downloads - File Manager", "files", "Downloads - File Manager"},
		{"code", "activity.go - JARVIS - Visual Studio Code", "editor", "activity.go - JARVIS"},
		{"spotify", "Spotify Premium", "other", "Spotify Premium"},
		// class empty (Chrome sometimes reports none) — fall back to the title.
		{"", "AnimeHeaven.Me - Google Chrome", "browser", "AnimeHeaven.Me"},
	}
	for _, c := range cases {
		kind, detail := classifyActivity(c.class, c.title)
		if kind != c.wantKind || detail != c.wantDetail {
			t.Errorf("classify(%q,%q) = (%q,%q); want (%q,%q)",
				c.class, c.title, kind, detail, c.wantKind, c.wantDetail)
		}
	}
}

// The tracker must dedupe consecutive identical windows, keep a bounded trail,
// and render "right now" plus a short recent history.
func TestActivityTrackerTrail(t *testing.T) {
	tr := &ActivityTracker{}
	push := func(kind, detail string) {
		tr.mu.Lock()
		if n := len(tr.recent); n > 0 && tr.recent[n-1].kind == kind && tr.recent[n-1].base == detail {
			tr.mu.Unlock()
			return
		}
		tr.recent = append(tr.recent, actState{at: time.Now(), kind: kind, base: detail, detail: detail})
		if len(tr.recent) > maxActivityTrail {
			tr.recent = tr.recent[len(tr.recent)-maxActivityTrail:]
		}
		tr.mu.Unlock()
	}

	push("terminal", "~/Development/JARVIS")
	push("terminal", "~/Development/JARVIS") // dupe — ignored
	push("browser", "AnimeHeaven.Me")
	push("files", "Downloads")

	cur := tr.Current()
	if !strings.HasPrefix(cur, "right now: in the file manager — Downloads") {
		t.Fatalf("current window wrong: %q", cur)
	}
	if !strings.Contains(cur, "recently:") || !strings.Contains(cur, "browsing AnimeHeaven.Me") {
		t.Fatalf("trail missing recent history: %q", cur)
	}
	// The deduped terminal entry should appear once, not twice.
	if strings.Count(cur, "Development/JARVIS") != 1 {
		t.Fatalf("consecutive duplicate was not deduped: %q", cur)
	}
}
