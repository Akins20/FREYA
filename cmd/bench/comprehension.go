package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/bench"
)

// Asking her what she would do, rather than watching her do it.
//
// Running the task measures whether it worked. Asking measures whether she KNOWS
// — and the difference matters because a toolset fails silently: she has the
// right tool, does not think of it, finishes badly by another route, and the
// artifact check passes anyway. Or she reports a limitation that is not real,
// which no artifact check can catch at all.
//
// She answers without acting, deliberately. Given tools she would discover the
// answer by trying things, and the discovery is exactly what is being measured.

// runComprehension asks every question and reports what she knows.
func runComprehension(binary string, verbose bool) int {
	questions := bench.Comprehension()
	fmt.Printf("Asking her about %d situations. She answers without acting.\n\n", len(questions))

	passed := 0
	var failures []string

	for i, q := range questions {
		prompt := comprehensionPrompt(q)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		cmd := exec.CommandContext(ctx, binary, "-ask", prompt)
		out, err := cmd.CombinedOutput()
		cancel()

		answer := cleanAnswer(string(out))
		if err != nil && answer == "" {
			fmt.Printf("  %2d. ERROR  %v\n", i+1, err)
			failures = append(failures, fmt.Sprintf("%d. could not ask: %v", i+1, err))
			continue
		}

		ok, why := bench.Grade(q, answer)
		mark := "FAIL"
		if ok {
			mark = "ok  "
			passed++
		}
		fmt.Printf("  %2d. %s  %s\n", i+1, mark, why)
		if verbose || !ok {
			fmt.Printf("        situation: %s\n", clipLine(q.Situation, 100))
			fmt.Printf("        she said:  %s\n", clipLine(answer, 260))
			if !ok {
				fmt.Printf("        matters because: %s\n", q.Why)
			}
		}
		if !ok {
			failures = append(failures, fmt.Sprintf("%d. %s — %s", i+1, why, q.Why))
		}
	}

	rate := float64(passed) / float64(len(questions)) * 100
	fmt.Printf("\n%d of %d (%.0f%%)\n", passed, len(questions), rate)

	if len(failures) > 0 {
		fmt.Println("\nWhat she does not know:")
		for _, f := range failures {
			fmt.Printf("  · %s\n", f)
		}
		fmt.Println("\nEach of these is a tool she has and would not reach for. That is worse " +
			"than not having it: it is weight in the prompt that buys nothing.")
	}

	if rate < 80 {
		return 1
	}
	return 0
}

// comprehensionPrompt asks the question in a way that measures knowledge rather
// than inviting a demonstration.
//
// The instruction not to act is load-bearing twice over: acting would let her
// find the answer by trial, and it would also mean a benchmark run driving a
// real browser against real sites.
func comprehensionPrompt(q bench.Question) string {
	return "This is a question about your own tools, not a task. Do NOT use any tool " +
		"and do NOT try to do this — just answer.\n\n" +
		"Situation: " + q.Situation + "\n\n" +
		"Answer in three or four sentences: name the exact tool you would use, say " +
		"what it does, and say what you expect to see afterwards. If a route would NOT " +
		"work here, say which and why."
}

// cleanAnswer strips the thinking window and tool tracing from a one-shot reply,
// so grading sees what she actually said rather than what she considered.
func cleanAnswer(raw string) string {
	var kept []string
	for _, line := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "",
			strings.HasPrefix(t, "💭"),
			strings.HasPrefix(t, "→"),
			strings.HasPrefix(t, "✓"),
			strings.HasPrefix(t, "✗"),
			strings.HasPrefix(t, "tools:"),
			strings.HasPrefix(t, "context:"),
			strings.HasPrefix(t, "daemon is running"):
			continue
		}
		kept = append(kept, t)
	}
	return strings.Join(kept, " ")
}

func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// comprehensionMain is the entry point when -comprehension is passed.
func comprehensionMain(binary string, verbose bool) {
	os.Exit(runComprehension(binary, verbose))
}
