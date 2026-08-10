package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/llm"
	"github.com/Akins20/FREYA/internal/telemetry"
)

// Letting Freya read her own instrumentation.
//
// # Why she gets to see this at all
//
// Without it, "have you been useful today" and "what is this costing me" are
// questions she can only answer by guessing, and she guesses fluently — which
// is the worst possible combination. Giving her the actual log means the honest
// answer is also the easy one.
//
// The skill descriptions say plainly that cost is an estimate, because she will
// repeat the framing she is given. A tool that hands her a number labelled
// "cost" will produce a confident claim about money; one that hands her a
// number labelled "estimate, not a bill" produces a hedged one.

// RegisterTelemetry adds the usage-reporting skills.
func RegisterTelemetry(r *Registry, dataDir string) {
	logPath := filepath.Join(dataDir, "telemetry.jsonl")

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "usage_report",
			Description: "Report what has actually run: which skills were used and how " +
				"often, how many tokens each model consumed, what failed, and an " +
				"ESTIMATED cost. The cost is computed from a built-in price table, not " +
				"from a bill — say so when quoting it, and never state it as what the " +
				"user has been charged. Use this for 'what have you been doing', 'what " +
				"is this costing', or 'which tools do you use most'.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"days": {Type: "integer",
					Description: "Only count the last N days. Omit for everything on record."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			var since time.Time
			if days := argInt(args, "days", 0); days > 0 {
				since = time.Now().AddDate(0, 0, -days)
			}
			events, err := telemetry.Load(logPath, since)
			if err != nil {
				return "", err
			}
			return telemetry.Summarise(events).Report(20), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "usage_daily",
			Description: "Usage broken down by day: tool calls, model calls, failures and " +
				"estimated cost per day. Use this to spot a trend or answer 'is this " +
				"getting more expensive'.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"days": {Type: "integer", Description: "How many days back (default 14)."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			days := argInt(args, "days", 14)
			events, err := telemetry.Load(logPath, time.Now().AddDate(0, 0, -days))
			if err != nil {
				return "", err
			}
			return telemetry.Daily(events), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "usage_rates",
			Description: "Show the price table the cost estimates are based on, and what it " +
				"does not account for. Use this when the user questions a figure, or asks " +
				"how the estimate is worked out.",
			Params: llm.ObjectSchema(map[string]llm.Property{}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return telemetry.Rates(), nil
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "usage_failures",
			Description: "List what has been failing and how often, grouped by kind of " +
				"failure. Use this when something seems broken, or when asked what keeps " +
				"going wrong.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"days": {Type: "integer", Description: "How many days back (default 7)."},
			}),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			days := argInt(args, "days", 7)
			events, err := telemetry.Load(logPath, time.Now().AddDate(0, 0, -days))
			if err != nil {
				return "", err
			}
			s := telemetry.Summarise(events)
			if len(s.Errors) == 0 {
				return fmt.Sprintf("Nothing has failed in the last %d days (%d events).",
					days, s.Events), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Failures in the last %d days:\n", days)
			for _, e := range s.Errors {
				fmt.Fprintf(&b, "  %-44s %d\n", e.Name, e.N)
			}
			// Naming the worst offender turns a list into something actionable.
			for _, tool := range s.Tools {
				if tool.Failed > 0 {
					fmt.Fprintf(&b, "\nMost unreliable skill: %s — %d of %d calls failed.",
						tool.Name, tool.Failed, tool.N)
					break
				}
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
	})
}
