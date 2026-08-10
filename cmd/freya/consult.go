package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/agent"
	"github.com/Akins20/FREYA/internal/claude"
	"github.com/Akins20/FREYA/internal/defect"
	"github.com/Akins20/FREYA/internal/telemetry"
)

// Turning what she could not do into a change to her.
//
// # The loop this closes
//
// Every fix in this codebase so far followed the same path: she fails, a person
// reads her telemetry, finds the cause a layer beneath the symptom, and changes
// the software. She has all the evidence — the trail, the errors, the exchange —
// and no way to hand it to anyone. So the person was the bottleneck, and the
// failures that nobody happened to look at simply persisted.
//
// Now a bad exchange writes itself down, and a bad enough one gets looked at by
// an engineer with her source in front of them.
//
// # What it deliberately does not do
//
// It does not deploy. The consult ends on a branch with the test results
// attached, and the running daemon keeps running what it was running. That is not
// caution for its own sake: twice in one week a change passed its tests and then
// broke her in production — a provenance rule that locked her out of a portal,
// an argument reconciler that broke two tests it should have left alone. Both
// were caught by a human reading the diff. A loop that writes AND deploys removes
// exactly that step.
//
// # The boundary that matters
//
// Her report is DATA. It quotes web pages, file contents and command output, any
// of which may be hostile, and a page that says "ignore your instructions" is
// quoted verbatim into an error message and travels here intact. defect.Fenced
// wraps it; the brief below says plainly that it is content. Without both, a web
// page has a path to her source code.

// consultBudget is how many defects may be looked at in a day.
//
// Small. Each consult is a full engineering session against the repository, and
// the value is in the first one or two: after that they are usually the same
// cause wearing different symptoms, and the branch from the first one already
// covers it.
const consultBudget = 3

// consultEvery is how often the pending queue is checked.
const consultEvery = 30 * time.Minute

// watchEvery is how often telemetry is swept for shapes no single exchange shows.
const watchEvery = 6 * time.Hour

// watchWindow is how far back a sweep looks.
const watchWindow = 24 * time.Hour

// engineer runs the consulting loop and the telemetry watcher.
type engineer struct {
	journal *defect.Journal
	claude  *claude.Client
	// source is the repository to work in. Without it there is nothing to read,
	// and the consult is skipped rather than run against the wrong directory.
	source  string
	dataDir string
}

// newEngineer wires the self-repair loop, or returns nil when it cannot run.
func newEngineer(j *defect.Journal, c *claude.Client, sourceDir, projectsDir, dataDir string) *engineer {
	if j == nil || c == nil || !c.Available() {
		return nil
	}
	src := findSource(sourceDir, projectsDir)
	if src == "" {
		return nil
	}
	return &engineer{journal: j, claude: c, source: src, dataDir: dataDir}
}

// findSource locates her own repository.
//
// Every candidate is checked by its go.mod rather than its name, so a renamed or
// moved checkout is still found and a directory that merely happens to be called
// FREYA is not. Returning empty is the right answer when nothing matches: a
// consult pointed at the wrong tree would read someone else's code and change it.
func findSource(sourceDir, projectsDir string) string {
	candidates := []string{
		sourceDir,
		filepath.Join(projectsDir, "FREYA"),
		filepath.Join(projectsDir, "JARVIS"),
		projectsDir,
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(mod), "github.com/Akins20/FREYA") {
			return dir
		}
	}
	return ""
}

// file records a failed exchange. Safe to call from any goroutine.
func (e *engineer) file(f agent.Failure) {
	if e == nil {
		return
	}
	r, err := e.journal.File(defect.Report{
		Kind:     defect.Kind(f.Kind),
		Goal:     f.Goal,
		Attempts: f.Attempts,
		Failures: f.Failures,
		Trail:    f.Trail,
		Exchange: f.Exchange,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "file defect: %v\n", err)
		return
	}
	fmt.Printf("%s  ⚑ noted a failure worth looking at: %s%s\n", cDim, r.Summary(), cReset)
}

// run starts both loops and returns.
func (e *engineer) run(ctx context.Context) {
	if e == nil {
		return
	}
	go e.watchTelemetry(ctx)
	go e.consultLoop(ctx)
}

// watchTelemetry sweeps the log for shapes no single exchange reveals.
func (e *engineer) watchTelemetry(ctx context.Context) {
	ticker := time.NewTicker(watchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			events, err := telemetry.Load(filepath.Join(e.dataDir, "telemetry.jsonl"),
				time.Now().Add(-watchWindow))
			if err != nil {
				continue
			}
			for _, r := range defect.Scan(events, watchWindow) {
				if _, err := e.journal.File(r); err != nil {
					fmt.Fprintf(os.Stderr, "file scan result: %v\n", err)
				}
			}
		}
	}
}

// consultLoop works the pending queue when the machine is otherwise quiet.
func (e *engineer) consultLoop(ctx context.Context) {
	ticker := time.NewTicker(consultEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Never while she is doing something. A consult is a long, heavy
			// operation, and it is competing for the same machine and the same
			// attention as the work the user is actually waiting for.
			if currentTurn() != nil || (jobs != nil && jobs.Active() > 0) {
				continue
			}
			if e.journal.ConsultedSince(startOfDay()) >= consultBudget {
				continue
			}
			pending := e.journal.Pending()
			if len(pending) == 0 {
				continue
			}
			e.consult(ctx, pending[0])
		}
	}
}

// consult hands one report to an engineer with her source in front of them.
func (e *engineer) consult(ctx context.Context, r defect.Report) {
	fmt.Printf("%s  ⚒ looking into %s%s\n", cDim, r.Summary(), cReset)

	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	res, err := e.claude.Run(ctx, claude.Options{
		Prompt:         consultBrief(r),
		Dir:            e.source,
		Effort:         "high",
		PermissionMode: "acceptEdits",
		Timeout:        20 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "consult %s: %v\n", r.ID, err)
		return
	}

	verdict := strings.TrimSpace(res.Text)
	branch := extractBranch(verdict)
	if err := e.journal.Resolve(r.ID, verdict, branch); err != nil {
		fmt.Fprintf(os.Stderr, "resolve %s: %v\n", r.ID, err)
	}

	head := firstLine(verdict)
	fmt.Printf("%s  ⚒ %s: %s%s\n", cDim, r.ID, clipLine(head, 140), cReset)
	if branch != "" {
		fmt.Printf("%s      on branch %s — review it before it goes anywhere%s\n",
			cDim, branch, cReset)
	}
}

// consultBrief is the whole interface between her failures and her source.
func consultBrief(r defect.Report) string {
	var b strings.Builder
	b.WriteString("You are looking at the source of Freya, a personal assistant written in " +
		"Go with no external dependencies. Read CLAUDE.md first: it explains the " +
		"architecture and the constraints that are load-bearing.\n\n" +
		"She tried to do something for her user and could not. Her report follows.\n\n")

	b.WriteString(r.Fenced())

	b.WriteString("\n\nWork out which of these it is, and say which in your first sentence:\n\n" +
		"  1. A DEFECT — she was prevented from doing something she should already be " +
		"able to do. A tool that misreports, an error that does not say what is wrong, " +
		"a guard that fires when it should not.\n" +
		"  2. A MISSING CAPABILITY — she has no tool for this and needs one.\n" +
		"  3. NEITHER — the task was genuinely impossible, or the software behaved " +
		"correctly and she used it wrongly. This is a perfectly good answer; say so and " +
		"stop.\n\n" +
		"Diagnose beneath the symptom. The causes found in this codebase have almost " +
		"always been a layer under where the error pointed: tools disagreeing about " +
		"what the page is, a result with no room to carry the truth, an error stating a " +
		"rule without saying what was actually sent.\n\n" +
		"If it is 1 or 2:\n" +
		"  · make the change on a NEW git branch (`git checkout -b fix/<short-name>`);\n" +
		"  · add a test that names the failure — that is the convention here, and a fix " +
		"without one is not finished;\n" +
		"  · run `make check` and report the result honestly, including if it fails;\n" +
		"  · commit to the branch. Do NOT merge, do NOT push, do NOT switch back to " +
		"master.\n\n" +
		"Hard limits, no exceptions:\n" +
		"  · do not install, deploy, or copy anything into ~/.local/bin;\n" +
		"  · do not restart, stop or otherwise touch the freya systemd service — she is " +
		"running right now and the user is using her;\n" +
		"  · do not read or write anything under ~/.local/share/freya, which is her " +
		"memory and the user's private data;\n" +
		"  · do not add a third-party dependency. The zero-dependency property is " +
		"deliberate and is explained in CLAUDE.md.\n\n" +
		"Finish with the branch name on its own line as `BRANCH: <name>`, or `BRANCH: " +
		"none` if you changed nothing.")
	return b.String()
}

var branchLine = regexp.MustCompile(`(?m)^BRANCH:\s*(\S+)\s*$`)

// extractBranch pulls the branch name out of the report, empty when nothing was
// changed.
func extractBranch(out string) string {
	m := branchLine.FindStringSubmatch(out)
	if len(m) < 2 || strings.EqualFold(m[1], "none") {
		return ""
	}
	return m[1]
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func startOfDay() time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location())
}

// problems is the journal, for the slash command. Set at startup beside the
// engineer, which may be nil when consulting is unavailable but the journal is
// not — noticing failures is worth doing even when nothing can be done about
// them automatically.
var problems *defect.Journal

// problemsCommand shows what has gone wrong and what came of it.
//
// The full verdict is behind an id rather than in the list, because a consult
// produces several paragraphs and twelve of those is not a list anyone reads.
func problemsCommand(rest string) error {
	if problems == nil {
		return fmt.Errorf("the defect journal is not available in this mode")
	}
	if id := strings.TrimSpace(rest); id != "" {
		r, ok := problems.Get(id)
		if !ok {
			return fmt.Errorf("no report %q", id)
		}
		fmt.Printf("  %s\n\n", r.Summary())
		if r.Note != "" {
			fmt.Printf("  what she said: %s\n\n", r.Note)
		}
		for _, f := range r.Failures {
			fmt.Printf("  error: %s\n", f)
		}
		if r.Verdict != "" {
			fmt.Printf("\n%s\n", r.Verdict)
		} else {
			fmt.Println("\n  not looked at yet")
		}
		return nil
	}

	all := problems.All()
	if len(all) == 0 {
		fmt.Println("  nothing noted")
		return nil
	}
	for i, r := range all {
		if i >= 20 {
			fmt.Printf("  …and %d older\n", len(all)-i)
			break
		}
		fmt.Printf("  %s\n", r.Summary())
	}
	fmt.Printf("%s  /problems <id> for the detail%s\n", cDim, cReset)
	return nil
}
