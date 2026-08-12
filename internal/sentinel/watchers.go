package sentinel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DiskWatcher reports filesystems that are filling up.
type DiskWatcher struct {
	// WarnPercent is where it starts caring. Below this, silence.
	WarnPercent int
	// CriticalPercent is where it interrupts.
	CriticalPercent int
	// MinFreeGB raises urgency when absolute free space is low regardless of
	// percentage — 5% of a 4TB disk is fine, 5% of a 128GB disk is not.
	MinFreeGB float64
}

func (DiskWatcher) Name() string            { return "disk" }
func (DiskWatcher) Interval() time.Duration { return 15 * time.Minute }

func (d DiskWatcher) Check(ctx context.Context) ([]Observation, error) {
	warn := d.WarnPercent
	if warn == 0 {
		warn = 85
	}
	critical := d.CriticalPercent
	if critical == 0 {
		critical = 95
	}
	minFree := d.MinFreeGB
	if minFree == 0 {
		minFree = 5
	}

	out, err := exec.CommandContext(ctx, "df", "-B1", "--output=target,size,used,avail,pcent",
		"-x", "tmpfs", "-x", "devtmpfs", "-x", "squashfs", "-x", "overlay").Output()
	if err != nil {
		return nil, err
	}

	readOnly := readOnlyMounts()

	var observations []Observation
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		// Mount points can contain spaces, so anchor on the trailing columns.
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		n := len(f)
		availBytes, _ := strconv.ParseFloat(f[n-2], 64)
		pcent, _ := strconv.Atoi(strings.TrimSuffix(f[n-1], "%"))
		target := strings.Join(f[:n-4], " ")

		// A read-only filesystem is always full and can never be freed, so an
		// alert about one is noise by construction. Excluded by that property
		// rather than by filesystem name, because the name list was already
		// there and already wrong: it excluded squashfs, and a machine without
		// the squashfs kernel module mounts the same snap packages through
		// fuse.snapfuse instead. Eleven permanent "100% full" alerts, one per
		// installed snap, from an exclusion list that looked complete.
		if readOnly[target] {
			continue
		}

		if pcent < warn {
			continue
		}
		availGB := availBytes / (1 << 30)

		urgency := UrgencyNotable
		if pcent >= critical || availGB < minFree {
			urgency = UrgencyImportant
		}
		if pcent >= 98 || availGB < 1 {
			urgency = UrgencyCritical
		}

		observations = append(observations, Observation{
			Key:        "disk:" + target,
			Summary:    fmt.Sprintf("%s is %d%% full — %.1f GB left", target, pcent, availGB),
			Detail:     fmt.Sprintf("mount %s, %d%% used, %.1f GB available", target, pcent, availGB),
			Urgency:    urgency,
			Actionable: true,
		})
	}
	return observations, nil
}

// readOnlyMounts is every mount point the kernel says cannot be written to.
//
// Read from /proc/self/mountinfo rather than by running a second command,
// because it is already the authority df is summarising and it needs nothing
// installed. Field 6 is the per-mount option list and "ro" appears there for a
// read-only mount whatever the filesystem underneath happens to be.
//
// Empty on anything without procfs, which leaves the watcher exactly as it was
// on those platforms.
func readOnlyMounts() map[string]bool {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	ro := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		// 0 id, 1 parent, 2 dev, 3 root, 4 mount point, 5 options, then optional
		// fields terminated by "-".
		if len(f) < 6 {
			continue
		}
		for _, opt := range strings.Split(f[5], ",") {
			if opt == "ro" {
				// Mount points are octal-escaped in mountinfo; a space is \040.
				ro[strings.ReplaceAll(f[4], `\040`, " ")] = true
				break
			}
		}
	}
	return ro
}

// BatteryWatcher reports low battery on a laptop.
type BatteryWatcher struct {
	WarnPercent     int
	CriticalPercent int
}

func (BatteryWatcher) Name() string            { return "battery" }
func (BatteryWatcher) Interval() time.Duration { return 5 * time.Minute }

func (b BatteryWatcher) Check(context.Context) ([]Observation, error) {
	warn := b.WarnPercent
	if warn == 0 {
		warn = 20
	}
	critical := b.CriticalPercent
	if critical == 0 {
		critical = 8
	}

	matches, err := filepath.Glob("/sys/class/power_supply/BAT*")
	if err != nil || len(matches) == 0 {
		return nil, nil // desktop, or no battery exposed
	}
	base := matches[0]

	raw, err := os.ReadFile(filepath.Join(base, "capacity"))
	if err != nil {
		return nil, nil
	}
	level, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, nil
	}
	stateRaw, _ := os.ReadFile(filepath.Join(base, "status"))
	state := strings.TrimSpace(string(stateRaw))

	// Charging is the resolution, not the problem.
	if strings.EqualFold(state, "Charging") || strings.EqualFold(state, "Full") {
		return nil, nil
	}
	if level > warn {
		return nil, nil
	}

	urgency := UrgencyNotable
	if level <= critical {
		urgency = UrgencyCritical
	} else if level <= warn/2 {
		urgency = UrgencyImportant
	}

	return []Observation{{
		Key:        "battery",
		Summary:    fmt.Sprintf("battery at %d%% and not charging", level),
		Detail:     fmt.Sprintf("capacity %d%%, status %s", level, state),
		Urgency:    urgency,
		Actionable: true,
	}}, nil
}

// ReminderWatcher surfaces reminders that have come due.
//
// This is the one watcher whose observations are unambiguously wanted: the user
// explicitly asked to be told.
type ReminderWatcher struct {
	// DataDir holds notes.json.
	DataDir string
	// Due is supplied by the caller so sentinel does not depend on the skills
	// package, which would be a circular import.
	Due func() ([]string, error)
}

func (ReminderWatcher) Name() string            { return "reminders" }
func (ReminderWatcher) Interval() time.Duration { return time.Minute }

func (r ReminderWatcher) Check(context.Context) ([]Observation, error) {
	if r.Due == nil {
		return nil, nil
	}
	due, err := r.Due()
	if err != nil {
		return nil, err
	}
	var out []Observation
	for _, text := range due {
		out = append(out, Observation{
			// Keyed by content so the same reminder is not raised twice.
			Key:        "reminder:" + text,
			Summary:    "reminder: " + text,
			Urgency:    UrgencyImportant,
			Actionable: true,
		})
	}
	return out, nil
}

// MemoryWatcher reports when the machine is running short on RAM.
type MemoryWatcher struct {
	WarnPercent int
}

func (MemoryWatcher) Name() string            { return "memory" }
func (MemoryWatcher) Interval() time.Duration { return 10 * time.Minute }

func (m MemoryWatcher) Check(context.Context) ([]Observation, error) {
	warn := m.WarnPercent
	if warn == 0 {
		warn = 90
	}

	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	var total, available float64
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(f[1], 64)
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			available = v
		}
	}
	if total == 0 {
		return nil, nil
	}

	usedPct := int((total - available) / total * 100)
	if usedPct < warn {
		return nil, nil
	}
	urgency := UrgencyNotable
	if usedPct >= 96 {
		urgency = UrgencyImportant
	}

	return []Observation{{
		Key:        "memory",
		Summary:    fmt.Sprintf("memory is %d%% used — %.1f GB free", usedPct, available/(1<<20)),
		Urgency:    urgency,
		Actionable: true,
	}}, nil
}

// GitWatcher notices uncommitted work sitting in projects.
//
// Deliberately gentle: uncommitted changes are normal mid-task and only worth
// mentioning once they have sat untouched for a while.
type GitWatcher struct {
	// Root is the projects directory to scan.
	Root string
	// StaleAfter is how long changes must sit before they are worth raising.
	StaleAfter time.Duration
	// MaxRepos bounds the scan on a directory with dozens of projects.
	MaxRepos int
}

func (GitWatcher) Name() string            { return "git" }
func (GitWatcher) Interval() time.Duration { return 30 * time.Minute }

func (g GitWatcher) Check(ctx context.Context) ([]Observation, error) {
	if g.Root == "" {
		return nil, nil
	}
	stale := g.StaleAfter
	if stale == 0 {
		stale = 24 * time.Hour
	}
	maxRepos := g.MaxRepos
	if maxRepos == 0 {
		maxRepos = 40
	}

	entries, err := os.ReadDir(g.Root)
	if err != nil {
		return nil, err
	}

	var out []Observation
	scanned := 0
	for _, e := range entries {
		if scanned >= maxRepos {
			break
		}
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		repo := filepath.Join(g.Root, e.Name())
		if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
			continue
		}
		scanned++

		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < stale {
			continue // still being worked on
		}

		status, err := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain").Output()
		if err != nil || len(strings.TrimSpace(string(status))) == 0 {
			continue
		}
		changed := len(strings.Split(strings.TrimSpace(string(status)), "\n"))

		out = append(out, Observation{
			Key: "git-dirty:" + e.Name(),
			Summary: fmt.Sprintf("%s has %d uncommitted change%s, untouched for %s",
				e.Name(), changed, plural(changed), humanDuration(time.Since(info.ModTime()))),
			Urgency:    UrgencyAmbient,
			Actionable: true,
		})
	}
	return out, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
