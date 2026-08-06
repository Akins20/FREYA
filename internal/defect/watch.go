package defect

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/telemetry"
)

// Noticing without being asked.
//
// # Why look at telemetry at all, when the agent already files reports
//
// Because the agent only sees one exchange at a time. The failures that matter
// most are visible only across many: a tool that has failed every time it was
// called today, a page that defeats the same approach every evening, a cost that
// quietly tripled. Every one of those was found this week by a person sitting
// down with the log — which is exactly the work that should not need a person.
//
// The scan is deliberately blunt. It reports SHAPES — this tool never works, this
// call was repeated until it was refused — and never causes. A watcher that
// guesses at a cause sends whoever reads it down the guess, and the causes found
// this week were all one layer beneath where the symptom pointed.

// minCalls is how many calls a tool needs before its failure rate means anything.
// Three, because one failure is noise and two is a coincidence.
const minCalls = 3

// thrashRun is how many identical failing calls in a row constitute thrash worth
// reporting. The breaker refuses the third, so reaching it at all means the loop
// happened.
const thrashRun = 3

// Scan reads a window of telemetry and reports the shapes worth a look.
//
// Reports are returned rather than filed, so the caller decides what to keep —
// and so this is a pure function that can be tested against a fixed log.
func Scan(events []telemetry.Event, window time.Duration) []Report {
	tools := make([]telemetry.Event, 0, len(events))
	for _, e := range events {
		if e.Kind == "tool" {
			tools = append(tools, e)
		}
	}
	if len(tools) == 0 {
		return nil
	}

	var out []Report
	out = append(out, alwaysFailing(tools, window)...)
	if r, ok := repeatedIdentically(tools, window); ok {
		out = append(out, r)
	}
	return out
}

// alwaysFailing reports tools that did not work once.
//
// A tool with a 40% failure rate is a hard page. A tool with a 100% failure rate
// over several calls is broken, or is being called in a way that cannot work —
// and either way she cannot get past it, which is the thing worth knowing.
func alwaysFailing(tools []telemetry.Event, window time.Duration) []Report {
	type tally struct {
		calls, failed int
		errs          []string
		last          time.Time
	}
	byTool := map[string]*tally{}
	for _, e := range tools {
		t := byTool[e.Name]
		if t == nil {
			t = &tally{}
			byTool[e.Name] = t
		}
		t.calls++
		t.last = e.At
		if e.Outcome != "" && e.Outcome != telemetry.OutcomeOK {
			t.failed++
			if e.Error != "" {
				t.errs = append(t.errs, e.Error)
			}
		}
	}

	names := make([]string, 0, len(byTool))
	for n := range byTool {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []Report
	for _, name := range names {
		t := byTool[name]
		if t.calls < minCalls || t.failed != t.calls {
			continue
		}
		out = append(out, Report{
			At:   t.last,
			Kind: NothingWorked,
			Goal: fmt.Sprintf("use %s", name),
			Note: fmt.Sprintf("Over the last %s, %s was called %d times and failed every "+
				"single time. Nothing she tried with it worked.",
				humanWindow(window), name, t.calls),
			Attempts: t.calls,
			Failures: distinct(t.errs, 4),
		})
	}
	return out
}

// repeatedIdentically finds the longest run of the same call with the same
// arguments failing over and over.
//
// The argument hash is what makes this meaningful: twenty attempts at twenty
// different things is work, and twenty attempts at the same thing is a loop. Only
// the hash can tell them apart, which is why it is recorded.
func repeatedIdentically(tools []telemetry.Event, window time.Duration) (Report, bool) {
	best, run := 0, 0
	var key string
	var bestEvent telemetry.Event
	var cur telemetry.Event

	for _, e := range tools {
		k := e.Name + "|" + e.ArgsHash
		if k != key {
			key, run, cur = k, 0, e
		}
		if e.Outcome != "" && e.Outcome != telemetry.OutcomeOK {
			run++
			if run > best {
				best, bestEvent = run, cur
			}
		} else {
			run = 0
		}
	}
	if best < thrashRun {
		return Report{}, false
	}
	return Report{
		At:   bestEvent.At,
		Kind: Thrash,
		Goal: fmt.Sprintf("use %s", bestEvent.Name),
		Note: fmt.Sprintf("In the last %s, %s was called %d times in a row with identical "+
			"arguments and failed every time. Identical arguments means she was not "+
			"adapting — either the error did not tell her what was wrong, or she could "+
			"not act on what it told her.", humanWindow(window), bestEvent.Name, best),
		Attempts: best,
		Failures: distinct([]string{bestEvent.Error}, 1),
		Exchange: bestEvent.ExchangeID,
	}, true
}

// distinct keeps the first few unique, non-empty strings.
func distinct(in []string, limit int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func humanWindow(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return d.Round(time.Minute).String()
	}
}
