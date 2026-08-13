package bench

import (
	"fmt"
	"sort"
	"strings"
)

// PassRateReport renders a multi-run scorecard: each benchmark as a pass-rate
// over its runs, grouped by category, with a weighted total.
//
// A pass-rate is the honest unit for a non-deterministic agent. "4/5" says more
// than "pass" — it distinguishes a benchmark she nails every time from one she
// scrapes occasionally, which is exactly the difference between a capability she
// has and one she sometimes stumbles into.
func PassRateReport(benchmarks []Benchmark, results map[string][]Result) string {
	// Index benchmarks by name for their category and difficulty.
	meta := map[string]Benchmark{}
	for _, b := range benchmarks {
		meta[b.Name] = b
	}

	type row struct {
		name       string
		category   string
		difficulty int
		passes     int
		runs       int
		lastReason string
	}
	var rows []row
	for name, rs := range results {
		b := meta[name]
		r := row{name: name, category: b.Category, difficulty: b.Difficulty, runs: len(rs)}
		for _, res := range rs {
			if res.Pass {
				r.passes++
			} else {
				r.lastReason = res.Reason
			}
		}
		rows = append(rows, r)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].category != rows[j].category {
			return rows[i].category < rows[j].category
		}
		return rows[i].name < rows[j].name
	})

	var b strings.Builder
	b.WriteString("FREYA AGENTIC BENCHMARKS — pass-rate over runs\n")
	b.WriteString("=============================================\n\n")

	var wGot, wTotal float64
	lastCat := ""
	for _, r := range rows {
		if r.category != lastCat {
			if lastCat != "" {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "%s\n", strings.ToUpper(r.category))
			lastCat = r.category
		}
		rate := 0.0
		if r.runs > 0 {
			rate = float64(r.passes) / float64(r.runs)
		}
		w := r.difficulty
		if w <= 0 {
			w = 1
		}
		wGot += rate * float64(w)
		wTotal += float64(w)

		stars := strings.Repeat("★", maxInt(1, r.difficulty))
		note := ""
		if r.passes < r.runs && r.lastReason != "" {
			note = "  — " + r.lastReason
		}
		fmt.Fprintf(&b, "  %-32s %-6s %d/%d%s\n", r.name, stars, r.passes, r.runs, note)
	}

	pct := 0.0
	if wTotal > 0 {
		pct = 100 * wGot / wTotal
	}
	b.WriteString("\n---------------------------------------------\n")
	fmt.Fprintf(&b, "WEIGHTED PASS-RATE  %.0f%%  (harder benchmarks count more)\n", pct)
	b.WriteString(reliabilityReport(results))
	return b.String()
}

// reliabilityReport summarises HOW she worked, not just whether the artifact
// landed. A task can pass while burning thirty rounds guessing — that is a task
// completed and a substrate that failed her, and it belongs in the score.
//
// Everything here comes from the run's own telemetry, read back out of the
// workspace, so it costs nothing to collect.
func reliabilityReport(results map[string][]Result) string {
	var runs, capped, thrashy, noops, failedTools, wasted int
	worstRun, worstTool, worstName := 0, "", ""

	for name, rs := range results {
		for _, r := range rs {
			if r.World == nil {
				continue
			}
			runs++
			wasted += r.World.WastedRounds()
			noops += r.World.SilentNoops()
			failedTools += r.World.FailedTools()
			// Counted from the rounds, not from a phrase in the reply. The reply is
			// the wrong place to look: at the cap she now writes a genuine progress
			// report in her own words, so there is no fixed string to match, and a
			// previous version of this matched a sentence that had since been
			// reworded — leaving the counter permanently zero and the gate metric
			// silently measuring nothing.
			if r.World.Rounds >= agentRoundCap {
				capped++
			}
			if n, tool := r.World.LongestRepeatRun(); n > 0 {
				if n > 3 {
					thrashy++
				}
				if n > worstRun {
					worstRun, worstTool, worstName = n, tool, name
				}
			}
		}
	}
	if runs == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\nRELIABILITY  (how she worked, not just what landed)\n")
	fmt.Fprintf(&b, "  ran out of rounds      %d/%d runs (%.0f%%)\n", capped, runs, 100*float64(capped)/float64(runs))
	fmt.Fprintf(&b, "  runs that thrashed     %d/%d  (>3 identical failing calls in a row)\n", thrashy, runs)
	if worstRun > 0 {
		fmt.Fprintf(&b, "  worst repeat run       %d × %s  (in %s)\n", worstRun, worstTool, worstName)
	}
	fmt.Fprintf(&b, "  wasted rounds          %d  (every tool call in the round failed)\n", wasted)
	fmt.Fprintf(&b, "  failed tool calls      %d\n", failedTools)
	fmt.Fprintf(&b, "  silent no-ops          %d  (succeeded and returned nothing)\n", noops)
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// agentRoundCap mirrors agent.maxToolRounds. It is duplicated rather than
// imported because the benchmark drives the built binary as a black box and must
// not link the package it is grading — but a mismatch would make the
// cap-exhaustion metric read zero, so it is asserted in the tests.
const agentRoundCap = 60
