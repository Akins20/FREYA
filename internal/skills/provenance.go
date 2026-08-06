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
	mu      sync.Mutex
	seen    map[IDKind]map[string]bool
	guesses int
}

// BeginExchange restores the guess budget for a new request, while keeping
// everything she has been shown.
//
// The two halves of this ledger have opposite lifetimes, and conflating them
// reintroduced the exact bug it exists to prevent. What she has SEEN is knowledge
// and must accumulate: a URL read an hour ago is still a URL she read. The
// BUDGET is about one thread of work going wrong — forty rounds walking ids — and
// a request that has not started yet cannot be doing that.
//
// Left process-wide, the budget was spent within minutes of the daemon starting
// and never came back, so every later reconstruction was refused on the strength
// of two guesses made in an unrelated conversation. That is the same stall as
// before, arriving slowly instead of at once.
func (l *Ledger) BeginExchange() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.guesses = 0
	l.mu.Unlock()
}

// spendGuess counts an unobserved id-carrying address and returns the running
// total for this exchange.
func (l *Ledger) spendGuess() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.guesses++
	return l.guesses
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

// composedURL reports whether a URL carries identifiers she could only have
// invented — and is deliberately narrow, because the first version of this rule
// was not and it did real damage.
//
// That version treated any path of more than one segment as composed, which
// refused her "/d2l/home" — a portal's front door, no parameters at all. She
// could not get in, wandered onto a different domain looking for another way,
// and lost the session. A guard that blocks ordinary navigation is worse than
// the disease it treats.
//
// So: path depth is not evidence of anything. Plenty of real pages are deep, and
// a link she follows can land anywhere. What she cannot invent is a QUERY
// PARAMETER carrying an id — qi=9603, ou=8359 — because those come from the
// page or they come from nowhere. Numbers are the tell: a numeric parameter is
// an identifier, and identifiers are the thing she walks by pattern. A query of
// words (a search, a language, a filter) is composable and always allowed.
func composedURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.RawQuery == "" {
		return false
	}
	for name, values := range u.Query() {
		if composableParam[strings.ToLower(name)] {
			continue // pagination and sizing are things anyone works out for themselves
		}
		for _, v := range values {
			if v == "" {
				continue
			}
			numeric := true
			for _, r := range v {
				if r < '0' || r > '9' {
					numeric = false
					break
				}
			}
			if numeric {
				return true
			}
		}
	}
	return false
}

// composableParam names the numeric parameters a person legitimately reasons
// about. Walking pages is navigation; walking record ids is guessing.
var composableParam = map[string]bool{
	"page": true, "p": true, "offset": true, "start": true,
	"limit": true, "size": true, "per_page": true, "perpage": true,
	"count": true, "n": true, "rows": true, "pagesize": true,
}

// guessBudget is how many unobserved id-carrying URLs may be tried in one
// exchange before the next is refused. Reset per exchange by BeginExchange.
//
// Not zero, and that is the lesson. A hard refusal on the first attempt blocked
// her from a portal's own home page and cost a session; sometimes a URL is
// legitimately reconstructed, and being wrong once is cheap. What is not cheap is
// being wrong forty times in a row, which is what actually happened. So the first
// two are allowed with the truth attached, and the pattern-walk is stopped.
const guessBudget = 2

// CheckURL judges a URL against what she has actually been shown.
//
// It returns a note to attach when the URL carries ids she was never given — a
// warning, not a veto — and an error only once she has spent the budget, because
// by then it is a pattern-walk rather than a reconstruction.
func CheckURL(ctx context.Context, raw string) (note string, err error) {
	led := ScopeFrom(ctx).Ledger()
	if led == nil || raw == "" {
		return "", nil
	}
	if !composedURL(raw) || led.Seen(IDURL, raw) {
		return "", nil
	}
	// Tolerate a trailing slash or a fragment difference.
	trimmed := strings.TrimRight(strings.SplitN(raw, "#", 2)[0], "/")
	if led.Seen(IDURL, trimmed) || led.Seen(IDURL, trimmed+"/") {
		return "", nil
	}

	if led.spendGuess() > guessBudget {
		e := fmt.Errorf("refusing %q: that is the %drd address with invented ids in this exchange, "+
			"which is a pattern-walk, not navigation. A page loading proves nothing — a wrong id returns "+
			"a real page with a real title, which is how forty rounds get spent reaching nothing. Read a "+
			"page and follow a link on it", raw, guessBudget+1)
		if known := led.Known(IDURL, 10); len(known) > 0 {
			return "", withOptions(e, known)
		}
		return "", e
	}

	return "\n(Heads up: the ids in that address were not on any page you have read, so this is a " +
		"reconstruction. If it is not the page you expected, go back and follow a link rather than " +
		"adjusting the numbers.)", nil
}
