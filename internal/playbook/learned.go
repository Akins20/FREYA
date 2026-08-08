package playbook

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Procedural memory she can write.
//
// # The gap this closes
//
// The playbooks above are embedded strings, which makes them fast, dependency-
// free and completely unteachable. Everything she knows about how to work is
// whatever was compiled in — so a route she works out the hard way is gone the
// moment the exchange ends. The case that named this: she spent an evening
// failing to sign in at learn.uopeople.edu/d2l/login, the real door was
// my.uopeople.edu, and a human found that by reading her archive days later. She
// had done the work and had nowhere to put the answer.
//
// # Why this is a store and not better retrieval
//
// The original plan for this phase was to distil trails into procedures and
// retrieve them "by task shape rather than vocabulary", because BM25 cannot
// match "how did I work this site last night" — a request with no distinctive
// words in it. That is true and it is the wrong lesson. Claude Code's procedural
// memory does not retrieve at all: a lightweight index of names and one-line
// summaries is simply always present, and the body is fetched on demand. A
// procedure whose name is already in the prompt never has to be found.
//
// So this is the same two-level disclosure the tool catalogue uses: an index she
// always sees, bodies she asks for.
//
// # Where the index goes, and why not next to the embedded one
//
// The embedded index rides in the `skill` tool's DESCRIPTION (skillbook.go),
// which is ideal for it — tool declarations lead the request, so it is cached
// and costs nothing per turn. A learned index cannot live there. It changes the
// moment she learns something, and a tool description that changes is a cached
// prefix that changes: one lesson would re-bill every stable tier behind it for
// the rest of the session.
//
// So the learned index goes in the VOLATILE TAIL instead, after everything
// cached, where a change costs only itself. That also makes it live rather than
// snapshotted at startup — she can learn a procedure and see it in the index on
// the very next turn, which the tool-description route could not have done
// without a restart.

// learnedCap bounds the store.
//
// Consolidation is the unsolved part of agent memory — the research is explicit
// that forgetting is understudied, and Claude Code's answer to it needs human
// review. Learned playbooks accumulate junk by construction: twenty near-
// identical "signed into the portal" entries are worse than one, and they are
// worse in the expensive place, because the index is sent every turn. Until
// there is a real merge pass this is the crude version of forgetting — a cap,
// and the least recently used one goes.
const learnedCap = 60

// Learned is the on-disk store of procedures she worked out herself.
type Learned struct {
	path string

	mu    sync.Mutex
	byID  map[string]Skill
	used  map[string]time.Time
	first map[string]time.Time
	// gone is what consolidation merged away, kept rather than deleted — see
	// consolidate.go for why nothing here is ever destroyed.
	gone []supersededSkill
}

// stored is the file format: live skills, plus the ones a merge replaced.
type stored struct {
	Skills     []storedSkill     `json:"skills"`
	Superseded []supersededSkill `json:"superseded,omitempty"`
}

// supersededSkill is a playbook a consolidation merged away, with a note saying
// what replaced it so a bad merge is recoverable.
type supersededSkill struct {
	Name       string    `json:"name"`
	Summary    string    `json:"summary"`
	Body       string    `json:"body"`
	ReplacedBy string    `json:"replaced_by"`
	ReplacedAt time.Time `json:"replaced_at"`
}

type storedSkill struct {
	Name     string    `json:"name"`
	Summary  string    `json:"summary"`
	Body     string    `json:"body"`
	Learned  time.Time `json:"learned"`
	LastUsed time.Time `json:"last_used"`
}

// OpenLearned loads the store from dir, creating nothing until something is
// learned. A missing or corrupt file is an empty store rather than an error:
// losing what she taught herself is bad, and refusing to start is worse.
func OpenLearned(dir string) (*Learned, error) {
	l := &Learned{
		path:  filepath.Join(dir, "learned.json"),
		byID:  map[string]Skill{},
		used:  map[string]time.Time{},
		first: map[string]time.Time{},
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return l, fmt.Errorf("read learned playbooks: %w", err)
	}
	var s stored
	if err := json.Unmarshal(data, &s); err != nil {
		return l, fmt.Errorf("learned playbooks are corrupt (starting empty): %w", err)
	}
	for _, e := range s.Skills {
		l.byID[e.Name] = Skill{Name: e.Name, Summary: e.Summary, Body: e.Body}
		l.used[e.Name] = e.LastUsed
		l.first[e.Name] = e.Learned
	}
	l.gone = s.Superseded
	return l, nil
}

// Add records a procedure, replacing any earlier one of the same name.
//
// Replacing rather than appending is deliberate: a second attempt at the same
// job usually knows more than the first, and two entries under one name would
// be a choice she has to make on every lookup — the same trap two selector-click
// tools were.
func (l *Learned) Add(s Skill) error {
	s.Name = normaliseName(s.Name)
	if s.Name == "" {
		return fmt.Errorf("a learned skill needs a name")
	}
	if strings.TrimSpace(s.Summary) == "" {
		return fmt.Errorf("a learned skill needs a one-line summary — it is the only " +
			"part that is always in front of you, and it is what tells you when to " +
			"open the rest")
	}
	if strings.TrimSpace(s.Body) == "" {
		return fmt.Errorf("a learned skill needs a body: the ordered steps that worked")
	}
	// An embedded playbook is authored practice and outranks anything worked out
	// in one evening. Shadowing one would silently replace it in every lookup.
	if _, clash := Get(s.Name); clash {
		return fmt.Errorf("%q is already one of your built-in skills; pick another name "+
			"so you do not shadow it", s.Name)
	}

	now := time.Now()
	l.mu.Lock()
	// Re-learning the same thing keeps the original date: it is one procedure
	// she has refined, not a new one, and the date says how long she has had it.
	if _, had := l.first[s.Name]; !had {
		l.first[s.Name] = now
	}
	l.byID[s.Name] = s
	l.used[s.Name] = now
	l.evictLocked()
	snapshot := l.snapshotLocked()
	l.mu.Unlock()

	return l.save(snapshot)
}

// Get returns a learned procedure and marks it used, so the cap evicts what she
// has stopped reaching for rather than what she happened to learn first.
func (l *Learned) Get(name string) (Skill, bool) {
	name = normaliseName(name)
	l.mu.Lock()
	s, ok := l.byID[name]
	if ok {
		l.used[name] = time.Now()
	}
	snapshot := l.snapshotLocked()
	l.mu.Unlock()

	if ok {
		// Best effort: failing to record a read must not fail the read.
		_ = l.save(snapshot)
	}
	return s, ok
}

// Index renders the one-line-per-procedure listing, newest-learned last so the
// order is stable and reads as a history. Empty when she has learned nothing,
// so the tail costs nothing until there is something to say.
func (l *Learned) Index() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byID) == 0 {
		return ""
	}
	names := make([]string, 0, len(l.byID))
	for n := range l.byID {
		names = append(names, n)
	}
	sort.Strings(names)

	var sb strings.Builder
	for _, n := range names {
		fmt.Fprintf(&sb, "- %s — %s\n", n, l.byID[n].Summary)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// Names lists what she has taught herself, sorted.
func (l *Learned) Names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.byID))
	for n := range l.byID {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// evictLocked drops the least recently used until the store is within the cap.
func (l *Learned) evictLocked() {
	for len(l.byID) > learnedCap {
		var oldest string
		var when time.Time
		for n := range l.byID {
			t := l.used[n]
			if oldest == "" || t.Before(when) {
				oldest, when = n, t
			}
		}
		delete(l.byID, oldest)
		delete(l.used, oldest)
		delete(l.first, oldest)
	}
}

func (l *Learned) snapshotLocked() stored {
	var s stored
	for n, sk := range l.byID {
		s.Skills = append(s.Skills, storedSkill{
			Name: n, Summary: sk.Summary, Body: sk.Body,
			Learned: l.first[n], LastUsed: l.used[n],
		})
	}
	sort.Slice(s.Skills, func(i, j int) bool { return s.Skills[i].Name < s.Skills[j].Name })
	s.Superseded = append([]supersededSkill(nil), l.gone...)
	return s
}

// save writes via a temporary file and a rename, so an interrupted write cannot
// leave a half-written store behind — the same rule the memory archive follows.
func (l *Learned) save(s stored) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// normaliseName keeps names to one shape, so "Portal Signin" and "portal-signin"
// cannot become two entries for one thing.
func normaliseName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
