package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Reading back what was recorded.
//
// Aggregation happens at query time rather than being maintained incrementally.
// A rollup kept up to date is a second source of truth that drifts from the
// first, and the log is small enough that recomputing is instant — a year of
// heavy use is a few tens of megabytes, which parses faster than the network
// call that produced any single row of it.

// Summary is an aggregate over a period.
type Summary struct {
	From, To time.Time
	Events   int

	Tools  []Count
	Models []ModelUsage
	Errors []Count

	TotalCostUSD   float64
	TotalInTokens  int
	TotalOutTokens int
	TotalCached    int
	TotalAudio     int

	ToolCalls    int
	ToolFailures int
	SlowestTool  string
	SlowestMS    int64
}

// Count is a name with a tally.
type Count struct {
	Name    string
	N       int
	Failed  int
	TotalMS int64
}

// AvgMS is the mean duration.
func (c Count) AvgMS() int64 {
	if c.N == 0 {
		return 0
	}
	return c.TotalMS / int64(c.N)
}

// ModelUsage aggregates one model's calls.
type ModelUsage struct {
	Name    string
	Calls   int
	In, Out int
	Cached  int
	Audio   int
	CostUSD float64
	TotalMS int64
}

// Load reads events from a log, optionally limited to those since a time.
func Load(path string, since time.Time) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // a torn final line from an interrupted write is survivable
		}
		if !since.IsZero() && e.At.Before(since) {
			continue
		}
		out = append(out, e)
	}
	return out, scanner.Err()
}

// Summarise aggregates a set of events.
func Summarise(events []Event) Summary {
	s := Summary{Events: len(events)}
	if len(events) == 0 {
		return s
	}

	tools := map[string]*Count{}
	models := map[string]*ModelUsage{}
	errors := map[string]*Count{}

	s.From, s.To = events[0].At, events[0].At

	for _, e := range events {
		if e.At.Before(s.From) {
			s.From = e.At
		}
		if e.At.After(s.To) {
			s.To = e.At
		}

		if !e.OK && e.Error != "" {
			key := errorClass(e.Error)
			if errors[key] == nil {
				errors[key] = &Count{Name: key}
			}
			errors[key].N++
		}

		switch e.Kind {
		case KindTool:
			if tools[e.Name] == nil {
				tools[e.Name] = &Count{Name: e.Name}
			}
			t := tools[e.Name]
			t.N++
			t.TotalMS += e.DurationMS
			if !e.OK {
				t.Failed++
				s.ToolFailures++
			}
			s.ToolCalls++
			if e.DurationMS > s.SlowestMS {
				s.SlowestMS, s.SlowestTool = e.DurationMS, e.Name
			}

		case KindModel:
			if models[e.Name] == nil {
				models[e.Name] = &ModelUsage{Name: e.Name}
			}
			m := models[e.Name]
			m.Calls++
			m.In += e.InputTokens
			m.Out += e.OutputTokens
			m.Cached += e.CachedTokens
			m.Audio += e.AudioTokens
			m.CostUSD += e.CostUSD
			m.TotalMS += e.DurationMS

			s.TotalInTokens += e.InputTokens
			s.TotalOutTokens += e.OutputTokens
			s.TotalCached += e.CachedTokens
			s.TotalAudio += e.AudioTokens
			s.TotalCostUSD += e.CostUSD
		}
	}

	for _, t := range tools {
		s.Tools = append(s.Tools, *t)
	}
	sort.Slice(s.Tools, func(i, j int) bool { return s.Tools[i].N > s.Tools[j].N })

	for _, m := range models {
		s.Models = append(s.Models, *m)
	}
	sort.Slice(s.Models, func(i, j int) bool { return s.Models[i].CostUSD > s.Models[j].CostUSD })

	for _, e := range errors {
		s.Errors = append(s.Errors, *e)
	}
	sort.Slice(s.Errors, func(i, j int) bool { return s.Errors[i].N > s.Errors[j].N })

	return s
}

// errorClass groups similar failures so a report shows kinds rather than a
// list of near-identical strings.
func errorClass(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		return "timeout"
	case strings.Contains(lower, "declined by user"):
		return "declined"
	case strings.Contains(lower, "refused:"):
		return "refused by guard"
	case strings.Contains(lower, "not found"), strings.Contains(lower, "no such"):
		return "not found"
	case strings.Contains(lower, "http 4"), strings.Contains(lower, "http 5"):
		return "api error"
	case strings.Contains(lower, "connection"), strings.Contains(lower, "network"):
		return "network"
	case strings.Contains(lower, "permission"), strings.Contains(lower, "denied"):
		return "permission"
	default:
		return clip(msg, 48)
	}
}

// Report renders a summary for display.
func (s Summary) Report(limit int) string {
	if s.Events == 0 {
		return "Nothing recorded yet."
	}
	if limit <= 0 {
		limit = 12
	}

	var b strings.Builder
	span := s.To.Sub(s.From)
	fmt.Fprintf(&b, "%d events over %s (%s → %s)\n",
		s.Events, humanDuration(span),
		s.From.Format("2 Jan 15:04"), s.To.Format("2 Jan 15:04"))

	if s.ToolCalls > 0 {
		rate := 100 * float64(s.ToolCalls-s.ToolFailures) / float64(s.ToolCalls)
		fmt.Fprintf(&b, "\nTOOLS — %d calls, %.1f%% succeeded\n", s.ToolCalls, rate)
		fmt.Fprintf(&b, "  %-26s %6s %7s %8s\n", "skill", "calls", "failed", "avg")
		for i, t := range s.Tools {
			if i >= limit {
				fmt.Fprintf(&b, "  … and %d more\n", len(s.Tools)-limit)
				break
			}
			fmt.Fprintf(&b, "  %-26s %6d %7d %6dms\n", t.Name, t.N, t.Failed, t.AvgMS())
		}
		if s.SlowestTool != "" {
			fmt.Fprintf(&b, "  slowest single call: %s at %dms\n", s.SlowestTool, s.SlowestMS)
		}
	}

	if len(s.Models) > 0 {
		fmt.Fprintf(&b, "\nMODELS — %s in, %s out, %s served from cache\n",
			humanCount(s.TotalInTokens), humanCount(s.TotalOutTokens), humanCount(s.TotalCached))
		fmt.Fprintf(&b, "  %-30s %6s %9s %9s %10s\n", "model", "calls", "in", "out", "est. cost")
		for _, m := range s.Models {
			fmt.Fprintf(&b, "  %-30s %6d %9s %9s %10s\n",
				clip(m.Name, 30), m.Calls, humanCount(m.In), humanCount(m.Out),
				fmt.Sprintf("~$%.4f", m.CostUSD))
		}
		if s.TotalAudio > 0 {
			fmt.Fprintf(&b, "  %s of input was audio, billed at roughly double the text rate\n",
				humanCount(s.TotalAudio))
		}
		if s.TotalInTokens > 0 {
			fmt.Fprintf(&b, "  cache hit rate: %.0f%% of input (higher is cheaper)\n",
				100*float64(s.TotalCached)/float64(s.TotalInTokens))
		}
		fmt.Fprintf(&b, "  ---\n  estimated total: ~$%.4f\n", s.TotalCostUSD)
		b.WriteString("  (an estimate from a built-in rate table, not a bill — " +
			"provider prices change, and subscription usage is not billed per token)\n")
	}

	if len(s.Errors) > 0 {
		fmt.Fprintf(&b, "\nFAILURES\n")
		for i, e := range s.Errors {
			if i >= limit {
				break
			}
			fmt.Fprintf(&b, "  %-40s %d\n", e.Name, e.N)
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// Daily buckets events by day, for spotting trends.
func Daily(events []Event) string {
	if len(events) == 0 {
		return "Nothing recorded yet."
	}
	type day struct {
		tools, models int
		cost          float64
		failures      int
	}
	byDay := map[string]*day{}
	var order []string

	for _, e := range events {
		key := e.At.Format("2006-01-02")
		if byDay[key] == nil {
			byDay[key] = &day{}
			order = append(order, key)
		}
		d := byDay[key]
		switch e.Kind {
		case KindTool:
			d.tools++
		case KindModel:
			d.models++
			d.cost += e.CostUSD
		}
		if !e.OK {
			d.failures++
		}
	}
	sort.Strings(order)

	var b strings.Builder
	fmt.Fprintf(&b, "%-12s %7s %8s %9s %10s\n", "day", "tools", "models", "failures", "est. cost")
	for _, k := range order {
		d := byDay[k]
		fmt.Fprintf(&b, "%-12s %7d %8d %9d %10s\n",
			k, d.tools, d.models, d.failures, fmt.Sprintf("~$%.4f", d.cost))
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
