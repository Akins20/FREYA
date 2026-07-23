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
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
