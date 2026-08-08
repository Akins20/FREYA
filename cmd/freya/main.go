// Command freya is the terminal interface to the JARVIS assistant.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/akins/jarvis/internal/agent"
	"github.com/akins/jarvis/internal/claude"
	"github.com/akins/jarvis/internal/config"
	"github.com/akins/jarvis/internal/daemon"
	"github.com/akins/jarvis/internal/defect"
	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/hotkey"
	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/reflect"
	"github.com/akins/jarvis/internal/schedule"
	"github.com/akins/jarvis/internal/sentinel"
	"github.com/akins/jarvis/internal/skills"
	"github.com/akins/jarvis/internal/telemetry"
	"github.com/akins/jarvis/internal/term"
	"github.com/akins/jarvis/internal/voice"
	"github.com/akins/jarvis/internal/work"
)

// ANSI styling, disabled when output is not a terminal or NO_COLOR is set.
var (
	cReset, cBold, cDim, cCyan, cYellow, cRed string
)

func initColors() {
	if os.Getenv("NO_COLOR") != "" {
		return
	}
	info, err := os.Stdout.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	cReset, cBold, cDim = "\033[0m", "\033[1m", "\033[2m"
	cCyan, cYellow, cRed = "\033[36m", "\033[33m", "\033[31m"
}

func main() {
	var (
		oneShot   = flag.String("ask", "", "ask a single question and exit")
		provider  = flag.String("provider", "", "override provider: gemini, anthropic or mock")
		model     = flag.String("model", "", "override model id")
		verbose   = flag.Bool("v", false, "show tool calls and context accounting")
		dryRun    = flag.Bool("dry-run", false, "assess actions without executing them")
		daemonize = flag.Bool("daemon", false, "run headless: watchers and notifications, no chat")
		install   = flag.Bool("install-service", false, "write a systemd user unit and enable it")
		talk      = flag.Bool("talk", false, "trigger one push-to-talk exchange in the running daemon")
		autonomy  = flag.Bool("yes", false, "auto-approve routine actions (writes, commands); still refuses the destructive")
	)
	flag.Parse()
	initColors()

	if *install {
		if err := installService(); err != nil {
			fmt.Fprintf(os.Stderr, "%sfreya: %v%s\n", cRed, err, cReset)
			os.Exit(1)
		}
		return
	}

	// -talk is a one-shot poke at the daemon, bound to a desktop key. It does no
	// setup of its own — it just sends one socket command and exits — so it stays
	// snappy enough to feel like a key press rather than launching a program.
	if *talk {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sfreya: %v%s\n", cRed, err, cReset)
			os.Exit(1)
		}
		reply, err := daemon.Ask(cfg.DataDir, "talk")
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sfreya: no daemon to talk to: %v%s\n", cRed, err, cReset)
			os.Exit(1)
		}
		if !reply.OK {
			fmt.Fprintf(os.Stderr, "%sfreya: %s%s\n", cYellow, reply.Message, cReset)
			os.Exit(1)
		}
		return
	}

	if err := run(*oneShot, *provider, *model, *verbose, *dryRun, *daemonize, *autonomy); err != nil {
		fmt.Fprintf(os.Stderr, "%sfreya: %v%s\n", cRed, err, cReset)
		os.Exit(1)
	}
}

func run(oneShot, providerOverride, modelOverride string, verbose, dryRun, daemonize, autonomous bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if providerOverride != "" {
		cfg.Provider = providerOverride
	}
	if modelOverride != "" {
		cfg.Model = modelOverride
	}
	if verbose {
		cfg.Verbose = true
	}
	if dryRun {
		cfg.DryRun = true
	}

	// Anchor to her working directory before anything opens files, so voice-driven
	// work always starts somewhere findable. Leaving WorkDir empty keeps her where
	// she was launched, which is what the benchmark harness relies on.
	//
	// The process is moved as well as the scope. The scope is what her file and
	// shell tools actually read, but a child process still inherits the process
	// directory, and anything that shells out without an explicit dir should land
	// in the same place rather than wherever the daemon happened to start.
	if cfg.WorkDir != "" {
		if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
			return fmt.Errorf("create work dir %s: %w", cfg.WorkDir, err)
		}
		if err := os.Chdir(cfg.WorkDir); err != nil {
			return fmt.Errorf("enter work dir %s: %w", cfg.WorkDir, err)
		}
	}
	// The foreground conversation's own scope. Background jobs get their own, so
	// one moving folders cannot move another.
	rootScope := skills.NewScope(skills.NewWorkspace(cfg.WorkDir), "", "")

	if cfg.SafetyThreshold != "" {
		llm.SafetyThreshold = cfg.SafetyThreshold
	}

	provider, err := buildProvider(cfg)
	if err != nil {
		return err
	}

	store, err := memory.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	index := memory.BuildIndex(store)
	persona := agent.LoadPersona(cfg.DataDir)
	if cfg.Address != "" {
		persona.Address = cfg.Address
	}

	// Guard gates every action that touches the machine. Its confirmation
	// prompt shares one stdin reader with the REPL so the two never race.
	stdin := bufio.NewReader(os.Stdin)
	auditLog, auditErr := guard.OpenLog(cfg.DataDir)
	if auditErr != nil {
		fmt.Fprintf(os.Stderr, "%swarning: audit log unavailable: %v%s\n", cYellow, auditErr, cReset)
	}
	defer auditLog.Close()

	confirm := confirmPrompt(stdin)
	if !isTerminal() {
		// A piped session has nobody to ask, so it must refuse rather than assume.
		confirm = autoDenyConfirm()
	}
	g := guard.New(confirm, auditLog)
	g.DryRun = cfg.DryRun
	g.ProtectedPaths = []string{cfg.DataDir}

	// Autonomy: how much she does without stopping to ask.
	//
	// The default guard asks before anything that writes a file, runs a command,
	// or moves something — sensible when a human is at the keyboard to answer.
	// But over voice, and in any automated run, there is nobody to answer, so
	// those routine actions were simply denied, and she could not do the work
	// she was handed. That is the "she narrates and does nothing" failure at its
	// root. The user's standing rule is the right dial: full autonomy for
	// ordinary work, a confirmation only before something destructive. So when
	// asked to run autonomously — the -yes flag, and the background daemon — she
	// auto-approves up to RiskMedium (writes, commands, moves, browser) and only
	// RiskHigh (deleting data, touching system paths, escalating to root) still
	// needs a yes she cannot get, and so is safely refused.
	if autonomous || daemonize {
		g.AutoApprove = guard.RiskMedium
	}

	reg := skills.New()
	skills.RegisterSystem(reg)
	skills.RegisterShell(reg, g)
	skills.RegisterMemory(reg, store, index)
	skills.RegisterWeb(reg, os.Getenv("SERPER_API_KEY"))
	skills.RegisterDev(reg, cfg.ProjectsDir)
	skills.RegisterDesktop(reg, g)
	places, placesErr := skills.RegisterPlaces(reg, cfg.DataDir)
	if placesErr != nil {
		return placesErr
	}
	skills.RegisterFiles(reg, g, places)
	skills.RegisterDocWriting(reg, g)

	// Terminal sessions outlive the turn that created them, which is what makes
	// long-running work possible.
	terminals := term.NewManager()
	defer terminals.CloseAll()
	skills.RegisterTerminal(reg, g, terminals)

	// Claude Code as a subordinate for heavy engineering work.
	claudeClient := claude.New(cfg.DataDir)
	skills.RegisterClaude(reg, g, claudeClient)
	skills.RegisterClaudeAdvice(reg, g, claudeClient)

	browserTabs := skills.NewTabs()
	defer browserTabs.CloseAll()
	skills.RegisterBrowser(reg, g, browserTabs)
	if seer, ok := provider.(llm.VisionAnalyzer); ok {
		skills.RegisterVision(reg, seer)
	}
	if err := skills.RegisterNotes(reg, cfg.DataDir); err != nil {
		return err
	}

	// Self-scheduling: a task she sets for her future self, which the daemon runs
	// when due. Opened here so the tool can write it and the daemon poll (below)
	// can read it. A failure to open just disables scheduling, never the session.
	selfTasks, schedErr := schedule.Open(cfg.DataDir)
	if schedErr != nil {
		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "%sschedule: %v%s\n", cDim, schedErr, cReset)
		}
	} else {
		skills.RegisterSchedule(reg, selfTasks)
	}

	// Voice must be constructed before the agent so its control skill can be
	// registered alongside the others.
	vs, voiceErr := setupVoice(cfg, provider)
	if voiceErr == nil {
		skills.RegisterVoice(reg, vs)
	}

	// The way back from a narrowed tool set. Registered last, so it can see every
	// other tool — it searches the registry, and anything registered after it
	// would be invisible to the search.
	skills.RegisterFinder(reg)

	builder := memory.NewContextBuilder(store, index, persona.Prompt(reg.Names()))
	// Everything she has, by name, in the cached prefix. Snapshot taken here
	// because registration is finished: the catalogue must be the whole registry
	// or it teaches her a blind spot instead of curing one.
	builder.Catalogue = reg.Catalogue()
	// Last activity grounds "you haven't been here in a while".
	if turns := store.Turns(); len(turns) > 0 {
		builder.LastSeen = turns[len(turns)-1].Timestamp
	}
	a := agent.New(provider, reg, store, builder, persona)
	a.Guard = g
	a.ThinkBudget = cfg.ThinkBudget
	a.Scope = rootScope

	// What else is on the user's plate, for the quiet-moment follow-up to weigh:
	// tasks she scheduled for herself and deadlines coming up within a few days.
	// Composed here so the agent stays decoupled from how either is stored.
	deadlineFeed := skills.Deadlines(store, cfg.DataDir)
	a.StateSummary = func() string {
		var parts []string
		if selfTasks != nil {
			if pending, err := selfTasks.Pending(); err == nil {
				for _, t := range pending {
					parts = append(parts, fmt.Sprintf("- you scheduled: %q (runs in %s)",
						t.Prompt, time.Until(t.Due).Round(time.Minute)))
				}
			}
		}
		if items, err := deadlineFeed(); err == nil {
			for _, it := range items {
				if d := time.Until(it.Deadline); d > 0 && d < 3*24*time.Hour {
					parts = append(parts, fmt.Sprintf("- due in %s: %s", d.Round(time.Minute), it.Text))
				}
			}
		}
		return strings.Join(parts, "\n")
	}

	// What the user appears to be doing — the focused window, classified by kind
	// (browser page, terminal directory, folder, file), with a short trail of
	// recent switches. In the daemon this is sampled frequently in the background
	// (below) so context is fresh; the tracker never speaks, it only grounds her
	// proactive choices. A one-shot read is the fallback when nothing samples it.
	activity := &skills.ActivityTracker{}
	a.UserActivity = func() string {
		if s := activity.Current(); s != "" {
			return s
		}
		return skills.CurrentActivity()
	}
	// Depth for browser and folder windows: on a change, screenshot that window
	// and have the vision model read what's actually there — the page, the folder's
	// files — not just the title. Bounded (only those two kinds, only on a change),
	// and skippable with FREYA_ACTIVITY_DEPTH=off since it costs a vision call and
	// puts the window's contents in front of the model.
	if seer, ok := provider.(llm.VisionAnalyzer); ok && os.Getenv("FREYA_ACTIVITY_DEPTH") != "off" {
		activity.Deepen = func(kind, _ string) string { return deepenActivity(seer, kind) }
	}

	// Telemetry. Opening it can fail — a full disk, a read-only data directory
	// — and that must not stop a session: a recorder that failed to open still
	// accepts events and discards them, so nothing downstream needs to care.
	tel, telErr := telemetry.Open(cfg.DataDir)
	if telErr != nil && cfg.Verbose {
		fmt.Fprintf(os.Stderr, "%stelemetry: %v%s\n", cDim, telErr, cReset)
	}
	defer tel.Close()
	a.Telemetry = tel
	g.Telemetry = tel

	// Reflection lenses: several readings of the same memory, so what surfaces
	// is experience rather than a single lookup.
	refl := reflect.New()
	refl.Add(&reflect.ConsequenceLens{Deadlines: deadlinesFrom(store)})
	a.Reflector = refl
	builder.Insights = func() []string {
		var out []string
		for _, i := range refl.Pending() {
			out = append(out, i.Summary)
		}
		return out
	}
	skills.RegisterReflection(reg, refl)
	// Interim narration: shown at once, and spoken when voice is on, so a slow
	// lookup is filled with speech rather than silence.
	a.OnInterim = func(text string) {
		fmt.Printf("%s%s%s\n", cDim, text, cReset)
	}

	// The thinking window — her reasoning before each step, shown so her decisions
	// are legible and inspectable. Rendered distinctly (a thought bubble, dim and
	// indented) so it never reads as something she said. Printed to stdout in the
	// REPL and, in the daemon, carried to the journal by the log wrapper below.
	a.OnThought = func(text string) {
		for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				fmt.Printf("%s  💭 %s%s\n", cDim, line, cReset)
			}
		}
	}

	if cfg.Verbose {
		a.OnTool = func(event, name, detail string) {
			switch event {
			case "start":
				fmt.Printf("%s  → %s %s%s\n", cDim, name, detail, cReset)
			case "error":
				fmt.Printf("%s  ✗ %s: %s%s\n", cRed, name, detail, cReset)
			default:
				fmt.Printf("%s  ✓ %s%s\n", cDim, name, cReset)
			}
		}
	}

	// Proactivity: watchers run in the background and only speak when an
	// observation clears the salience bar.
	sen := setupSentinel(cfg, reg, store)
	skills.RegisterProactive(reg, sen)
	skills.RegisterTelemetry(reg, cfg.DataDir)
	skills.RegisterSkillbook(reg)

	// Ctrl-C cancels the in-flight request rather than killing the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Background work. Registered before anything can call it and shut down on
	// the way out, so a job never outlives the process that would have reported
	// it — an orphaned job is work the user is never told about.
	// Show her the tools the request calls for, rather than all ninety-six. On by
	// default: everything stays executable, a tool she names that was not offered
	// still runs and is counted, and FREYA_TOOL_ROUTING=off turns it off entirely.
	a.RouteTools = !strings.EqualFold(strings.TrimSpace(cfg.ToolRouting), "off")

	// The failures write themselves down, and the bad ones get looked at. Wired
	// before anything can fail, so nothing is missed during startup.
	defects, derr := defect.Open(cfg.DataDir)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "%swarning: defect journal unavailable: %v%s\n", cYellow, derr, cReset)
	}
	problems = defects
	eng := newEngineer(defects, claudeClient, cfg.SourceDir, cfg.ProjectsDir, cfg.DataDir)
	if defects != nil {
		// Filing goes through the engineer when there is one and straight to the
		// journal otherwise: noticing a failure is worth doing even on a machine
		// where nothing can be done about it automatically.
		a.OnFailure = func(f agent.Failure) {
			if eng != nil {
				eng.file(f)
				return
			}
			_, _ = defects.File(defect.Report{
				Kind: defect.Kind(f.Kind), Goal: f.Goal, Attempts: f.Attempts,
				Failures: f.Failures, Trail: f.Trail, Exchange: f.Exchange,
			})
		}
		skills.RegisterDefects(reg, defects)
	}

	jobs = newWorkManager(ctx, a, store, cfg)
	skills.RegisterWork(reg, jobs)
	defer jobs.Shutdown(10 * time.Second)

	// A finished job waits for a natural opening in the conversation. This is what
	// turns "job2 completed" into "oh, and those quizzes are done" — the report is
	// handed to her as context for her next turn, in her own words.
	a.PendingWork = func() string { return brief(reports.drain()) }
	if vs != nil && voiceErr == nil {
		go watchForAnOpening(ctx, func(text string) bool {
			return vs.speak(context.Background(), voice.Background, text)
		})
	}

	if daemonize {
		// Headless, but not inert. This is the process that is running when
		// nobody is at the keyboard, so it is the one that has to answer when
		// spoken to and the one that shows whether she is awake. It owns the
		// memory store until an interactive session takes it — see the yield
		// and resume handlers below, and Store.Suspend.
		d := daemon.New(cfg.DataDir, sen)

		ind := newIndicator(ctx)
		defer ind.close()

		// Only in the daemon. In a REPL session the user is sitting right there and
		// can ask; these loops exist for the hours when nobody is.
		eng.run(ctx)

		if vs != nil {
			vs.indicator = ind
			// Over voice, a destructive action is not silently denied — she asks
			// about it aloud and acts on the spoken answer. Routine work never
			// reaches here (auto-approved); only RiskHigh does.
			if voiceErr == nil {
				g.Confirm = voiceConfirm(vs)
			}
			d.Speak = func(text string) {
				go func() { _ = vs.session.Speak(context.Background(), text) }()
			}
			// The desktop key binding pokes the socket, which calls this.
			if voiceErr == nil {
				d.Talk = func() { pushToTalk(ctx, a, vs) }

				// In the daemon, voice is push-to-talk, so her mid-action
				// narration must be *spoken*, not just printed to a terminal
				// nobody is watching. Without this she goes to work in silence:
				// you ask her to do something, she says nothing, and things
				// start happening. Speaking the interim line is how she tells
				// you "on it, opening your portal" before she opens it.
				printInterim := a.OnInterim
				a.OnInterim = func(text string) {
					printInterim(text)
					go func() { _ = vs.session.Speak(context.Background(), text) }()
				}
			}
		}

		listening := "no voice"
		switch {
		case voiceErr == nil && vs != nil && wakeDisabled(cfg):
			// Asked for, not merely failed. Always-on listening records the room
			// almost continuously, so it must be something the user chose rather
			// than something that arrives when an unrelated setting is fixed.
			listening = "wake word off (FREYA_WAKE=off), push-to-talk only"
		case voiceErr == nil && vs != nil:
			if err := startWakeListening(ctx, a, vs, ind); err != nil {
				listening = "wake word unavailable: " + err.Error()
			} else {
				listening = `listening for "Freya"`
			}
		}
		if voiceErr != nil {
			listening = "no voice: " + voiceErr.Error()
		}

		// Global push-to-talk. Ctrl+Space anywhere starts one spoken exchange.
		// This is the primary way to talk to her on hardware where always-on
		// wake detection is unreliable: a deliberate key press needs no onset
		// detection and cannot be triggered by a silent room. The press runs the
		// exchange in its own goroutine so the grab keeps listening through it.
		if vs != nil && voiceErr == nil {
			if grab, err := hotkey.OpenCtrlSpace(); err != nil {
				fmt.Fprintf(os.Stderr, "push-to-talk hotkey unavailable: %v\n", err)
			} else {
				defer grab.Close()
				go func() {
					err := grab.Listen(ctx, func() {
						fmt.Printf("%s  ⌨ Ctrl+Space%s\n", cDim, cReset)
						go pushToTalk(ctx, a, vs)
					})
					if err != nil && ctx.Err() == nil {
						fmt.Fprintf(os.Stderr, "push-to-talk hotkey stopped: %v\n", err)
					}
				}()
				listening += ", push-to-talk on Ctrl+Space"
			}
		}

		// Handover. A session that starts while the daemon holds the store must
		// take it, or both write to an append-only archive at once. daemonActive
		// tracks who holds it, so the self-task poll below never runs a turn while
		// a session owns the store.
		daemonActive.Store(true)
		d.Yield = func() {
			// Stop taking new work first, so nothing starts during the handover.
			daemonActive.Store(false)
			if vs != nil && vs.listener != nil {
				vs.listener.Stop()
			}
			ind.idle()

			// Then wait for the turn already in flight. Suspending underneath it
			// makes its next AppendTurn return ErrSuspended, which fails an
			// exchange that was going perfectly well — the user asked for
			// something, she did the work, and the reply is lost at the last step
			// because somebody opened a terminal. Background jobs need no wait:
			// they write to their own branch and hold their report in memory.
			deadline := time.Now().Add(handoverGrace)
			for currentTurn() != nil && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if currentTurn() != nil {
				fmt.Fprintf(os.Stderr,
					"handing over with an exchange still running; its reply may not be archived\n")
			}

			if err := store.Suspend(); err != nil {
				fmt.Fprintf(os.Stderr, "suspend memory: %v\n", err)
			}
		}
		d.Resume = func() {
			if err := store.Resume(); err != nil {
				fmt.Fprintf(os.Stderr, "resume memory: %v\n", err)
				return
			}
			daemonActive.Store(true)
			if vs != nil && voiceErr == nil {
				_ = startWakeListening(ctx, a, vs, ind)
			}
		}

		// The self-task poll: the mechanism that turns "I'll check back in ten
		// minutes" into something that actually happens. It runs due tasks THROUGH
		// the agent — so each ends in work and a spoken result, not a notification.
		//
		// A due task is a background Job now, so it runs on its own branched
		// conversation, in its own scope, bounded by the same pool of two, and no
		// longer waits for the user to stop talking. It used to stand down
		// whenever a conversation was in flight, which quietly turned "check back
		// in ten minutes" into "check back once you stop typing".
		//
		// What it must still respect is ownership of the archive: it only runs
		// while daemonActive, so it never starts work a session's store would
		// refuse. And DueNow marks tasks as run, so it stays behind the pool check
		// — a task is never marked-and-lost, it simply waits for the next tick.
		if selfTasks != nil {
			go func() {
				ticker := time.NewTicker(20 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if !daemonActive.Load() {
							continue // a session holds the store
						}
						if jobs != nil && jobs.Active() >= work.DefaultConcurrency {
							continue // the pool is full; the task stays due
						}
						due, err := selfTasks.DueNow()
						if err == nil {
							for _, t := range due {
								startSelfTask(t)
							}
						}
					}
				}
			}()
		}

		// Ambient activity sampling: read the focused window every 45s so she keeps
		// a fresh, classified sense of what the user is doing (a browser page, a
		// terminal's directory, a folder, a file) with a short trail of recent
		// switches. Passive by design — it only feeds context, it never speaks.
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			activity.Sample() // seed immediately so context isn't empty at first
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					activity.Sample()
				}
			}
		}()

		// Quiet-moment re-engagement. After the conversation has been quiet for a
		// while — but before it is clearly over — she reviews the recent exchange
		// and, if there is a genuine loose end (a task left hanging, a promise to
		// report back), follows it up in her own voice. It happens at most once per
		// lull, defaults to silence, and only when chattiness invites it — this is
		// her noticing what was left undone, not filling the air.
		if cfg.FollowupAfter > 0 && sentinel.ParseChattiness(cfg.Chattiness) != sentinel.ChattyQuiet {
			go func() {
				lullMin := cfg.FollowupAfter           // a pause, not a breath (tunable)
				lullMax := lullMin + 20*time.Minute    // beyond this the conversation is over, not paused
				tick := lullMin / 2                    // poll often enough to catch the lull promptly
				if tick > 2*time.Minute || tick <= 0 { // but no slower than every couple of minutes
					tick = 2 * time.Minute
				}
				var handled time.Time // the last-user-turn time already considered
				ticker := time.NewTicker(tick)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if !daemonActive.Load() {
							continue // a session holds the store
						}
						last := lastUserTurnTime(store.Turns())
						if last.IsZero() {
							continue
						}
						since := time.Since(last)
						if since < lullMin || since > lullMax {
							continue
						}
						if !last.After(handled) {
							continue // this lull was already considered
						}
						if currentTurn() != nil {
							continue // she is mid-conversation
						}
						if !bgBusy.CompareAndSwap(false, true) {
							continue // another background task is running
						}
						line, err := a.Followup(ctx)
						bgBusy.Store(false)
						handled = last // one consideration per lull, whether she speaks or passes
						if err != nil || line == "" {
							continue
						}
						fmt.Printf("%s  ☎ %s%s\n", cDim, line, cReset)
						if _, aerr := store.AppendTurn(memory.Turn{Role: "assistant", Text: line}); aerr != nil {
							fmt.Fprintf(os.Stderr, "archive follow-up: %v\n", aerr)
						}
						if vs != nil && voiceErr == nil {
							_ = vs.session.Speak(context.Background(), line)
						}
					}
				}
			}()
		}

		fmt.Printf("%sFreya daemon: %d watchers, chattiness %s, %s, socket %s%s\n",
			cDim, len(sen.Watchers()), sen.Chattiness, listening,
			daemon.SocketPath(cfg.DataDir), cReset)
		return d.Run(ctx)
	}

	// A session defers to a running daemon rather than starting a second set of
	// watchers, so the same warning cannot arrive from two directions. It also
	// takes the memory store: the daemon has it open for writing, and a person
	// at the keyboard outranks a background process.
	if daemon.Running(cfg.DataDir) {
		sen.Stop()
		if _, err := daemon.Ask(cfg.DataDir, "yield"); err != nil {
			fmt.Fprintf(os.Stderr, "%scould not take memory from the daemon: %v%s\n",
				cYellow, err, cReset)
		} else {
			// Handing it back on the way out is what lets her keep listening
			// once the terminal is closed.
			defer func() { _, _ = daemon.Ask(cfg.DataDir, "resume") }()
			// The daemon closed the archive after we opened it; reopening picks
			// up anything it wrote in between.
			if err := store.Resume(); err != nil {
				fmt.Fprintf(os.Stderr, "%sreopen memory: %v%s\n", cYellow, err, cReset)
			}
		}
		fmt.Printf("%sdaemon is running — deferring background watching to it%s\n", cDim, cReset)
	}

	if oneShot != "" {
		return ask(ctx, a, cfg, oneShot)
	}

	// Voice is optional: a missing recorder or synthesiser must not stop a
	// text session.
	if voiceErr != nil {
		if cfg.Voice {
			fmt.Fprintf(os.Stderr, "%svoice unavailable: %v%s\n", cYellow, voiceErr, cReset)
		}
		vs = nil
	} else if cfg.Voice {
		vs.enabled = true
	}
	if vs != nil {
		// Speak the interim line too, so voice mode has no dead air.
		spoken := a.OnInterim
		a.OnInterim = func(text string) {
			spoken(text)
			if vs.enabled {
				go func() { _ = vs.session.Speak(context.Background(), text) }()
			}
		}
	}

	sen.Notify = notifier(vs, true)
	sen.Start(ctx)
	defer sen.Stop()

	// The screen light. Red while she is merely running, blue when the wake
	// word is armed, amber while she is working. Absent entirely without X11,
	// which is not an error.
	ind := newIndicator(ctx)
	defer ind.close()
	if vs != nil {
		vs.indicator = ind
	}

	return repl(ctx, a, cfg, store, index, persona, vs, stdin, sen, ind)
}

func buildProvider(cfg *config.Config) (llm.Provider, error) {
	switch strings.ToLower(cfg.Provider) {
	case "gemini":
		p, err := llm.NewGemini(cfg.GeminiKey, cfg.Model)
		if err != nil {
			if errors.Is(err, llm.ErrNoCredentials) {
				return nil, fmt.Errorf("provider gemini needs GEMINI_API_KEY; " +
					"set it in .env or run with -provider mock")
			}
			return nil, err
		}
		return p, nil
	case "anthropic", "claude":
		p, err := llm.NewAnthropic(cfg.AnthropicKey, cfg.Model)
		if err != nil {
			if errors.Is(err, llm.ErrNoCredentials) {
				return nil, fmt.Errorf("provider anthropic needs ANTHROPIC_API_KEY; " +
					"set it in .env or run with -provider mock")
			}
			return nil, err
		}
		return p, nil
	case "mock", "offline":
		return llm.NewMock(), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want gemini, anthropic or mock)", cfg.Provider)
	}
}

func ask(ctx context.Context, a *agent.Agent, cfg *config.Config, input string) error {
	res, err := a.Ask(ctx, input)
	if err != nil {
		return err
	}
	fmt.Println(res.Reply)
	if cfg.Verbose {
		printSnapshot(res)
	}
	return nil
}

func repl(ctx context.Context, a *agent.Agent, cfg *config.Config,
	store *memory.Store, index *memory.Index, persona agent.Persona,
	vs *voiceState, stdin *bufio.Reader, sen *sentinel.Sentinel, ind *indicator) error {

	fmt.Printf("%s%s%s — %s, %d skills, %d turns remembered\n",
		cBold, persona.Name, cReset, a.Provider.Name(), len(a.Skills.Names()), store.TurnCount())
	if vs != nil {
		fmt.Printf("%svoice: %s%s\n", cDim, vs.describe(), cReset)
	}
	fmt.Printf("%stype /help for commands, /quit to leave%s\n\n", cDim, cReset)

	for {
		fmt.Printf("%s❯%s ", cCyan, cReset)
		raw, readErr := stdin.ReadString('\n')
		if readErr != nil && raw == "" {
			fmt.Println()
			return nil
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			// In voice mode a bare Enter is the push-to-talk trigger.
			if vs != nil && vs.enabled {
				if err := spokenTurn(ctx, a, cfg, vs); err != nil {
					fmt.Printf("%s%v%s\n", cRed, err, cReset)
				}
			}
			continue
		}

		if strings.HasPrefix(line, "/") {
			quit, err := command(ctx, line, a, cfg, store, index, vs, sen, ind)
			if err != nil {
				fmt.Printf("%s%v%s\n", cRed, err, cReset)
			}
			if quit {
				return nil
			}
			continue
		}

		start := time.Now()
		done := ind.working()
		res, err := a.Ask(ctx, line)
		done()
		if err != nil {
			if ctx.Err() != nil {
				fmt.Printf("\n%sinterrupted%s\n", cYellow, cReset)
				return nil
			}
			fmt.Printf("%s%v%s\n", cRed, err, cReset)
			continue
		}

		fmt.Printf("\n%s\n\n", res.Reply)
		if cfg.Verbose {
			fmt.Printf("%s  %.1fs · %d round(s)%s\n", cDim, time.Since(start).Seconds(), res.Rounds, cReset)
			printSnapshot(res)
			fmt.Println()
		}
	}
}

// command handles /-prefixed REPL directives. Returns true to exit.
func command(ctx context.Context, line string, a *agent.Agent, cfg *config.Config,
	store *memory.Store, index *memory.Index, vs *voiceState,
	sen *sentinel.Sentinel, ind *indicator) (bool, error) {

	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "/quit", "/exit", "/q":
		return true, nil

	case "/stats":
		return false, showStats(cfg, rest, ind)

	case "/help":
		fmt.Print(`
  /persona                 show current personality
  /persona <traits>        set traits, comma or space separated
  /persona address <name>  set what she calls you
  /persona custom <text>   append a freeform instruction
  /persona reset           restore defaults
  /traits                  list available traits
  /memory                  memory statistics
  /stats                   what has run, and what it cost
  /stats daily             usage broken down by day
  /stats rates             the price table the estimates assume
  /context                 token accounting for the last exchange
  /skills                  list registered skills
  /voice                   voice status
  /voice on | off          spoken mode (Enter = push to talk)
  /voice enroll [name]     record a voiceprint
  /voice test              score your voice against the print
  /voice policy off|warn|enforce
  /voice threshold <0-1>   tune acceptance
  /voice say <text>        speak something
  /voice listen [2h|forever]  wake-word listening ("Freya" / "hey Freya")
  /voice deaf              stop listening
  /voice ack [chime|speak|both|silent]
  /voice chime             play the wake sound
  /voice style             show delivery settings
  /voice pace <name>       very slow .. very fast
  /voice pitch <name>      very low .. very high
  /voice tone <list>       e.g. casual, dry, amused
  /voice style voice Kore  pick a voice preset
  /voice style reset       restore defaults
  /voice voices            list voices, paces, tones
  /reflect                 lenses and queued insights
  /daemon                  background service status
  /proactive               status and queued observations
  /proactive quiet|balanced|companion
  /proactive check         drain the queue now
  /jobs [stop <id>|stop all]  background tasks and how far they've got
  /problems [<id>]         software problems noted, and what came of them
  /tools                   which tools this request would offer, and what routing missed
  /audit                   recent actions and their outcomes
  /verbose                 toggle tool tracing
  /quit                    exit

`)
		return false, nil

	case "/jobs":
		return false, jobsCommand(rest)

	case "/problems":
		return false, problemsCommand(rest)

	case "/tools":
		return false, toolsCommand(rest, a)

	case "/traits":
		fmt.Printf("  %s\n", strings.Join(agent.TraitNames(), ", "))
		return false, nil

	case "/persona":
		return false, personaCommand(rest, a, cfg)

	case "/voice":
		if vs == nil {
			return false, fmt.Errorf("voice is unavailable on this machine; " +
				"install sox and espeak-ng")
		}
		_, err := voiceCommand(ctx, rest, vs, a)
		return false, err

	case "/skills":
		names := a.Skills.Names()
		fmt.Printf("  %d skills: %s\n", len(names), strings.Join(names, ", "))
		return false, nil

	case "/reflect":
		if a.Reflector == nil {
			return false, fmt.Errorf("reflection is not running")
		}
		fmt.Printf("  lenses: %s\n", strings.Join(a.Reflector.Lenses(), ", "))
		pending := a.Reflector.Peek()
		if len(pending) == 0 {
			fmt.Println("  nothing queued")
			return false, nil
		}
		for _, i := range pending {
			fmt.Printf("  %s\n", i.Describe())
		}
		return false, nil

	case "/daemon":
		if !daemon.Running(cfg.DataDir) {
			fmt.Println("  not running — start it with:  freya -daemon")
			fmt.Println("  or install it as a service:   freya -install-service")
			return false, nil
		}
		reply, err := daemon.Ask(cfg.DataDir, "status")
		if err != nil {
			return false, err
		}
		if reply.Status != nil {
			fmt.Printf("  %s\n", reply.Status.Describe())
		}
		if pending, err := daemon.Ask(cfg.DataDir, "pending"); err == nil {
			if len(pending.Observations) == 0 {
				fmt.Println("  nothing queued")
			}
			for _, o := range pending.Observations {
				fmt.Printf("    %s\n", o.Describe())
			}
		}
		return false, nil

	case "/proactive":
		return false, proactiveCommand(rest, sen, cfg)

	case "/audit":
		if a.Guard == nil || a.Guard.Audit == nil {
			return false, fmt.Errorf("no audit log configured")
		}
		records, err := a.Guard.Audit.Recent(20)
		if err != nil {
			return false, err
		}
		if len(records) == 0 {
			fmt.Println("  nothing recorded yet")
			return false, nil
		}
		for i := len(records) - 1; i >= 0; i-- {
			fmt.Printf("  %s\n", records[i].Summary())
		}
		return false, nil

	case "/memory":
		facts := store.Facts()
		pinned := 0
		for _, f := range facts {
			if f.Pinned {
				pinned++
			}
		}
		fmt.Printf("  %d turns · %d facts (%d pinned) · %d episodes · %d indexed docs\n  %s\n",
			store.TurnCount(), len(facts), pinned, len(store.Episodes()), index.Size(), store.Dir())
		return false, nil

	case "/context":
		b := a.Builder.Budget
		fmt.Printf("  window %d, usable %d after reserve\n", b.Window, b.Usable())
		fmt.Printf("  identity %d · facts %d · episodes %d · working %d · retrieved %d\n",
			b.Identity, b.Facts, b.Episodes, b.Working, b.Retrieved)
		return false, nil

	case "/verbose":
		cfg.Verbose = !cfg.Verbose
		if cfg.Verbose {
			a.OnTool = func(event, name, detail string) {
				fmt.Printf("%s  → %s %s%s\n", cDim, name, detail, cReset)
			}
		} else {
			a.OnTool = nil
		}
		fmt.Printf("  verbose %v\n", cfg.Verbose)
		return false, nil

	default:
		return false, fmt.Errorf("unknown command %q — try /help", cmd)
	}
}

func personaCommand(rest string, a *agent.Agent, cfg *config.Config) error {
	if rest == "" {
		fmt.Printf("  %s\n", a.Persona.Describe())
		return nil
	}

	sub, arg, _ := strings.Cut(rest, " ")
	arg = strings.TrimSpace(arg)

	switch sub {
	case "reset":
		a.Persona = agent.DefaultPersona()
	case "address":
		a.Persona.Address = arg
	case "custom":
		a.Persona.Custom = arg
	default:
		// Everything else is treated as a trait list.
		fields := strings.FieldsFunc(rest, func(r rune) bool {
			return r == ',' || r == ' '
		})
		if unknown := a.Persona.SetTraits(fields); len(unknown) > 0 {
			fmt.Printf("%s  ignored unknown traits: %s%s\n",
				cYellow, strings.Join(unknown, ", "), cReset)
		}
	}

	if err := agent.SavePersona(cfg.DataDir, a.Persona); err != nil {
		return fmt.Errorf("save persona: %w", err)
	}
	fmt.Printf("  %s\n", a.Persona.Describe())
	return nil
}

func printSnapshot(res *agent.Result) {
	s := res.Snapshot
	// Rounds ride on the accounting line, which every verbose path prints — the
	// one-shot -ask path included. Without it the benchmark harness, which drives
	// exactly that path, read every run as "0 rounds" and any measure of wasted
	// effort built on it was silently always zero.
	fmt.Printf("%s  context: %d tok (identity %d · facts %d · episodes %d · working %d/%d turns · recalled %d) · %d round(s)%s\n",
		cDim, s.TotalTokens, s.IdentityTokens, s.FactTokens, s.EpisodeTokens,
		s.WorkingTokens, s.WorkingTurns, s.RetrievedCount, res.Rounds, cReset)
	if len(res.ToolCalls) > 0 {
		fmt.Printf("%s  tools: %s%s\n", cDim, strings.Join(res.ToolCalls, ", "), cReset)
	}
}
