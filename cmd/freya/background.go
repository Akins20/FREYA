package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/akins/jarvis/internal/agent"
	"github.com/akins/jarvis/internal/config"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/skills"
	"github.com/akins/jarvis/internal/work"
)

// Wiring a background job to an actual conversation.
//
// The Manager knows nothing about agents or memory — it owns states, slots and
// cancellation. Everything that makes a job *her* doing the work lives here: the
// cloned agent, the branched memory, the scope it runs in, and what happens to
// the result when it is done.

// newWorkManager builds the background-job manager for this process.
func newWorkManager(ctx context.Context, a *agent.Agent, store *memory.Store,
	cfg *config.Config) *work.Manager {

	mgr := work.New(ctx, work.DefaultConcurrency, func(jobCtx context.Context, j *work.Job) (string, error) {
		// Its own conversation: the stable tiers are shared with the foreground —
		// same identity, facts and episodes, so the same cached prefix — and the
		// history is frozen at this moment, so the two threads cannot rewrite each
		// other's prompt.
		branch := memory.NewBranch(store, j.ID)

		// Its own place to work. A job that changes directory, or opens a tab,
		// must not move the foreground underneath the user; the scope is what
		// makes that true rather than merely hoped for.
		scope := skills.NewScope(skills.NewWorkspace(cfg.WorkDir), j.ID+"-", j.ID)

		worker := a.ForJob(branch, scope)
		// Progress goes to the job, not to a terminal nobody is watching. This is
		// what makes "what are you doing?" answerable while it happens.
		worker.OnInterim = func(text string) { j.Note(clipLine(text, 100)) }
		worker.OnTool = func(event, name, detail string) {
			if event == "start" {
				j.Note(name)
			}
		}

		res, err := worker.Ask(jobCtx, j.Goal)
		if err != nil {
			return "", err
		}

		// The transcript is written whole, and separately. Nothing is destroyed —
		// a standing rule here — but eighty tool turns from a background task
		// would evict the conversation the user is actually having, so they stay
		// out of the working set.
		if terr := memory.RecordJob(store.Dir(), j.ID, j.Goal, branch.Own()); terr != nil {
			fmt.Fprintf(os.Stderr, "record job transcript: %v\n", terr)
		}
		return res.Reply, nil
	})

	mgr.OnFinish = reportJob
	return mgr
}

// watchForAnOpening speaks a held report when no opening arrives.
//
// The preferred path is her next reply, where a finished job becomes "oh, and
// those quizzes are done" in her own words. But a report held forever is its own
// kind of unhelpful: if nobody says anything for a while, she should just tell
// you. The speaker's priority rules do the rest — a Background utterance is
// dropped rather than queued, so this can never talk over a conversation that
// starts in the meantime.
func watchForAnOpening(ctx context.Context, speak func(string) bool) {
	if speak == nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			oldest := reports.oldest()
			if oldest.IsZero() || time.Since(oldest) < reportIdle {
				continue
			}
			if currentTurn() != nil {
				continue // she is mid-conversation; her reply will carry it
			}
			held := reports.drain()
			line := speakable(held)
			if line == "" {
				continue
			}
			fmt.Printf("%s  ⬛ %s%s\n", cDim, line, cReset)
			if !speak(line) {
				// The device was taken between the check and the attempt. Put them
				// back rather than losing the result to a race.
				for _, r := range held {
					reports.add(r)
				}
			}
		}
	}
}

// reportJob folds a finished job back into the conversation.
//
// Exactly one turn, and it is written through the archive's normal path so it
// lands under the store's lock, in order, like anything else she said. That
// single turn is the whole reconciliation: the user hears the outcome, the next
// prompt contains it, and nothing else from the job's private thread leaks into
// the working set.
func reportJob(j *work.Job) {
	result, err := j.Result()
	outcome := strings.TrimSpace(result)
	if outcome == "" && err != nil {
		outcome = err.Error()
	}
	if outcome == "" {
		return // nothing happened worth mentioning
	}

	fmt.Printf("%s  ⬛ [%s] %s — %s%s\n", cDim, j.ID, clipLine(j.Goal, 60),
		clipLine(outcome, 100), cReset)

	// Held rather than announced, and NOT archived as a turn of its own. A
	// templated "Finished in the background (goal): result" turn was both an
	// interruption and words she never said; what lands in the archive is
	// whatever she actually says about it on her next turn.
	reports.add(report{
		id:      j.ID,
		goal:    j.Goal,
		outcome: outcome,
		state:   j.State(),
		at:      time.Now(),
	})
}

// jobs is the process's background-work manager.
//
// Package-level because the same manager has to be reachable from the tools she
// calls, the slash commands, and the daemon loops — and there is exactly one of
// it per process, for the same reason there is one archive.
var jobs *work.Manager

// jobsCommand is the terminal's window onto background work.
//
// The same information she gets from work_list, because there is one truth about
// what is running and the user should not have to ask her to find out.
func jobsCommand(rest string) error {
	if jobs == nil {
		return fmt.Errorf("background work is not available in this mode")
	}
	rest = strings.TrimSpace(rest)
	switch {
	case rest == "stop all":
		fmt.Printf("  stopped %d\n", jobs.CancelAll())
		return nil
	case strings.HasPrefix(rest, "stop "):
		id := strings.TrimSpace(strings.TrimPrefix(rest, "stop "))
		if !jobs.Cancel(id) {
			return fmt.Errorf("%s is not running", id)
		}
		fmt.Printf("  stopped %s\n", id)
		return nil
	}

	list := jobs.List()
	if len(list) == 0 {
		fmt.Println("  nothing running in the background")
		return nil
	}
	for _, j := range list {
		fmt.Printf("  %s\n", j.Describe())
		for _, p := range j.Progress() {
			fmt.Printf("      · %s\n", p)
		}
	}
	return nil
}

// daemonActive reports whether the daemon still owns the memory store. An
// interactive session takes it, and while it is gone the daemon must not start an
// exchange it cannot archive.
var daemonActive atomic.Bool

// handoverGrace is how long a yield waits for the exchange already in flight.
//
// Generous, because the alternative is losing a reply the user is waiting for;
// bounded, because a wedged exchange must not stop a session from starting.
const handoverGrace = 15 * time.Second
