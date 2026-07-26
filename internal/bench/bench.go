// Package bench runs Freya against strict, outcome-checked agentic tasks.
//
// # What a real benchmark for an agent looks like
//
// The point is not whether she says something plausible — a chatbot does that.
// The point is whether she *drives work to a verifiable end state*: a file that
// exists with the right content, a document with the right structure, a
// multi-step task where every step actually happened. So every benchmark ends in
// a Check that inspects the world she left behind, not the words she left in the
// terminal. A benchmark passes only if the artifact is there and correct.
//
// # Black box on purpose
//
// The runner drives the real `freya` binary as a subprocess, in a throwaway
// workspace, with its data and project directories pointed inside that
// workspace. Nothing is mocked. This tests exactly what ships — the same agent
// loop, the same tools, the same prompt — rather than a reconstruction of it that
// could pass while the real thing fails. The cost is that it needs a live model
// and its API key; that is the correct cost for measuring an agent.
package bench

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/telemetry"
)

// Benchmark is one task with a verifiable outcome.
type Benchmark struct {
	// Name is a short unique identifier.
	Name string
	// Category groups related benchmarks for the scorecard.
	Category string
	// Difficulty is 1 (basic) to 5 (hard orchestration), for weighting.
	Difficulty int
	// Setup prepares the workspace before the task — input files, a project
	// tree, whatever the task reads. Optional.
	Setup func(workdir string) error
	// Prompt is the instruction handed to the agent, exactly as a user would say
	// it. It should demand work, not a fact.
	Prompt string
	// Timeout bounds the run. Zero uses the harness default.
	Timeout time.Duration
	// Check inspects the world after the run and reports pass plus a one-line
	// reason. This is where strictness lives: verify the artifact, not the reply.
	Check func(w *World) (bool, string)
	// Exclusive marks a benchmark whose Check reads state living outside its own
	// workspace — the fake portal's record of which quiz page was reached, say.
	// Those cannot overlap: with the driver running jobs in parallel, and the same
	// benchmark enqueued once per run, one run's progress would satisfy another
	// run's Check and report a pass nobody earned. Exclusive benchmarks are
	// serialised against each other; everything else still runs in parallel.
	Exclusive bool
}

// World is what a Check inspects: the workspace, and what the agent did.
type World struct {
	Dir       string        // the workspace root the task ran in
	Reply     string        // her final spoken/printed answer
	Trace     string        // full verbose output, including the tool trace
	ToolCalls []string      // tools invoked, in order
	Rounds    int           // agent loop iterations used
	Duration  time.Duration // wall-clock for the run
	ExitOK    bool          // the process exited cleanly
	TimedOut  bool

	// Events is the run's own telemetry, read back from the workspace.
	//
	// This costs nothing to collect: FREYA_DATA_DIR already points inside the
	// workspace, so she has always been writing a machine-readable record of every
	// tool call and model call there — and it was always thrown away with the temp
	// directory. Parsing it turns questions the text trace cannot answer ("how many
	// rounds achieved nothing", "did she call the same thing twenty times", "did a
	// tool succeed and return nothing") into ordinary checks.
	Events []telemetry.Event
}

// toolEvents returns the run's tool calls in the order they were recorded.
// Concurrent tool goroutines mean file order is not causal order, so this sorts
// by the sequence number stamped at record time.
func (w *World) toolEvents() []telemetry.Event {
	var out []telemetry.Event
	for _, e := range w.Events {
		if e.Kind == telemetry.KindTool {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// WastedRounds counts rounds in which every tool call failed — a whole model
// call spent achieving nothing. This is the shape of the thrash that used to eat
// a 40-round budget: not one catastrophic error, but twenty rounds of near-misses.
func (w *World) WastedRounds() int {
	type tally struct{ total, failed int }
	rounds := map[int]*tally{}
	for _, e := range w.toolEvents() {
		if e.Round == 0 {
			continue // pre-correlation event; cannot attribute it
		}
		t := rounds[e.Round]
		if t == nil {
			t = &tally{}
			rounds[e.Round] = t
		}
		t.total++
		if !e.OK {
			t.failed++
		}
	}
	n := 0
	for _, t := range rounds {
		if t.total > 0 && t.total == t.failed {
			n++
		}
	}
	return n
}

// LongestRepeatRun returns the longest run of consecutive calls to the same tool
// with the same arguments that all failed, and the tool's name.
//
// Identical arguments are the point: twenty different selectors is exploration,
// twenty of the same one is a stuck loop, and only the argument fingerprint tells
// them apart.
func (w *World) LongestRepeatRun() (int, string) {
	best, bestName := 0, ""
	run, key, name := 0, "", ""
	for _, e := range w.toolEvents() {
		k := e.Name + "|" + e.ArgsHash
		if !e.OK && k == key {
			run++
		} else {
			run, key, name = 1, k, e.Name
			if e.OK {
				run, key = 0, ""
			}
		}
		if run > best {
			best, bestName = run, name
		}
	}
	return best, bestName
}

// SilentNoops counts calls that reported success and returned nothing at all —
// the failure mode that is invisible to the model, because an empty success and
// a real one arrive looking identical.
func (w *World) SilentNoops() int {
	n := 0
	for _, e := range w.toolEvents() {
		if e.Outcome == telemetry.OutcomeEmpty {
			n++
		}
	}
	return n
}

// FailedTools counts tool calls that returned an error.
func (w *World) FailedTools() int {
	n := 0
	for _, e := range w.toolEvents() {
		if !e.OK {
			n++
		}
	}
	return n
}

// Result is the graded outcome of one benchmark.
type Result struct {
	Benchmark Benchmark
	World     *World
	Pass      bool
	Reason    string
}

const defaultTimeout = 4 * time.Minute

// Runner executes benchmarks against a freya binary.
type Runner struct {
	// Binary is the path to the freya executable under test.
	Binary string
	// Env is extra environment (the API keys) layered over the process env.
	Env []string
	// KeepWorkdirs leaves the throwaway workspaces on disk for inspection.
	KeepWorkdirs bool
	// Verbose streams each run's tool trace as it happens.
	Verbose bool
}

// Run executes one benchmark end to end and grades it.
func (r *Runner) Run(ctx context.Context, b Benchmark) Result {
	w := &World{}
	res := Result{Benchmark: b, World: w}

	dir, err := os.MkdirTemp("", "freya-bench-"+safe(b.Name)+"-")
	if err != nil {
		res.Reason = "could not create workspace: " + err.Error()
		return res
	}
	w.Dir = dir
	if !r.KeepWorkdirs {
		defer os.RemoveAll(dir)
	}

	// Data and projects live inside the workspace so a benchmark cannot see the
	// user's real memory or files, and starts from a clean slate every time.
	dataDir := filepath.Join(dir, ".freya-data")
	workspace := filepath.Join(dir, "workspace")
	_ = os.MkdirAll(workspace, 0o755)

	if b.Setup != nil {
		if err := b.Setup(workspace); err != nil {
			res.Reason = "setup failed: " + err.Error()
			return res
		}
	}

	timeout := b.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	// -yes: a benchmark measures whether she can do the work, not whether a human
	// is standing by to approve each file write. She runs with the same autonomy
	// the daemon has — routine work proceeds, the destructive is still refused.
	cmd := exec.CommandContext(runCtx, r.Binary, "-v", "-yes", "-ask", b.Prompt)
	cmd.Dir = workspace
	cmd.Env = append(append(os.Environ(), r.Env...),
		"FREYA_DATA_DIR="+dataDir,
		"FREYA_PROJECTS_DIR="+workspace,
		// Neutralise any FREYA_WORK_DIR the operator's shell exports: a benchmark
		// run must anchor to its own sandbox (cmd.Dir), never to the daemon's
		// shared workspace, or every run would write into the same real folder
		// and the file checks would read each other's output. Empty means "stay
		// where you were launched", which is exactly cmd.Dir.
		"FREYA_WORK_DIR=",
		"NO_COLOR=1",
	)

	out, runErr := cmd.CombinedOutput()
	w.Duration = time.Since(start)
	w.Trace = string(out)
	w.ExitOK = runErr == nil
	w.TimedOut = runCtx.Err() == context.DeadlineExceeded
	w.Reply = extractReply(w.Trace)
	w.ToolCalls = extractToolCalls(w.Trace)
	w.Rounds = extractRounds(w.Trace)
	// Her own telemetry, written into this workspace during the run. Best-effort:
	// a run that crashed before flushing still has a trace to grade on.
	w.Events, _ = telemetry.Load(filepath.Join(dataDir, "telemetry.jsonl"), time.Time{})

	if r.Verbose {
		fmt.Printf("  [%s] %d tools, %d rounds, %s\n", b.Name, len(w.ToolCalls), w.Rounds, w.Duration.Round(time.Second))
	}

	if w.TimedOut {
		res.Reason = fmt.Sprintf("timed out after %s (%d tools used)", timeout, len(w.ToolCalls))
		return res
	}

	if b.Check == nil {
		res.Reason = "no check defined"
		return res
	}
	res.Pass, res.Reason = b.Check(w)
	return res
}

// --- trace parsing -----------------------------------------------------------

// toolLine matches the verbose tracer's "  → toolname args" start marker.
var toolLine = regexp.MustCompile(`(?m)^\s*→\s+(\w+)`)

func extractToolCalls(trace string) []string {
	var calls []string
	for _, m := range toolLine.FindAllStringSubmatch(trace, -1) {
		calls = append(calls, m[1])
	}
	return calls
}

// roundLine matches the "· N round(s)" field the verbose accounting line carries.
//
// Anchored on the literal "round(s)" rather than a bare "round", so a tool that
// happens to print "3 rounds" of its own cannot be mistaken for the footer.
var roundLine = regexp.MustCompile(`(\d+)\s+round\(s\)`)

func extractRounds(trace string) int {
	m := roundLine.FindStringSubmatch(trace)
	if m == nil {
		return 0
	}
	n := 0
	fmt.Sscanf(m[1], "%d", &n)
	return n
}

// extractReply pulls the model's final answer out of the trace.
//
// The one-shot path prints the reply on its own, but the verbose tracer
// interleaves tool lines; the reply is the run of non-trace text before the
// timing footer. This is a heuristic, and Checks that need the reply should be
// forgiving — the artifacts are the real signal.
//
// Her *reasoning* must be filtered as hard as her tool trace. Thinking is on by
// default and prints as "💭 …", so leaving it in let a Check pass on a token she
// only thought about, and fail on a stalling phrase she considered and rejected —
// both of which make the instrument lie about the thing it is measuring. The
// "tools:" accounting line is dropped for the same reason: it names every tool
// she called, so a reply check could match on a tool name rather than an answer.
func extractReply(trace string) string {
	var b strings.Builder
	sc := bufio.NewScanner(strings.NewReader(trace))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, "→"), strings.HasPrefix(t, "✓"), strings.HasPrefix(t, "✗"):
			continue
		case strings.HasPrefix(t, "💭"):
			continue
		case strings.HasPrefix(t, "context:"), strings.Contains(t, "round(s)"):
			continue
		case strings.HasPrefix(t, "tools:"):
			continue
		case strings.Contains(t, "deferring background"):
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func safe(s string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(s, "-")
}
