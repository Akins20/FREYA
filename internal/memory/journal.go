package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Two conversations at once, without them becoming one.
//
// # The problem a background job creates
//
// She is single-threaded because the archive is: every turn appends to one
// append-only file in the order it happened, and the working set replays that
// file verbatim. Run a background job against the same store and its turns
// interleave with the foreground's — the transcript then reads as one deranged
// conversation ("open my portal" / "your inbox is empty" / "quiz 2 submitted"),
// and the next prompt is assembled from that. Worse for cost, every job turn
// changes the shared prefix, so the ~70% cache hit rate collapses for both.
//
// # The decision
//
// A background job is an ISOLATED conversation, not an interleaved one. It runs
// against a Branch: identity, facts and episodes are read live from the real
// store (shared knowledge, and the cached prefix), the conversation it starts
// from is FROZEN at the moment it was spawned, and every turn it produces
// accumulates in the branch instead of the archive.
//
// Freezing is what buys the cache: the job's prompt opens with exactly the bytes
// the foreground had at spawn, so both threads hit the same cached prefix and
// diverge only in their tails.
//
// When the job finishes, one summary turn joins the real conversation. Its full
// transcript is written to jobs.jsonl — nothing is destroyed, which is a standing
// rule here — but it stays out of the working set, where eighty tool turns from a
// background task would evict the conversation the user is actually having.

// Journal is what one thread of conversation needs from memory.
//
// The real Store satisfies it, and so does a Branch. Facts are deliberately NOT
// branched: a fact learned by a background job is knowledge, it belongs to
// everybody, and it does not interleave anything — only turns do.
type Journal interface {
	AppendTurn(Turn) (Turn, error)
	Turns() []Turn
	TurnCount() int
	// View is one consistent read of everything prompt assembly needs.
	View() View
	// Advance moves the verbatim window forward and returns what left it.
	Advance(to int) []Turn
	AddEpisode(Episode) error
	// Facts are shared, not branched — see the note above — but a thread of work
	// still reads them, so they belong on the interface.
	Facts() []Fact
	Dir() string
}

// View is a consistent read of the tiers, taken under a single lock.
//
// Build used to call the store six separate times — Identity, Facts (twice),
// Episodes, WorkingSet — so a concurrent writer could land between them and the
// assembled prompt would describe two different moments. With one thread that was
// merely theoretical; with a background job running it is a Tuesday.
type View struct {
	Identity string
	Facts    []Fact
	Episodes []Episode
	// Turns is the whole archive, oldest first. Anchor is where the verbatim
	// window currently starts within it.
	Turns  []Turn
	Anchor int
}

// Window returns the turns currently inside the verbatim tier.
func (v View) Window() []Turn {
	if v.Anchor < 0 || v.Anchor > len(v.Turns) {
		return v.Turns
	}
	return v.Turns[v.Anchor:]
}

// Branch is one background job's isolated view of memory.
type Branch struct {
	parent *Store

	mu sync.Mutex
	// base is the conversation as it stood when the job was spawned, frozen so
	// the foreground cannot change this job's prompt underneath it.
	base []Turn
	// own is everything this job has said and done. It never reaches the
	// archive; the Manager writes one summary turn there instead.
	own []Turn
	// anchor is the verbatim window's start within base+own, so a long job
	// evicts its own history rather than growing without bound.
	anchor int
	// jobID names this thread of work in the job transcript.
	jobID string
}

// NewBranch forks an isolated conversation from the live store.
func NewBranch(parent *Store, jobID string) *Branch {
	return &Branch{parent: parent, base: parent.Turns(), jobID: jobID}
}

// AppendTurn records a turn against this job only.
//
// It cannot return ErrSuspended, and that is a feature: a background job must not
// die because an interactive session took the write handle. Its work is held here
// and reconciled by the Manager when it finishes.
func (b *Branch) AppendTurn(t Turn) (Turn, error) {
	if t.ID == "" {
		t.ID = newID()
	}
	if t.Timestamp.IsZero() {
		t.Timestamp = time.Now()
	}
	if t.Tokens == 0 {
		t.Tokens = EstimateTokens(t.Text)
	}
	if t.SessionID == "" {
		t.SessionID = "job:" + b.jobID
	}
	b.mu.Lock()
	b.own = append(b.own, t)
	b.mu.Unlock()
	return t, nil
}

// Turns returns the frozen history plus everything this job has added.
func (b *Branch) Turns() []Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Turn, 0, len(b.base)+len(b.own))
	return append(append(out, b.base...), b.own...)
}

func (b *Branch) TurnCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.base) + len(b.own)
}

// Own returns just this job's turns — its transcript, for the record.
func (b *Branch) Own() []Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Turn(nil), b.own...)
}

// View reads the stable tiers live from the parent and the conversation from the
// branch. Identity, facts and episodes are shared on purpose: they are the same
// knowledge and the same cached prefix bytes for both threads.
func (b *Branch) View() View {
	pv := b.parent.View()
	turns := b.Turns()
	b.mu.Lock()
	anchor := b.anchor
	b.mu.Unlock()
	if anchor > len(turns) {
		anchor = 0
	}
	return View{
		Identity: pv.Identity,
		Facts:    pv.Facts,
		Episodes: pv.Episodes,
		Turns:    turns,
		Anchor:   anchor,
	}
}

// Advance moves this job's own window forward. Nothing is written to disk: the
// branch is discarded when the job ends, after its transcript is recorded.
func (b *Branch) Advance(to int) []Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := len(b.base) + len(b.own)
	if to <= b.anchor || to > total {
		return nil
	}
	all := append(append([]Turn(nil), b.base...), b.own...)
	evicted := all[b.anchor:to]
	b.anchor = to
	return evicted
}

// AddEpisode is deliberately a no-op.
//
// Episodes are the archive's own distillation of a conversation that aged out.
// A background job's history is not archive material — it is written whole to
// jobs.jsonl and summarised into a single turn — so distilling it here would put
// a job's internal chatter into the shared long-term memory of every future
// prompt.
func (b *Branch) AddEpisode(Episode) error { return nil }

// Facts delegate to the parent: knowledge is shared, and a fact a job learns is
// the same fact the foreground should have.
func (b *Branch) Facts() []Fact { return b.parent.Facts() }

func (b *Branch) Dir() string { return b.parent.Dir() }

// jobsFile is the append-only record of what background jobs actually did.
const jobsFile = "jobs.jsonl"

// RecordJob writes a finished job's transcript, so nothing it did is destroyed
// even though none of it enters the working set.
//
// Best-effort and separate from the archive: a failure to record history must
// never fail the work itself, and the summary turn — the part the conversation
// needs — is written through the archive's own path where it is not optional.
func RecordJob(dir, jobID, goal string, turns []Turn) error {
	if dir == "" || len(turns) == 0 {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, jobsFile),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	header, err := json.Marshal(map[string]any{
		"job": jobID, "goal": goal, "at": time.Now(), "turns": len(turns),
	})
	if err != nil {
		return err
	}
	if _, err := f.Write(append(header, '\n')); err != nil {
		return err
	}
	for _, t := range turns {
		line, err := json.Marshal(t)
		if err != nil {
			return fmt.Errorf("memory: encode job turn: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}
