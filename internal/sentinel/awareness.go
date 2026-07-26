package sentinel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Situational awareness: time, load, idleness and remembered commitments.
//
// These watchers exist because "proactive" should mean *aware*, not merely
// *periodic*. Knowing that it is 2am, that the machine has been thrashing for
// twenty minutes, that the user walked away an hour ago, or that a deadline
// they mentioned last week is now three days out — that is what separates an
// assistant from a cron job.

// --- temporal ---------------------------------------------------------------

// TemporalWatcher tracks the passage of time itself: the working day, session
// length, and dates rolling over.
type TemporalWatcher struct {
	// SessionStart is when this run began.
	SessionStart time.Time
	// LongSession is how long before a continuous stretch is worth mentioning.
	LongSession time.Duration
	// Now is injectable for testing. Nil means time.Now.
	Now func() time.Time
}

func (TemporalWatcher) Name() string            { return "time" }
func (TemporalWatcher) Interval() time.Duration { return 10 * time.Minute }

func (t TemporalWatcher) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t TemporalWatcher) Check(context.Context) ([]Observation, error) {
	now := t.now()
	var out []Observation

	long := t.LongSession
	if long == 0 {
		long = 3 * time.Hour
	}

	// Working very late. Keyed by date so it fires at most once per night
	// rather than every ten minutes until dawn.
	hour := now.Hour()
	if hour >= 1 && hour < 5 {
		out = append(out, Observation{
			Key:        "late-night:" + now.Format("2006-01-02"),
			Summary:    fmt.Sprintf("it's %s — you've been at this into the small hours", now.Format("3:04pm")),
			Urgency:    UrgencyAmbient,
			Actionable: true,
		})
	}

	if !t.SessionStart.IsZero() {
		elapsed := now.Sub(t.SessionStart)
		if elapsed > long {
			// Bucket by whole hours so it recurs at most hourly, not per tick.
			out = append(out, Observation{
				Key: fmt.Sprintf("long-session:%d", int(elapsed.Hours())),
				Summary: fmt.Sprintf("you've been going %s straight",
					humanDuration(elapsed)),
				Urgency:    UrgencyAmbient,
				Actionable: true,
			})
		}
	}
	return out, nil
}

// TimeContext describes the current moment in the terms a person would use.
//
// This is fed into the prompt rather than raised as an observation: knowing it
// is "Wednesday evening" changes how a reply should read, but is never itself
// worth interrupting for.
func TimeContext(now time.Time, sessionStart time.Time, lastSeen time.Time) string {
	var sb strings.Builder

	part := "night"
	switch h := now.Hour(); {
	case h >= 5 && h < 12:
		part = "morning"
	case h >= 12 && h < 17:
		part = "afternoon"
	case h >= 17 && h < 21:
		part = "evening"
	case h >= 21 || h < 1:
		part = "late evening"
	case h >= 1 && h < 5:
		part = "the small hours"
	}

	fmt.Fprintf(&sb, "It is %s, %s (%s).",
		now.Format("Monday 2 January 2006"), now.Format("15:04"), part)

	if !sessionStart.IsZero() {
		if d := now.Sub(sessionStart); d > 5*time.Minute {
			fmt.Fprintf(&sb, " This session has been running %s.", humanDuration(d))
		}
	}
	if !lastSeen.IsZero() {
		gap := now.Sub(lastSeen)
		switch {
		case gap > 14*24*time.Hour:
			fmt.Fprintf(&sb, " You haven't spoken in %s — a lot may have changed.", humanDuration(gap))
		case gap > 24*time.Hour:
			fmt.Fprintf(&sb, " Last spoke %s ago.", humanDuration(gap))
		}
	}
	return sb.String()
}

// --- process and load -------------------------------------------------------

// ProcessWatcher notices the machine working unusually hard.
type ProcessWatcher struct {
	// LoadPerCore above which the machine counts as overloaded.
	LoadPerCore float64
	// CPUHogPercent is the single-process threshold worth naming.
	CPUHogPercent float64
	// Cores overrides the detected core count.
	Cores int
}

func (ProcessWatcher) Name() string            { return "process" }
func (ProcessWatcher) Interval() time.Duration { return 5 * time.Minute }

func (p ProcessWatcher) Check(ctx context.Context) ([]Observation, error) {
	loadPerCore := p.LoadPerCore
	if loadPerCore == 0 {
		loadPerCore = 1.5
	}
	hog := p.CPUHogPercent
	if hog == 0 {
		hog = 90
	}

	cores := p.Cores
	if cores == 0 {
		cores = runtimeCores()
	}

	var out []Observation

	// Load average, normalised per core so the threshold means the same thing
	// on a dual-core laptop and a sixteen-core desktop.
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(raw))
		if len(f) > 0 {
			if load1, err := strconv.ParseFloat(f[0], 64); err == nil {
				perCore := load1 / float64(cores)
				if perCore > loadPerCore {
					urgency := UrgencyNotable
					if perCore > loadPerCore*2 {
						urgency = UrgencyImportant
					}
					out = append(out, Observation{
						Key: "load",
						Summary: fmt.Sprintf("system load is %.1f across %d cores — the machine is struggling",
							load1, cores),
						Detail:     fmt.Sprintf("load average %s, %.2f per core", f[0], perCore),
						Urgency:    urgency,
						Actionable: true,
					})
				}
			}
		}
	}

	// A single process dominating the CPU is worth naming, because the user can
	// act on a name in a way they cannot act on a load average.
	cmd := exec.CommandContext(ctx, "ps", "-eo", "pcpu,comm", "--sort=-pcpu")
	psOut, err := cmd.Output()
	if err != nil {
		return out, nil
	}
	for i, line := range strings.Split(string(psOut), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pcpu, err := strconv.ParseFloat(f[0], 64)
		if err != nil || pcpu < hog {
			break // sorted descending: once below the bar, all the rest are too
		}
		name := f[1]
		// Freya must not report herself as the problem.
		if name == "freya" {
			continue
		}
		out = append(out, Observation{
			Key:        "cpu-hog:" + name,
			Summary:    fmt.Sprintf("%s is using %.0f%% CPU", name, pcpu),
			Urgency:    UrgencyNotable,
			Actionable: true,
		})
	}
	return out, nil
}

func runtimeCores() int {
	raw, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 1
	}
	n := strings.Count(string(raw), "processor\t")
	if n < 1 {
		return 1
	}
	return n
}

// --- idleness ---------------------------------------------------------------

// IdleWatcher notices when the user steps away and when they come back.
//
// Returning after a break is the single best moment to deliver everything that
// was suppressed while they were gone: they are present, and not yet
// mid-thought.
type IdleWatcher struct {
	// AwayAfter is how long without input counts as away.
	AwayAfter time.Duration

	lastIdle    time.Duration
	lastCounted uint64
	lastPoll    time.Time
	wasAway     bool
}

func (IdleWatcher) Name() string            { return "idle" }
func (IdleWatcher) Interval() time.Duration { return 2 * time.Minute }

func (w *IdleWatcher) Check(ctx context.Context) ([]Observation, error) {
	away := w.AwayAfter
	if away == 0 {
		away = 15 * time.Minute
	}

	idle, ok := w.idleTime(ctx)
	if !ok {
		return nil, nil
	}

	var out []Observation
	isAway := idle >= away

	// The transition matters, not the state. Firing every tick while someone
	// is at lunch would be exactly the nagging this package exists to avoid.
	if w.wasAway && !isAway {
		out = append(out, Observation{
			Key:        "returned",
			Summary:    "welcome back",
			Detail:     fmt.Sprintf("away for about %s", humanDuration(w.lastIdle)),
			Urgency:    UrgencyAmbient,
			Actionable: false,
		})
	}
	w.wasAway = isAway
	w.lastIdle = idle
	return out, nil
}

// idleTime reports how long since the last input event.
//
// xprintidle is used when present. Otherwise input-device interrupt counters
// stand in: if the keyboard and mouse IRQ totals have not moved between polls,
// nobody is touching the machine. Coarser than the X extension, but it needs no
// package installed and no X connection.
func (w *IdleWatcher) idleTime(ctx context.Context) (time.Duration, bool) {
	if _, err := exec.LookPath("xprintidle"); err == nil {
		out, err := exec.CommandContext(ctx, "xprintidle").Output()
		if err == nil {
			if ms, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64); err == nil {
				return time.Duration(ms) * time.Millisecond, true
			}
		}
	}

	total, ok := inputInterruptTotal()
	if !ok {
		return 0, false
	}
	now := time.Now()
	defer func() {
		w.lastCounted = total
		w.lastPoll = now
	}()

	if w.lastPoll.IsZero() {
		return 0, false // first poll establishes the baseline
	}
	if total != w.lastCounted {
		return 0, true // input happened since the last poll
	}
	return now.Sub(w.lastPoll), true
}

var inputIRQPattern = regexp.MustCompile(`(?i)(i8042|keyboard|mouse|synaptics|touchpad|elan)`)

// inputInterruptTotal sums interrupt counts for human-input devices.
func inputInterruptTotal() (uint64, bool) {
	raw, err := os.ReadFile("/proc/interrupts")
	if err != nil {
		return 0, false
	}
	var total uint64
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !inputIRQPattern.MatchString(line) {
			continue
		}
		found = true
		for _, f := range strings.Fields(line)[1:] {
			if n, err := strconv.ParseUint(f, 10, 64); err == nil {
				total += n
			}
		}
	}
	return total, found
}

// --- remembered commitments -------------------------------------------------

// Commitment is something the user said they had to do, with a deadline.
type Commitment struct {
	Key      string
	Text     string
	Deadline time.Time
}

// CommitmentWatcher reviews what Freya remembers and surfaces deadlines as
// they approach.
//
// This is the difference between memory as a transcript and memory as
// something that works for you. The user mentioned a dissertation deadline
// once, weeks ago; nobody should have to ask for that to come back at the
// right moment.
type CommitmentWatcher struct {
	// Commitments is supplied by the caller, keeping sentinel independent of
	// how memory is stored.
	Commitments func() ([]Commitment, error)
	// Now is injectable for testing.
	Now func() time.Time
}

func (CommitmentWatcher) Name() string { return "commitments" }

// Interval is minutes, not hours, because deadlines now include near-term ones
// (a quiz due tonight, not only a dissertation due next month). An hourly poll
// would sail past the "due in 20 minutes" window entirely; five minutes catches
// each escalation stage while it still means something.
func (CommitmentWatcher) Interval() time.Duration { return 5 * time.Minute }

func (c CommitmentWatcher) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c CommitmentWatcher) Check(context.Context) ([]Observation, error) {
	if c.Commitments == nil {
		return nil, nil
	}
	items, err := c.Commitments()
	if err != nil {
		return nil, err
	}

	now := c.now()
	var out []Observation

	for _, item := range items {
		if item.Deadline.IsZero() {
			continue
		}
		remaining := item.Deadline.Sub(now)

		// Urgency climbs as the deadline nears. Keyed by bucket so each stage
		// is announced once rather than every hour within it.
		var urgency Urgency
		var bucket, phrasing string
		// Urgency climbs as the deadline nears, now with sub-day stages so a
		// near-term deadline escalates instead of sitting at one coarse "within a
		// day" for its final hours. Each stage keyed by its own bucket, so it is
		// announced once rather than every poll while inside it.
		switch {
		case remaining < 0:
			urgency, bucket = UrgencyImportant, "overdue"
			phrasing = fmt.Sprintf("%s was due %s ago", item.Text, humanDuration(-remaining))
		case remaining < 15*time.Minute:
			urgency, bucket = UrgencyCritical, "15m"
			phrasing = fmt.Sprintf("%s is due in %s", item.Text, humanDuration(remaining))
		case remaining < time.Hour:
			urgency, bucket = UrgencyCritical, "1h"
			phrasing = fmt.Sprintf("%s is due within the hour — %s left", item.Text, humanDuration(remaining))
		case remaining < 3*time.Hour:
			urgency, bucket = UrgencyImportant, "3h"
			phrasing = fmt.Sprintf("%s is due in about %s", item.Text, humanDuration(remaining))
		case remaining < 12*time.Hour:
			urgency, bucket = UrgencyImportant, "12h"
			phrasing = fmt.Sprintf("%s is due in %s", item.Text, humanDuration(remaining))
		case remaining < 24*time.Hour:
			urgency, bucket = UrgencyNotable, "1d"
			phrasing = fmt.Sprintf("%s is due within a day", item.Text)
		case remaining < 3*24*time.Hour:
			urgency, bucket = UrgencyNotable, "3d"
			phrasing = fmt.Sprintf("%s is due in %d days", item.Text, int(remaining.Hours()/24))
		case remaining < 7*24*time.Hour:
			urgency, bucket = UrgencyAmbient, "1w"
			phrasing = fmt.Sprintf("%s is due in about a week", item.Text)
		case remaining < 30*24*time.Hour:
			urgency, bucket = UrgencyAmbient, "1m"
			phrasing = fmt.Sprintf("%s is due in %d days", item.Text, int(remaining.Hours()/24))
		default:
			continue // too far out to be worth raising
		}

		out = append(out, Observation{
			Key:        "commitment:" + item.Key + ":" + bucket,
			Summary:    phrasing,
			Detail:     "deadline " + item.Deadline.Format("Mon 2 Jan 2006"),
			Urgency:    urgency,
			Actionable: true,
		})
	}
	return out, nil
}

// --- deadline extraction ----------------------------------------------------

// datePatterns recognise the ways a person writes a date in passing.
var datePatterns = []struct {
	re     *regexp.Regexp
	layout string
	// reorder maps capture groups into the layout's expected order.
	reorder func([]string) string
}{
	{ // 30 August 2026 / 30th August 2026
		re:     regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(january|february|march|april|may|june|july|august|september|october|november|december)\s+(\d{4})\b`),
		layout: "2 January 2006",
		reorder: func(m []string) string {
			return fmt.Sprintf("%s %s %s", m[1], capitalise(m[2]), m[3])
		},
	},
	{ // August 30 2026 / August 30th, 2026
		re:     regexp.MustCompile(`(?i)\b(january|february|march|april|may|june|july|august|september|october|november|december)\s+(\d{1,2})(?:st|nd|rd|th)?,?\s+(\d{4})\b`),
		layout: "2 January 2006",
		reorder: func(m []string) string {
			return fmt.Sprintf("%s %s %s", m[2], capitalise(m[1]), m[3])
		},
	},
	{ // 2026-08-30
		re:      regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`),
		layout:  "2006-01-02",
		reorder: func(m []string) string { return m[0] },
	},
	{ // 30/08/2026 — day first, as written in most of the world
		re:      regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`),
		layout:  "2/1/2006",
		reorder: func(m []string) string { return m[0] },
	},
}

// deadlineWords mark text that is about an obligation rather than a date in
// passing. "Due 30 August" is a commitment; "released 30 August" is trivia.
var deadlineWords = regexp.MustCompile(`(?i)\b(due|deadline|submit|submission|hand in|exam|interview|expires?|renewal|appointment|meeting|presentation|defen[cs]e|viva)\b`)

// ExtractDeadline finds a commitment date in free text, if there is one.
//
// It returns false for text that merely mentions a date, because promoting
// every remembered date into a countdown would turn memory into a nag.
func ExtractDeadline(text string) (time.Time, bool) {
	if !deadlineWords.MatchString(text) {
		return time.Time{}, false
	}
	for _, p := range datePatterns {
		m := p.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		if t, err := time.Parse(p.layout, p.reorder(m)); err == nil {
			// Deadlines land at end of day unless stated otherwise.
			return t.Add(23*time.Hour + 59*time.Minute), true
		}
	}
	return time.Time{}, false
}

// capitalise upper-cases the first rune and lower-cases the rest, which is what
// time.Parse expects for month names. strings.Title is deprecated and would
// also capitalise every word.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	lower := strings.ToLower(s)
	return strings.ToUpper(lower[:1]) + lower[1:]
}
