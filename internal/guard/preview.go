package guard

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// previewFileLimit bounds how much of the filesystem a preview will walk.
// Counting a hundred thousand files to answer "is this safe" is itself a
// problem; past this point the answer is "a lot, be careful".
const previewFileLimit = 20000

// previewSampleSize is how many example paths to show.
const previewSampleSize = 6

// describeEffect renders what an action will actually do, in concrete terms.
//
// This is the difference between an informed decision and a reflex. Resolving
// globs, counting files and measuring bytes costs a few milliseconds and turns
// "are you sure?" into "delete 4,312 files, 8.2 GB, including Development/JARVIS".
func describeEffect(action Action) string {
	switch action.Kind {
	case KindDelete:
		return describeDeletion(action.Paths)

	case KindMove:
		if len(action.Paths) >= 2 {
			return fmt.Sprintf("move %s to %s%s",
				shortPath(action.Paths[0]), shortPath(action.Paths[len(action.Paths)-1]),
				overwriteWarning(action.Paths[len(action.Paths)-1]))
		}
		return "move " + strings.Join(shortPaths(action.Paths), ", ")

	case KindWrite:
		var parts []string
		for _, p := range action.Paths {
			if info, err := os.Stat(p); err == nil {
				parts = append(parts, fmt.Sprintf("overwrite %s (currently %s)",
					shortPath(p), humanBytes(info.Size())))
			} else {
				parts = append(parts, "create "+shortPath(p))
			}
		}
		return strings.Join(parts, "; ")

	case KindInput:
		return "type or click into the focused window: " + commandLine(action)

	case KindBrowser:
		return "browser: " + commandLine(action)

	case KindSystem:
		return "system change: " + commandLine(action)

	default:
		line := commandLine(action)
		if action.Elevated {
			line = "sudo " + line
		}
		// An exec action that names paths gets them resolved too, since `rm`
		// arriving as KindExec is the most likely way to lose data.
		if base := filepath.Base(action.Command); base == "rm" || base == "shred" {
			if targets := globTargets(action.Paths); len(targets) > 0 {
				return line + " → " + describeDeletion(action.Paths)
			}
		}
		return "run: " + line
	}
}

// describeDeletion resolves globs and reports the true scope of a removal.
func describeDeletion(paths []string) string {
	targets := globTargets(paths)
	if len(targets) == 0 {
		return "delete " + strings.Join(shortPaths(paths), ", ") + " (nothing currently matches)"
	}

	var files, dirs int
	var bytes int64
	var samples []string
	truncated := false

	for _, t := range targets {
		info, err := os.Lstat(t)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			files++
			bytes += info.Size()
			if len(samples) < previewSampleSize {
				samples = append(samples, shortPath(t))
			}
			continue
		}

		dirs++
		if len(samples) < previewSampleSize {
			samples = append(samples, shortPath(t)+"/")
		}
		// Walk the tree, but stop counting rather than stall on a huge one.
		count := 0
		_ = filepath.WalkDir(t, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			count++
			if count > previewFileLimit {
				truncated = true
				return filepath.SkipAll
			}
			if d.IsDir() {
				dirs++
				return nil
			}
			files++
			if fi, err := d.Info(); err == nil {
				bytes += fi.Size()
			}
			return nil
		})
	}

	var sb strings.Builder
	sb.WriteString("delete ")
	if files > 0 {
		fmt.Fprintf(&sb, "%d file%s", files, plural(files))
	}
	if dirs > 0 {
		if files > 0 {
			sb.WriteString(" and ")
		}
		fmt.Fprintf(&sb, "%d director%s", dirs, dirPlural(dirs))
	}
	if truncated {
		sb.WriteString("+ (stopped counting)")
	}
	if bytes > 0 {
		fmt.Fprintf(&sb, ", %s", humanBytes(bytes))
	}
	if len(samples) > 0 {
		sort.Strings(samples)
		fmt.Fprintf(&sb, " — including %s", strings.Join(samples, ", "))
		if len(targets) > len(samples) {
			fmt.Fprintf(&sb, " and %d more", len(targets)-len(samples))
		}
	}
	return sb.String()
}

// globTargets expands wildcards to the paths that actually exist.
func globTargets(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		expanded := os.ExpandEnv(p)
		matches := []string{expanded}
		if strings.ContainsAny(expanded, "*?[") {
			if m, err := filepath.Glob(expanded); err == nil {
				matches = m
			}
		}
		for _, m := range matches {
			if seen[m] {
				continue
			}
			if _, err := os.Lstat(m); err != nil {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// overwriteWarning flags a destination that already exists.
func overwriteWarning(dest string) string {
	if info, err := os.Stat(dest); err == nil {
		if info.IsDir() {
			return " (destination directory exists)"
		}
		return fmt.Sprintf(" — OVERWRITING an existing %s file", humanBytes(info.Size()))
	}
	return ""
}

// shortPath abbreviates the home directory to keep previews readable.
func shortPath(p string) string {
	abs := absPath(p)
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(abs, home) {
		return "~" + strings.TrimPrefix(abs, home)
	}
	return abs
}

func shortPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, shortPath(p))
	}
	return out
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTP"[exp])
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func dirPlural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
