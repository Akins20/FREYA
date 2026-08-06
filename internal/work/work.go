// Package work runs long tasks in the background, as separate threads of
// conversation, so that asking her to do something slow does not mean waiting
// for it.
//
// # Why she was single-threaded, and why that was ours to fix
//
// Nothing in the model made her serial. The substrate did: one archive appended
// in wall-clock order, one process working directory, one microphone gate that
// dropped work on contention. A quiz run that takes four minutes therefore took
// four minutes of her, and anything said meanwhile was either ignored or
// interleaved into a transcript that then read as one deranged conversation.
//
// # The shape
//
// A Job is a goal, a state, and a way to stop it. The Manager owns a small
// bounded pool — two at once, because the constraint is her attention and the
// provider's rate limit, not CPU — and each job runs on its own isolated
// conversation (memory.Branch) in its own execution scope.
//
// The Manager deliberately knows nothing about agents or models. It is handed a
// Runner, so this package can be tested without a provider and cannot drag the
// whole assistant into a dependency cycle.
package work

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// State is where a job has got to.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Done      State = "done"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Finished reports whether a job will change no further.
func (s State) Finished() bool { return s == Done || s == Failed || s == Cancelled }

// Job is one background thread of work.
type Job struct {
	ID   string
	Goal string
	// Origin is who asked — "you", "schedule", "watcher" — so a report can say
	// why this happened without the user having to remember.
	Origin  string
	Created time.Time
	Started time.Time
	Ended   time.Time

	mu       sync.Mutex
	state    State
	result   string
	err      error
	progress []string
	cancel   context.CancelFunc
}

// State reports the job's current state.
func (j *Job) State() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

// Result returns what the job produced and why it ended.
func (j *Job) Result() (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.result, j.err
}

// Note records a line of progress, so "what is it doing?" has an answer that is
// not "no idea".
//
// Bounded: a forty-round job would otherwise accumulate a transcript here as
// well as in its branch, and this copy exists only to be glanced at.
func (j *Job) Note(line string) {
	if line == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.progress = append(j.progress, line)
	if len(j.progress) > maxNotes {
		// Keep the beginning and the end: the middle of a long run is repetition,
		// and dropping it silently would misrepresent the length.
		keep := append([]string{}, j.progress[:maxNotes/2]...)
		keep = append(keep, fmt.Sprintf("… %d more …", len(j.progress)-maxNotes))
		j.progress = append(keep, j.progress[len(j.progress)-maxNotes/2:]...)
	}
}

// Progress returns the notes recorded so far.
func (j *Job) Progress() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.progress...)
}

const maxNotes = 12

// Runner does the actual work. Supplied by the caller so this package never
// imports the agent.
//
// The context is cancelled when the job is cancelled or the manager shuts down.
type Runner func(ctx context.Context, j *Job) (string, error)

// Manager owns the running jobs and the pool they run in.
type Manager struct {
	// Concurrency is how many jobs may run at once. Two: the binding constraint
	// is her attention and the provider's rate limit, not the machine.
	slots chan struct{}
	run   Runner

	// OnFinish is called once per job, after it ends, outside every lock — this
	// is where the caller reconciles the job's memory with the real archive and
	// tells the user. It must not block for long.
	OnFinish func(*Job)

	mu   sync.Mutex
	jobs map[string]*Job
	seq  int
	// parent is the manager's lifetime. Jobs are children of it, so shutting the
	// manager down stops everything.
	parent context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
}

// DefaultConcurrency is how many background jobs run at once.
const DefaultConcurrency = 2

// ErrBusy is returned when the pool is full and the queue would be unbounded.
var ErrBusy = errors.New("all background slots are in use")

// New builds a manager. ctx bounds every job it will ever start.
func New(ctx context.Context, concurrency int, run Runner) *Manager {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	inner, stop := context.WithCancel(ctx)
	return &Manager{
		slots:  make(chan struct{}, concurrency),
		run:    run,
		jobs:   map[string]*Job{},
		parent: inner,
		stop:   stop,
	}
}

// maxQueued bounds how many jobs may be waiting for a slot.
//
// Small on purpose. A queue that grows without limit turns "start this in the
// background" into a way to promise the user things that will never happen —
// twenty queued jobs behind a stuck one is not concurrency, it is a backlog she
// will report as in-progress.
const maxQueued = 4

// Start queues a job and returns it immediately.
func (m *Manager) Start(goal, origin string) (*Job, error) {
	if m.run == nil {
		return nil, errors.New("no runner configured")
	}
	m.mu.Lock()
	waiting := 0
	for _, j := range m.jobs {
		if j.State() == Queued {
			waiting++
		}
	}
	if waiting >= maxQueued {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	m.seq++
	// The cancel handle is created HERE, not in the goroutine, so it exists the
	// moment the caller holds the job. Creating it inside execute left a window
	// where a job cancelled immediately after Start — which is exactly what "no,
	// stop" does — found no handle and was silently not cancelled.
	ctx, cancel := context.WithCancel(m.parent)
	j := &Job{
		ID:      fmt.Sprintf("job%d", m.seq),
		Goal:    goal,
		Origin:  origin,
		Created: time.Now(),
		state:   Queued,
		cancel:  cancel,
	}
	m.jobs[j.ID] = j
	m.mu.Unlock()

	m.wg.Add(1)
	go m.execute(j, ctx)
	return j, nil
}

func (m *Manager) execute(j *Job, ctx context.Context) {
	defer m.wg.Done()
	defer func() {
		j.mu.Lock()
		cancel := j.cancel
		j.mu.Unlock()
		cancel()
	}()

	// Wait for a slot, but stay cancellable while waiting — a job cancelled in
	// the queue must never start afterwards.
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		// Never carries an error: being stopped is not a failure, and a job that
		// was cancelled before it started has nothing to report at all.
		m.finish(j, Cancelled, "", nil)
		return
	}

	// Cancelled while queued, and the slot happened to free at the same moment.
	if ctx.Err() != nil {
		m.finish(j, Cancelled, "", nil)
		return
	}

	j.mu.Lock()
	j.state = Running
	j.Started = time.Now()
	j.mu.Unlock()

	result, err := m.run(ctx, j)
	switch {
	case ctx.Err() != nil:
		// Cancellation wins over whatever the runner returned: a runner unwinding
		// from a cancelled context reports an error that is a consequence, not a
		// cause, and calling it a failure would put a false alarm in front of the
		// user for something they asked for.
		m.finish(j, Cancelled, result, nil)
	case err != nil:
		m.finish(j, Failed, result, err)
	default:
		m.finish(j, Done, result, nil)
	}
}

func (m *Manager) finish(j *Job, s State, result string, err error) {
	j.mu.Lock()
	j.state = s
	j.result = result
	j.err = err
	j.Ended = time.Now()
	j.mu.Unlock()

	if m.OnFinish != nil {
		m.OnFinish(j)
	}
}

// Get returns a job by id.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// List returns every job, newest first.
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Created.After(out[k].Created) })
	return out
}

// Active reports how many jobs are queued or running — what "is she busy?"
// actually means.
func (m *Manager) Active() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if !j.State().Finished() {
			n++
		}
	}
	return n
}

// Cancel stops a job. It reports whether there was one to stop.
func (m *Manager) Cancel(id string) bool {
	m.mu.Lock()
	j, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	j.mu.Lock()
	cancel, running := j.cancel, !j.state.Finished()
	j.mu.Unlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	return true
}

// CancelAll stops everything, for "stop what you're doing".
func (m *Manager) CancelAll() int {
	n := 0
	for _, j := range m.List() {
		if m.Cancel(j.ID) {
			n++
		}
	}
	return n
}

// Shutdown cancels every job and waits for them to unwind, so a process exiting
// does not leave a half-written job transcript behind.
func (m *Manager) Shutdown(wait time.Duration) {
	m.stop()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

// Describe renders one job as a line a person can read.
func (j *Job) Describe() string {
	state := j.State()
	var when string
	switch {
	case state == Queued:
		when = "waiting for a slot"
	case !j.Ended.IsZero():
		when = fmt.Sprintf("%s, took %s", state, j.Ended.Sub(j.Started).Round(time.Second))
	case !j.Started.IsZero():
		when = fmt.Sprintf("running for %s", time.Since(j.Started).Round(time.Second))
	default:
		when = string(state)
	}
	line := fmt.Sprintf("[%s] %s — %s (asked by %s)", j.ID, j.Goal, when, j.Origin)
	if _, err := j.Result(); err != nil {
		line += fmt.Sprintf("\n      failed: %v", err)
	}
	return line
}
