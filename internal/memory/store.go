package memory

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	archiveFile  = "archive.jsonl"
	factsFile    = "facts.json"
	episodesFile = "episodes.json"
	stateFile    = "state.json"
	identityFile = "identity.md"
)

// state is the small mutable bookkeeping that survives restarts.
type state struct {
	// WorkingAnchor is the archive index where the verbatim working set begins.
	// It advances in chunks, not per-turn, to preserve prompt-prefix caching.
	WorkingAnchor int `json:"working_anchor"`
	Sessions      int `json:"sessions"`
}

// Store persists Freya's memory to a directory of plain files.
//
// The archive is append-only JSONL and is never rewritten, so a crash can lose
// at most the turn in flight. Facts and episodes are small enough to rewrite
// atomically on change.
type Store struct {
	dir string

	mu       sync.RWMutex
	turns    []Turn
	facts    map[string]*Fact // keyed by Fact.Key
	episodes []Episode
	identity string
	st       state

	archive *os.File
}

// ErrSuspended is returned by writes attempted while another process owns the
// store.
var ErrSuspended = errors.New("memory: this process is not the current writer")

// Open loads (or initialises) a memory store rooted at dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("memory: create dir: %w", err)
	}
	s := &Store{dir: dir, facts: map[string]*Fact{}}

	if err := s.loadArchive(); err != nil {
		return nil, err
	}
	if err := s.loadJSON(factsFile, &s.facts); err != nil {
		return nil, err
	}
	if err := s.loadJSON(episodesFile, &s.episodes); err != nil {
		return nil, err
	}
	if err := s.loadJSON(stateFile, &s.st); err != nil {
		return nil, err
	}
	if s.facts == nil {
		s.facts = map[string]*Fact{}
	}

	// identity.md is meant to be edited by hand; absence is not an error.
	if b, err := os.ReadFile(filepath.Join(dir, identityFile)); err == nil {
		s.identity = string(b)
	}

	f, err := os.OpenFile(filepath.Join(dir, archiveFile),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("memory: open archive: %w", err)
	}
	s.archive = f

	s.st.Sessions++
	_ = s.saveJSON(stateFile, s.st)
	return s, nil
}

// Suspend releases the archive handle and stops this process writing.
//
// # Why memory has an owner at all
//
// The archive is an append-only file and the JSON stores are rewritten whole.
// Two processes writing either one interleave and corrupt it. So exactly one
// process holds the write handle at a time: the daemon while nobody is at the
// keyboard, and the interactive session the moment one starts.
//
// Suspend is the handover. Reads keep working from what is already in memory,
// which is what lets the daemon answer a question that arrives during the
// changeover instead of failing it.
func (s *Store) Suspend() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archive == nil {
		return nil
	}
	err := s.archive.Close()
	s.archive = nil
	return err
}

// Resume re-reads everything from disk and takes the write handle back.
//
// A full reload rather than a tail-read of what was appended: the session may
// also have rewritten facts, episodes and the working anchor, and reconciling
// those incrementally is a great deal of machinery to save reading a few
// megabytes that the page cache is holding anyway.
func (s *Store) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.archive != nil {
		return nil // already writing
	}

	s.turns = nil
	s.facts = map[string]*Fact{}
	s.episodes = nil

	if err := s.loadArchive(); err != nil {
		return err
	}
	if err := s.loadJSON(factsFile, &s.facts); err != nil {
		return err
	}
	if err := s.loadJSON(episodesFile, &s.episodes); err != nil {
		return err
	}
	if err := s.loadJSON(stateFile, &s.st); err != nil {
		return err
	}
	if s.facts == nil {
		s.facts = map[string]*Fact{}
	}
	if b, err := os.ReadFile(filepath.Join(s.dir, identityFile)); err == nil {
		s.identity = string(b)
	}

	f, err := os.OpenFile(filepath.Join(s.dir, archiveFile),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("memory: reopen archive: %w", err)
	}
	s.archive = f
	return nil
}

// Writing reports whether this process currently holds the write handle.
func (s *Store) Writing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.archive != nil
}

// Close releases the archive handle.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archive == nil {
		return nil
	}
	err := s.archive.Close()
	s.archive = nil
	return err
}

// SessionID returns a label for the current run.
func (s *Store) SessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fmt.Sprintf("s%04d", s.st.Sessions)
}

// --- turns ------------------------------------------------------------------

// AppendTurn records a turn in the archive. Tokens are computed if unset.
func (s *Store) AppendTurn(t Turn) (Turn, error) {
	if t.ID == "" {
		t.ID = newID()
	}
	if t.Timestamp.IsZero() {
		t.Timestamp = time.Now()
	}
	if t.Tokens == 0 {
		t.Tokens = EstimateTokens(t.Text)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if t.SessionID == "" {
		t.SessionID = fmt.Sprintf("s%04d", s.st.Sessions)
	}

	line, err := json.Marshal(t)
	if err != nil {
		return t, fmt.Errorf("memory: encode turn: %w", err)
	}
	// A suspended store must refuse rather than accept-and-discard. Keeping the
	// turn in memory while skipping the file would make it look recorded right
	// up until the next restart, which is the worst way to lose a conversation.
	if s.archive == nil {
		return t, ErrSuspended
	}
	if _, err := s.archive.Write(append(line, '\n')); err != nil {
		return t, fmt.Errorf("memory: append turn: %w", err)
	}
	s.turns = append(s.turns, t)
	return t, nil
}

// Turns returns every archived turn, oldest first.
func (s *Store) Turns() []Turn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Turn, len(s.turns))
	copy(out, s.turns)
	return out
}

// TurnCount reports how many turns are archived.
func (s *Store) TurnCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.turns)
}

// WorkingSet returns the verbatim tail of the archive that fits within budget,
// together with any turns evicted to make it fit.
//
// Eviction is chunked: rather than sliding the window by one turn per exchange
// (which would invalidate the cached prompt prefix every single request), the
// anchor jumps forward by evictChunk of the window at once and then holds still
// for many turns. Evicted turns are returned so the caller can distil them into
// an Episode before they leave the verbatim tier.
func (s *Store) WorkingSet(budget int, evictChunk float64) (working, evicted []Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.st.WorkingAnchor > len(s.turns) {
		s.st.WorkingAnchor = 0
	}
	anchor := s.st.WorkingAnchor

	total := 0
	for _, t := range s.turns[anchor:] {
		total += t.Tokens
	}

	if total > budget && budget > 0 {
		// Evict down to a level *below* the budget, not merely to it, so the
		// freed headroom absorbs many subsequent turns before the anchor moves
		// again. Targeting the budget itself would re-evict on every exchange
		// and invalidate the cached prefix each time — the exact failure this
		// chunking exists to avoid.
		target := budget - int(float64(budget)*evictChunk)
		if target < 0 {
			target = 0
		}
		// Never evict the newest turn. A turn larger than the whole budget
		// would otherwise drain the working set to nothing and discard the
		// exchange currently in progress.
		limit := len(s.turns) - 1
		for anchor < limit && total > target {
			total -= s.turns[anchor].Tokens
			anchor++
		}
		if anchor > s.st.WorkingAnchor {
			evicted = append(evicted, s.turns[s.st.WorkingAnchor:anchor]...)
			s.st.WorkingAnchor = anchor
			_ = s.saveJSONLocked(stateFile, s.st)
		}
	}

	working = append(working, s.turns[anchor:]...)
	return working, evicted
}

// --- facts ------------------------------------------------------------------

// PutFact inserts or updates a fact by key, preserving its creation time.
func (s *Store) PutFact(f Fact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if existing, ok := s.facts[f.Key]; ok {
		f.ID = existing.ID
		f.Created = existing.Created
	} else {
		if f.ID == "" {
			f.ID = newID()
		}
		f.Created = now
	}
	f.Updated = now
	f.Tokens = EstimateTokens(f.Text)
	s.facts[f.Key] = &f
	return s.saveJSONLocked(factsFile, s.facts)
}

// Facts returns all facts, pinned first, then most recently updated.
func (s *Store) Facts() []Fact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Fact, 0, len(s.facts))
	for _, f := range s.facts {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].Updated.After(out[j].Updated)
	})
	return out
}

// ForgetFact removes a fact by key. Returns false if it was not present.
func (s *Store) ForgetFact(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.facts[key]; !ok {
		return false, nil
	}
	delete(s.facts, key)
	return true, s.saveJSONLocked(factsFile, s.facts)
}

// --- episodes ---------------------------------------------------------------

// AddEpisode stores a distilled summary of aged-out turns.
func (s *Store) AddEpisode(e Episode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.ID == "" {
		e.ID = newID()
	}
	if e.Created.IsZero() {
		e.Created = time.Now()
	}
	e.Tokens = EstimateTokens(e.Summary)
	s.episodes = append(s.episodes, e)
	return s.saveJSONLocked(episodesFile, s.episodes)
}

// Episodes returns stored episodes, newest first.
func (s *Store) Episodes() []Episode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Episode, len(s.episodes))
	copy(out, s.episodes)
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// Identity returns the hand-curated identity.md contents, if any.
func (s *Store) Identity() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.identity
}

// Dir reports where the store keeps its files.
func (s *Store) Dir() string { return s.dir }

// --- persistence helpers ----------------------------------------------------

func (s *Store) loadArchive() error {
	f, err := os.Open(filepath.Join(s.dir, archiveFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memory: open archive: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Individual turns can exceed bufio's default 64KB line cap.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var t Turn
		if err := json.Unmarshal(line, &t); err != nil {
			// A torn final line from an interrupted write is survivable.
			continue
		}
		s.turns = append(s.turns, t)
	}
	return sc.Err()
}

func (s *Store) loadJSON(name string, dst any) error {
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memory: read %s: %w", name, err)
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("memory: parse %s: %w", name, err)
	}
	return nil
}

func (s *Store) saveJSON(name string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveJSONLocked(name, v)
}

// saveJSONLocked writes via a temp file and rename so readers never observe a
// half-written file. Callers must hold the lock.
func (s *Store) saveJSONLocked(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("memory: encode %s: %w", name, err)
	}
	final := filepath.Join(s.dir, name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("memory: write %s: %w", name, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("memory: replace %s: %w", name, err)
	}
	return nil
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
