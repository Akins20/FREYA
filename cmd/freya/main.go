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
	"github.com/akins/jarvis/internal/config"
	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/reflect"
	"github.com/akins/jarvis/internal/sentinel"
	"github.com/akins/jarvis/internal/skills"
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
		oneShot  = flag.String("ask", "", "ask a single question and exit")
		provider = flag.String("provider", "", "override provider: gemini, anthropic or mock")
		model    = flag.String("model", "", "override model id")
		verbose  = flag.Bool("v", false, "show tool calls and context accounting")
		dryRun   = flag.Bool("dry-run", false, "assess actions without executing them")
	)
	flag.Parse()
	initColors()

	if err := run(*oneShot, *provider, *model, *verbose, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "%sfreya: %v%s\n", cRed, err, cReset)
		os.Exit(1)
	}
}

func run(oneShot, providerOverride, modelOverride string, verbose, dryRun bool) error {
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

	reg := skills.New()
	skills.RegisterSystem(reg)
	skills.RegisterShell(reg, g)
	skills.RegisterMemory(reg, store, index)
	skills.RegisterWeb(reg, os.Getenv("SERPER_API_KEY"))
	skills.RegisterDev(reg, cfg.ProjectsDir)
	skills.RegisterDesktop(reg, g)
	skills.RegisterFiles(reg, g)
	if err := skills.RegisterNotes(reg, cfg.DataDir); err != nil {
		return err
	}

	// Voice must be constructed before the agent so its control skill can be
	// registered alongside the others.
	vs, voiceErr := setupVoice(cfg, provider)
	if voiceErr == nil {
		skills.RegisterVoice(reg, vs)
	}

	builder := memory.NewContextBuilder(store, index, persona.Prompt(reg.Names()))
	// Last activity grounds "you haven't been here in a while".
	if turns := store.Turns(); len(turns) > 0 {
		builder.LastSeen = turns[len(turns)-1].Timestamp
	}
	a := agent.New(provider, reg, store, builder, persona)
	a.Guard = g

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

	// Ctrl-C cancels the in-flight request rather than killing the process.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	sen.Notify = notifier(vs, true)
	sen.Start(ctx)
	defer sen.Stop()

	return repl(ctx, a, cfg, store, index, persona, vs, stdin, sen)
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
	vs *voiceState, stdin *bufio.Reader, sen *sentinel.Sentinel) error {

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
			quit, err := command(ctx, line, a, cfg, store, index, vs, sen)
			if err != nil {
				fmt.Printf("%s%v%s\n", cRed, err, cReset)
			}
			if quit {
				return nil
			}
			continue
		}

		start := time.Now()
		res, err := a.Ask(ctx, line)
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
	sen *sentinel.Sentinel) (bool, error) {

	cmd, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)

	switch cmd {
	case "/quit", "/exit", "/q":
		return true, nil

	case "/help":
		fmt.Print(`
  /persona                 show current personality
  /persona <traits>        set traits, comma or space separated
  /persona address <name>  set what she calls you
  /persona custom <text>   append a freeform instruction
  /persona reset           restore defaults
  /traits                  list available traits
  /memory                  memory statistics
  /context                 token accounting for the last exchange
  /skills                  list registered skills
  /voice                   voice status
  /voice on | off          spoken mode (Enter = push to talk)
  /voice enroll [name]     record a voiceprint
  /voice test              score your voice against the print
  /voice policy off|warn|enforce
  /voice threshold <0-1>   tune acceptance
  /voice say <text>        speak something
  /voice style             show delivery settings
  /voice pace <name>       very slow .. very fast
  /voice pitch <name>      very low .. very high
  /voice tone <list>       e.g. casual, dry, amused
  /voice style voice Kore  pick a voice preset
  /voice style reset       restore defaults
  /voice voices            list voices, paces, tones
  /reflect                 lenses and queued insights
  /proactive               status and queued observations
  /proactive quiet|balanced|companion
  /proactive check         drain the queue now
  /audit                   recent actions and their outcomes
  /verbose                 toggle tool tracing
  /quit                    exit

`)
		return false, nil

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
	fmt.Printf("%s  context: %d tok (identity %d · facts %d · episodes %d · working %d/%d turns · recalled %d)%s\n",
		cDim, s.TotalTokens, s.IdentityTokens, s.FactTokens, s.EpisodeTokens,
		s.WorkingTokens, s.WorkingTurns, s.RetrievedCount, cReset)
	if len(res.ToolCalls) > 0 {
		fmt.Printf("%s  tools: %s%s\n", cDim, strings.Join(res.ToolCalls, ", "), cReset)
	}
}
