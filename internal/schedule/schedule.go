// Package schedule lets Freya set a task for her future self and have the
// daemon actually run it when the time comes.
//
// # Why this is different from a reminder
//
// A reminder is a message to the user: "you asked me to remind you to call the
// bank". A self-task is a message to *herself* that she then acts on: "in five
// minutes, check whether the download finished and, if it did, unzip it". The
// first ends in a notification; the second ends in work. Without this she cannot
// say "I'll check back on that shortly" and mean it — when a turn ends, nothing
// runs, so a promise to continue later is a promise she structurally cannot
// keep. This is the mechanism that keeps it.
//
// # Kept deliberately small
//
// A due time and a line of instruction, persisted to one JSON file, polled by
// the daemon. No cron syntax, no recurrence, no priorities — those can grow
// later. The point now is that a scheduled intention becomes an executed action.
package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const fileName = "selftasks.json"

// Task is one thing Freya has scheduled for herself.
type Task struct {
	ID      string    `json:"id"`
	Prompt  string    `json:"prompt"` // the instruction she will hand her future self
	Due     time.Time `json:"due"`
	Created time.Time `json:"created"`
	// Note is an optional human-facing label for why it was set.
	Note string `json:"note,omitempty"`
	// Ran records when it fired, so a task is executed once and not re-run on the
	// next poll.
	Ran *time.Time `json:"ran,omitempty"`
}

// Store persists scheduled tasks.
//
// It is safe for concurrent use: the daemon polls it while a skill adds to it.
type Store struct {
	mu   sync.Mutex
	path string
	// lost records that the task list could not be read and was set aside, so
	// callers can say so rather than reporting an empty schedule. Empty when
	// nothing has been lost.
	lost string
	// now is injected so tests are not at the mercy of the clock. Nil means
	// time.Now.
	now func() time.Time
}

// Open loads (or creates) a store in a directory.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(dir, fileName)}, nil
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// load reads the task list. Caller holds the lock.
func (s *Store) load() ([]Task, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tasks []Task
	if err := json.Unmarshal(b, &tasks); err != nil {
		// A corrupt file must not wedge scheduling forever, so this starts fresh
		// rather than failing every poll. What it must not do is start fresh
		// SILENTLY: everything she had promised to come back and do is in that
		// file, and an empty schedule is indistinguishable from a schedule that
		// was quietly thrown away. The symptom is her saying she will follow up
		// and then never following up, which is the exact failure the daemon
		// exists to prevent.
		//
		// So the bad file is moved aside rather than overwritten, and the path is
		// remembered so Pending and the tools can report it.
		aside := s.path + ".unreadable"
		if err := os.Rename(s.path, aside); err != nil {
			aside = s.path
		}
		s.lost = aside
		return nil, nil
	}
	return tasks, nil
}

// Lost reports where an unreadable task list was set aside, or empty if none
// was. Anything scheduled before that point is gone, and saying so is the whole
// point of keeping it.
func (s *Store) Lost() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lost
}

// save writes the task list atomically. Caller holds the lock.
func (s *Store) save(tasks []Task) error {
	b, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Add schedules a task and returns its human-readable confirmation.
func (s *Store) Add(prompt string, due time.Time, note string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	now := s.clock()
	t := Task{
		ID:      fmt.Sprintf("t%d", now.UnixNano()),
		Prompt:  prompt,
		Due:     due,
		Created: now,
		Note:    note,
	}
	tasks = append(tasks, t)
	if err := s.save(tasks); err != nil {
		return Task{}, err
	}
	return t, nil
}

// DueNow returns the tasks that have come due and marks them as run, so each
// fires exactly once.
//
// Marking before the caller executes is deliberate: if execution crashes the
// daemon, a task that re-ran every restart would be worse than one that was
// missed. At-most-once beats a self-scheduled action looping on a crash.
func (s *Store) DueNow() ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	now := s.clock()

	var due []Task
	changed := false
	for i := range tasks {
		if tasks[i].Ran == nil && !tasks[i].Due.After(now) {
			ran := now
			tasks[i].Ran = &ran
			due = append(due, tasks[i])
			changed = true
		}
	}
	if changed {
		if err := s.save(s.prune(tasks, now)); err != nil {
			return due, err
		}
	}
	return due, nil
}

// prune drops tasks that ran more than a day ago, so the file does not grow
// without bound. Recent ones are kept for a short history.
func (s *Store) prune(tasks []Task, now time.Time) []Task {
	cutoff := now.Add(-24 * time.Hour)
	kept := tasks[:0]
	for _, t := range tasks {
		if t.Ran == nil || t.Ran.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// Pending returns tasks not yet run, soonest first.
func (s *Store) Pending() ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	var pending []Task
	for _, t := range tasks {
		if t.Ran == nil {
			pending = append(pending, t)
		}
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].Due.Before(pending[j].Due) })
	return pending, nil
}

// Cancel removes a pending task by id.
func (s *Store) Cancel(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks, err := s.load()
	if err != nil {
		return false, err
	}
	kept := tasks[:0]
	found := false
	for _, t := range tasks {
		if t.ID == id && t.Ran == nil {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return false, nil
	}
	return true, s.save(kept)
}
