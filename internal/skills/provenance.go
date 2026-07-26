package skills

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// What she has actually been shown, as distinct from what she can compose.
//
// # The disease this exists to stop
//
// She fails by fabricating identifiers she cannot know. Two forms, and the
// difference between them is everything:
//
//   - A fabricated CSS selector FAILS LOUDLY — "no element matches" — so she gets
//     a signal, and a circuit breaker can act on it.
//   - A fabricated URL SUCCEEDS. She walked quiz ids by pattern —
//     quiz_summary.d2l?qi=9600, then 9603, then 9604, then 10130 — and every one
//     returned HTTP 200 with a real page and a real title. Nothing ever told her
//     she was off the rails, so she spent forty rounds of apparent progress
//     reaching nothing, and at one point spliced a course id remembered from a
//     different course onto a quiz id from this one.
//
// A guess that fails is survivable. A guess that succeeds is not, because it
// looks exactly like work.
//
// An audit of all ~106 tools found twenty where this happens: URLs, tab names,
// directory paths, place names, terminal session names, Claude session ids, fact
// keys. The common shape is that the tool accepts an identifier and has no idea
// whether she saw it or invented it.
//
// # The distinction that makes this safe
//
// Referencing and minting are different acts. Naming a NEW file, a NEW tab, a NEW
// terminal session is legitimate composition — the identifier cannot have been
// observed, because it does not exist yet. Referencing an EXISTING thing she was
// never shown is a guess. The ledger only ever constrains the second.
type Ledger struct {
	mu   sync.Mutex
	seen map[IDKind]map[string]bool
}

// IDKind separates the namespaces, so a path cannot vouch for a URL.
type IDKind string

const (
	IDURL  IDKind = "url"
	IDPath IDKind = "path"
	IDName IDKind = "name" // tabs, terminal sessions, places, session ids
)

// NewLedger creates an empty ledger.
func NewLedger() *Ledger { return &Ledger{seen: map[IDKind]map[string]bool{}} }

// Observe records identifiers that a tool actually showed her.
func (l *Ledger) Observe(kind IDKind, values ...string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[kind] == nil {
		l.seen[kind] = map[string]bool{}
	}
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			l.seen[kind][v] = true
		}
	}
}

// Seen reports whether an identifier was surfaced to her.
func (l *Ledger) Seen(kind IDKind, value string) bool {
	if l == nil {
		return true // no ledger, no opinion — never block on missing bookkeeping
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[kind][strings.TrimSpace(value)]
}

// Known lists what has been surfaced, for putting real options in a refusal.
func (l *Ledger) Known(kind IDKind, limit int) []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.seen[kind]))
	for v := range l.seen[kind] {
		out = append(out, v)
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// urlPattern harvests absolute URLs out of whatever a tool printed.
//
// Harvesting from the output is what makes this affordable: rather than
// instrumenting forty producer tools by hand — browser_links, browser_read,
// web_search, browser_history, bookmarks, downloads, every click that reports
// where it landed — the framework reads what was handed to her and records it.
// A tool that shows her a URL has, by definition, shown her that URL.
var urlPattern = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)

// ObserveText records identifiers appearing in arbitrary text — what the user
// said, or what a tool printed.
func (l *Ledger) ObserveText(text string) { l.harvest(text) }

// harvest records every identifier visible in a tool's output.
func (l *Ledger) harvest(text string) {
	if l == nil || text == "" {
		return
	}
	found := urlPattern.FindAllString(text, 200)
	for i, u := range found {
		found[i] = strings.TrimRight(u, ".,;:")
	}
	l.Observe(IDURL, found...)
}

// composedURL reports whether a URL looks composed rather than observed, and is
// the rule that catches the quiz-id walk without blocking ordinary work.
//
// Walking in a site's front door is legitimate: an origin, or an origin with a
// bare path, is something anyone can type and she is meant to be able to explore.
// Teleporting to a deep link she was never shown is not — a path with several
// segments, or any query string, encodes identifiers she cannot know unless the
// page gave them to her. That single distinction blocks the failure and permits
// everything a person would actually do.
func composedURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false // not a URL we can reason about; leave it alone
	}
	if u.RawQuery != "" {
		return true
	}
	segments := 0
	for _, s := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if s != "" {
			segments++
		}
	}
	return segments > 1
}

// CheckURL refuses a deep link she was never shown, and says where the real ones
// are. Returns nil when the URL is observed, shallow, or there is no ledger.
func CheckURL(ctx context.Context, raw string) error {
	led := ScopeFrom(ctx).Ledger()
	if led == nil || raw == "" {
		return nil
	}
	if !composedURL(raw) || led.Seen(IDURL, raw) {
		return nil
	}
	// Tolerate the same URL with a trailing slash or fragment difference.
	trimmed := strings.TrimRight(strings.SplitN(raw, "#", 2)[0], "/")
	if led.Seen(IDURL, trimmed) || led.Seen(IDURL, trimmed+"/") {
		return nil
	}

	err := fmt.Errorf("refusing to open %q: that is a deep link with parameters you have not been "+
		"shown, so it is a guess. A page loading proves nothing — a wrong id returns a real page with a "+
		"real title, which is how a whole session can be spent walking ids that were never right. "+
		"Open the site's front page and follow the links on it, or use one you actually read", raw)

	if known := led.Known(IDURL, 12); len(known) > 0 {
		return withOptions(err, known)
	}
	return err
}
