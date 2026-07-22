package main

import (
	"context"
	"fmt"
	"strings"

	"time"

	"github.com/akins/jarvis/internal/config"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/reflect"
	"github.com/akins/jarvis/internal/sentinel"
	"github.com/akins/jarvis/internal/skills"
)

// setupSentinel builds the proactivity engine and its watchers.
func setupSentinel(cfg *config.Config, reg *skills.Registry, store *memory.Store) *sentinel.Sentinel {
	s := sentinel.New(sentinel.ParseChattiness(cfg.Chattiness), nil)

	s.Add(sentinel.DiskWatcher{})
	s.Add(sentinel.BatteryWatcher{})
	s.Add(sentinel.MemoryWatcher{})
	s.Add(sentinel.GitWatcher{Root: cfg.ProjectsDir})
	s.Add(sentinel.ReminderWatcher{
		DataDir: cfg.DataDir,
		Due:     skills.DueReminders(cfg.DataDir),
	})
	s.Add(sentinel.TemporalWatcher{SessionStart: time.Now()})
	s.Add(sentinel.ProcessWatcher{})
	s.Add(&sentinel.IdleWatcher{})
	s.Add(sentinel.CommitmentWatcher{Commitments: skills.Commitments(store)})
	return s
}

// notifier prints an observation the sentinel decided to raise.
//
// It writes above the prompt rather than replacing it, so an interruption never
// costs the user a half-typed line.
func notifier(vs *voiceState, speak bool) func(sentinel.Observation) {
	return func(o sentinel.Observation) {
		marker, colour := "·", cDim
		switch o.Urgency {
		case sentinel.UrgencyCritical:
			marker, colour = "!!", cRed
		case sentinel.UrgencyImportant:
			marker, colour = "!", cYellow
		case sentinel.UrgencyNotable:
			marker, colour = "•", cCyan
		}
		fmt.Printf("\n%s%s %s%s\n%s❯%s ", colour, marker, o.Summary, cReset, cCyan, cReset)

		// Critical things are worth saying out loud when voice is active.
		if speak && vs != nil && vs.enabled && o.Urgency >= sentinel.UrgencyImportant {
			go func() { _ = vs.session.Speak(context.Background(), o.Summary) }()
		}
	}
}

// proactiveCommand handles /proactive.
func proactiveCommand(rest string, s *sentinel.Sentinel, cfg *config.Config) error {
	if s == nil {
		return fmt.Errorf("proactivity is not running")
	}
	sub, arg, _ := strings.Cut(strings.TrimSpace(rest), " ")
	arg = strings.TrimSpace(arg)

	switch sub {
	case "":
		fmt.Printf("  chattiness: %s · watchers: %s\n",
			s.Chattiness, strings.Join(s.Watchers(), ", "))
		pending := s.Peek()
		if len(pending) == 0 {
			fmt.Println("  nothing queued")
			return nil
		}
		fmt.Printf("  %d queued:\n", len(pending))
		for _, o := range pending {
			fmt.Printf("    %s\n", o.Describe())
		}
		return nil

	case "quiet", "balanced", "companion":
		s.Chattiness = sentinel.ParseChattiness(sub)
		cfg.Chattiness = sub
		fmt.Printf("  chattiness set to %s\n", s.Chattiness)
		return nil

	case "check", "now":
		pending := s.Pending()
		if len(pending) == 0 {
			fmt.Println("  nothing to report")
			return nil
		}
		for _, o := range pending {
			fmt.Printf("  %s\n", o.Describe())
		}
		return nil

	default:
		return fmt.Errorf("unknown option %q — try quiet, balanced, companion, check", sub)
	}
}

// deadlinesFrom bridges remembered commitments into the consequence lens,
// so "let's refactor everything" three days before a submission gets noticed.
func deadlinesFrom(store *memory.Store) func() ([]reflect.Deadline, error) {
	commitments := skills.Commitments(store)
	return func() ([]reflect.Deadline, error) {
		items, err := commitments()
		if err != nil {
			return nil, err
		}
		out := make([]reflect.Deadline, 0, len(items))
		for _, c := range items {
			out = append(out, reflect.Deadline{Text: c.Text, When: c.Deadline})
		}
		return out, nil
	}
}
