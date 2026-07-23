package bench

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Scorecard aggregates results into something worth reading.
type Scorecard struct {
	Results []Result
}

// Add records one result.
func (s *Scorecard) Add(r Result) { s.Results = append(s.Results, r) }

// byCategory groups results.
func (s *Scorecard) byCategory() map[string][]Result {
	m := map[string][]Result{}
	for _, r := range s.Results {
		m[r.Benchmark.Category] = append(m[r.Benchmark.Category], r)
	}
	return m
}

// Weighted score credits harder benchmarks more, so passing five trivial tasks
// does not outweigh failing the orchestration that is the whole point.
func (s *Scorecard) Weighted() (got, total int) {
	for _, r := range s.Results {
		w := r.Benchmark.Difficulty
		if w <= 0 {
			w = 1
		}
		total += w
		if r.Pass {
			got += w
		}
	}
	return got, total
}

// Report renders the full scorecard.
func (s *Scorecard) Report() string {
	var b strings.Builder
	passed := 0
	var slowest *Result
	var totalTools int
	for i := range s.Results {
		r := &s.Results[i]
		if r.Pass {
			passed++
		}
		if r.World != nil {
			totalTools += len(r.World.ToolCalls)
			if slowest == nil || r.World.Duration > slowest.World.Duration {
				slowest = r
			}
		}
	}

	fmt.Fprintf(&b, "FREYA AGENTIC BENCHMARKS\n")
	fmt.Fprintf(&b, "========================\n\n")

	cats := s.byCategory()
	names := make([]string, 0, len(cats))
	for c := range cats {
		names = append(names, c)
	}
	sort.Strings(names)

	for _, cat := range names {
		rs := cats[cat]
		cp := 0
		for _, r := range rs {
			if r.Pass {
				cp++
			}
		}
		fmt.Fprintf(&b, "%s  (%d/%d)\n", strings.ToUpper(cat), cp, len(rs))
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Benchmark.Name < rs[j].Benchmark.Name })
		for _, r := range rs {
			mark := "FAIL"
			if r.Pass {
				mark = "pass"
			}
			d := time.Duration(0)
			tools := 0
			if r.World != nil {
				d = r.World.Duration.Round(time.Second)
				tools = len(r.World.ToolCalls)
			}
			stars := strings.Repeat("★", max(1, r.Benchmark.Difficulty))
			fmt.Fprintf(&b, "  [%s] %-28s %-6s %2dt %5s  %s\n",
				mark, r.Benchmark.Name, stars, tools, d, r.Reason)
		}
		b.WriteString("\n")
	}

	got, total := s.Weighted()
	pct := 0.0
	if total > 0 {
		pct = 100 * float64(got) / float64(total)
	}
	fmt.Fprintf(&b, "------------------------\n")
	fmt.Fprintf(&b, "PASSED   %d / %d benchmarks\n", passed, len(s.Results))
	fmt.Fprintf(&b, "WEIGHTED %d / %d  (%.0f%%, harder tasks count more)\n", got, total, pct)
	fmt.Fprintf(&b, "DROVE    %d tool calls total\n", totalTools)
	if slowest != nil {
		fmt.Fprintf(&b, "SLOWEST  %s at %s\n", slowest.Benchmark.Name, slowest.World.Duration.Round(time.Second))
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
