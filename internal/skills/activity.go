package skills

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ActivityTracker keeps a live, lightweight read of what the user is doing. It is
// sampled frequently in the background so context is fresh and a short trail of
// recent windows accumulates — a browser page, a terminal's directory, a folder,
// a file being edited. It never speaks; it exists so proactive behaviour (a
// follow-up, a decision about whether to act) can be grounded in what is actually
// on screen rather than guessing.
type ActivityTracker struct {
	// Deepen, when set, looks closer at a browser or folder window that just
	// changed and returns a richer one-line read of its actual content (the page,
	// the folder's files) — beyond what the title alone says. It is only called on
	// a change and only for those two kinds, so the heavier work (a screenshot and
	// a vision call) is bounded. Empty return falls back to the title.
	Deepen func(kind, hint string) string

	mu     sync.Mutex
	recent []actState
}

type actState struct {
	at   time.Time
	kind string // browser | terminal | files | editor | other
	// base is the title-derived content, used to detect a genuine change; detail
	// is what's shown, which may be the deeper vision read.
	base   string
	detail string
}

const maxActivityTrail = 6

// Sample captures the focused window now, classifies it, and records it only when
// it differs from the last — so a trail of genuine switches builds rather than a
// column of the same window. For a browser or folder that just changed, it looks
// deeper (Deepen) to read the actual content, not just the title.
func (t *ActivityTracker) Sample() {
	title := activityField("getwindowname")
	if title == "" {
		return // no display, no tool, or nothing focused
	}
	kind, base := classifyActivity(strings.ToLower(activityField("getwindowclassname")), title)

	// Change detection uses the stable title-derived base, not the deep read
	// (which can vary run to run for the same page).
	t.mu.Lock()
	if n := len(t.recent); n > 0 && t.recent[n-1].kind == kind && t.recent[n-1].base == base {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	detail := base
	if t.Deepen != nil && (kind == "browser" || kind == "files") {
		if deep := strings.TrimSpace(t.Deepen(kind, base)); deep != "" {
			detail = deep
		}
	}

	t.mu.Lock()
	t.recent = append(t.recent, actState{at: time.Now(), kind: kind, base: base, detail: detail})
	if len(t.recent) > maxActivityTrail {
		t.recent = t.recent[len(t.recent)-maxActivityTrail:]
	}
	t.mu.Unlock()
}

// Current renders what they're doing now, with a short trail of what came just
// before — the shape a proactive check wants for context.
func (t *ActivityTracker) Current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.recent) == 0 {
		return ""
	}
	out := "right now: " + phraseActivity(t.recent[len(t.recent)-1])
	var trail []string
	for i := len(t.recent) - 2; i >= 0 && len(trail) < 3; i-- {
		trail = append(trail, phraseActivity(t.recent[i]))
	}
	if len(trail) > 0 {
		out += "; recently: " + strings.Join(trail, ", ")
	}
	return out
}

func phraseActivity(s actState) string {
	switch s.kind {
	case "browser":
		return "browsing " + s.detail
	case "terminal":
		return "in a terminal — " + s.detail
	case "files":
		return "in the file manager — " + s.detail
	case "editor":
		return "editing " + s.detail
	default:
		return s.detail
	}
}

// classifyActivity buckets a window by its application and pulls the meaningful
// content out of its title: the page for a browser, the path or command for a
// terminal, the folder for a file manager, the file for an editor. It reads the
// window class when available and falls back to the app name in the title
// (Chrome sometimes reports no class), so it stays reliable either way. Browser
// is checked first, so a page *about* terminals is still a browser page.
func classifyActivity(class, title string) (kind, detail string) {
	lt := strings.ToLower(title)
	switch {
	case containsAnyFold(class, "chrome", "chromium", "firefox", "brave", "edge", "webkit", "vivaldi"),
		containsAnyFold(lt, "google chrome", "chromium", "mozilla firefox", "microsoft edge", "brave", "vivaldi"):
		return "browser", trimAppSuffix(title)
	case containsAnyFold(class, "terminal", "xterm", "konsole", "kitty", "alacritty", "tilix", "st-256"),
		containsAnyFold(lt, "terminal"):
		return "terminal", title
	case containsAnyFold(class, "thunar", "nautilus", "nemo", "dolphin", "pcmanfm", "caja", "files"),
		containsAnyFold(lt, "file manager"):
		return "files", title
	case containsAnyFold(class, "code", "codium", "sublime", "gedit", "kate", "gvim", "jetbrains", "intellij", "pycharm", "goland"),
		containsAnyFold(lt, "visual studio code", "sublime text"):
		return "editor", trimAppSuffix(title)
	default:
		return "other", title
	}
}

// trimAppSuffix drops a trailing " - App Name" so a browser or editor title
// reads as the page or file, not the program. Handles both separators windows
// use — a hyphen (Chrome, VS Code) and an em-dash (Firefox) — at whichever sits
// rightmost, so a page title that itself contains a hyphen survives intact.
func trimAppSuffix(title string) string {
	cut := -1
	for _, sep := range []string{" - ", " — "} {
		if i := strings.LastIndex(title, sep); i > cut {
			cut = i
		}
	}
	if cut > 0 {
		return strings.TrimSpace(title[:cut])
	}
	return title
}

func containsAnyFold(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// CurrentActivity is a one-shot read of the focused window, for callers that want
// the current context without a running tracker.
func CurrentActivity() string {
	title := activityField("getwindowname")
	if title == "" {
		return ""
	}
	if len(title) > 120 {
		title = title[:120] + "…"
	}
	kind, detail := classifyActivity(strings.ToLower(activityField("getwindowclassname")), title)
	return phraseActivity(actState{kind: kind, detail: detail})
}

// activityField asks xdotool one thing about the active window.
func activityField(what string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xdotool", "getactivewindow", what).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
