package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/llm"
)

// maxReadBytes caps how much of a file is returned to the model in one call.
const maxReadBytes = 200 << 10

// RegisterDev adds development skills scoped to a projects directory.
//
// Every path argument is resolved and checked against root, so the model cannot
// be talked into reading outside the projects tree.
func RegisterDev(r *Registry, root string) {
	// So a file tool that missed can say which reader owns the root the file is
	// actually under. See elsewhere() in files.go for the three rounds this cost.
	noteReadableRoot("dev_read", root)

	d := &devSkills{root: root}

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "dev_projects",
			Description: "List projects in the development directory, newest activity first.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"filter": {Type: "string", Description: "Optional substring to match project names."},
			}),
		},
		Handler: d.projects,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "dev_git_status",
			Description: "Show git branch, working-tree status and recent commits for a project.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"project": {Type: "string", Description: "Project directory name."},
			}, "project"),
		},
		Handler: d.gitStatus,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "dev_find",
			Description: "Find files by name pattern within the projects directory.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"pattern": {Type: "string", Description: "Glob-style name pattern, e.g. '*.go' or 'README*'."},
				"project": {Type: "string", Description: "Optional project to limit the search to."},
				"limit":   {Type: "number", Description: "Maximum results, default 40."},
			}, "pattern"),
		},
		Handler: d.find,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "dev_read",
			Description: "Read a text file from the projects directory.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":  {Type: "string", Description: "Path relative to the projects directory."},
				"lines": {Type: "number", Description: "Maximum lines to return, default 300."},
			}, "path"),
		},
		Handler: d.read,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "dev_grep",
			Description: "Search file contents for a string across the projects directory.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"query":   {Type: "string", Description: "Text to search for."},
				"project": {Type: "string", Description: "Optional project to limit the search to."},
				"limit":   {Type: "number", Description: "Maximum matching lines, default 40."},
			}, "query"),
		},
		Handler: d.grep,
	})
}

type devSkills struct{ root string }

// resolve turns a user-supplied path into an absolute one inside root,
// rejecting anything that escapes via .. or symlinks.
func (d *devSkills) resolve(rel string) (string, error) {
	if rel == "" {
		return d.root, nil
	}
	joined := filepath.Join(d.root, filepath.Clean("/"+rel))

	rootEval, err := filepath.EvalSymlinks(d.root)
	if err != nil {
		rootEval = d.root
	}
	// The target may not exist yet; check the nearest existing ancestor.
	probe := joined
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	probeEval, err := filepath.EvalSymlinks(probe)
	if err != nil {
		probeEval = probe
	}
	if probeEval != rootEval && !strings.HasPrefix(probeEval, rootEval+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q is outside the projects directory", rel)
	}
	return joined, nil
}

func (d *devSkills) projects(_ context.Context, args map[string]any) (string, error) {
	entries, err := os.ReadDir(d.root)
	if err != nil {
		return "", fmt.Errorf("read projects dir: %w", err)
	}
	filter := strings.ToLower(argString(args, "filter"))

	type project struct {
		name     string
		modified time.Time
		isRepo   bool
	}
	var projects []project
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(e.Name()), filter) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		_, gitErr := os.Stat(filepath.Join(d.root, e.Name(), ".git"))
		projects = append(projects, project{e.Name(), info.ModTime(), gitErr == nil})
	}
	if len(projects) == 0 {
		return "No projects found.", nil
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].modified.After(projects[j].modified) })

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d projects in %s:\n", len(projects), d.root)
	for _, p := range projects {
		marker := ""
		if p.isRepo {
			marker = " [git]"
		}
		fmt.Fprintf(&sb, "- %s%s (modified %s)\n", p.name, marker, p.modified.Format("2 Jan 2006"))
	}
	return strings.TrimSpace(sb.String()), nil
}

func (d *devSkills) gitStatus(ctx context.Context, args map[string]any) (string, error) {
	dir, err := d.resolve(argString(args, "project"))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", fmt.Errorf("%s is not a git repository", filepath.Base(dir))
	}

	var sb strings.Builder
	if out, err := run(ctx, 15*time.Second, "git", "-C", dir, "branch", "--show-current"); err == nil {
		sb.WriteString("Branch: " + out + "\n")
	}
	if out, err := run(ctx, 15*time.Second, "git", "-C", dir, "status", "--short"); err == nil {
		if out == "" {
			sb.WriteString("Working tree clean.\n")
		} else {
			sb.WriteString("Changes:\n" + out + "\n")
		}
	}
	if out, err := run(ctx, 15*time.Second, "git", "-C", dir, "log", "-5", "--pretty=format:%h %s (%cr)"); err == nil {
		sb.WriteString("Recent commits:\n" + out + "\n")
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("could not read git state for %s", dir)
	}
	return strings.TrimSpace(sb.String()), nil
}

func (d *devSkills) find(ctx context.Context, args map[string]any) (string, error) {
	pattern := argString(args, "pattern")
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	dir, err := d.resolve(argString(args, "project"))
	if err != nil {
		return "", err
	}
	limit := clamp(argInt(args, "limit", 40), 1, 200)

	// Prune the directories that dominate a dev tree and interest nobody.
	out, _ := run(ctx, 30*time.Second, "find", dir,
		"-name", ".git", "-prune", "-o",
		"-name", "node_modules", "-prune", "-o",
		"-name", "vendor", "-prune", "-o",
		"-type", "f", "-iname", pattern, "-print")

	lines := splitNonEmpty(out)
	if len(lines) == 0 {
		// "Nothing there" and "you named a directory that does not exist" are
		// different answers, and conflating them hands back a confident false
		// negative: she reads "No files matching" as having checked. The search
		// only means anything if the place searched was real.
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", fmt.Errorf("there is no %q to search — check the project name with dev_projects", argString(args, "project"))
		}
		return fmt.Sprintf("No files matching %q.", pattern), nil
	}
	truncated := false
	if len(lines) > limit {
		lines, truncated = lines[:limit], true
	}

	var sb strings.Builder
	for _, l := range lines {
		if rel, err := filepath.Rel(d.root, l); err == nil {
			l = rel
		}
		sb.WriteString("- " + l + "\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "[showing first %d matches]\n", limit)
	}
	return strings.TrimSpace(sb.String()), nil
}

func (d *devSkills) read(_ context.Context, args map[string]any) (string, error) {
	path, err := d.resolve(argString(args, "path"))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; use dev_find to list it", path)
	}
	if info.Size() > maxReadBytes {
		return "", fmt.Errorf("%s is %.1f MB, too large to read in full",
			filepath.Base(path), float64(info.Size())/(1<<20))
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	maxLines := clamp(argInt(args, "lines", 300), 1, 5000)
	lines := strings.Split(string(b), "\n")
	truncated := false
	if len(lines) > maxLines {
		lines, truncated = lines[:maxLines], true
	}
	text := strings.Join(lines, "\n")
	if truncated {
		text += fmt.Sprintf("\n[truncated at %d lines]", maxLines)
	}
	return text, nil
}

func (d *devSkills) grep(ctx context.Context, args map[string]any) (string, error) {
	query := argString(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	dir, err := d.resolve(argString(args, "project"))
	if err != nil {
		return "", err
	}
	limit := clamp(argInt(args, "limit", 40), 1, 200)

	// -F keeps the query literal, so regex metacharacters cannot surprise us.
	out, _ := run(ctx, 30*time.Second, "grep", "-rInF",
		"--exclude-dir=.git", "--exclude-dir=node_modules", "--exclude-dir=vendor",
		"-m", "3", query, dir)

	lines := splitNonEmpty(out)
	if len(lines) == 0 {
		if _, statErr := os.Stat(dir); statErr != nil {
			return "", fmt.Errorf("there is no %q to search — check the project name with dev_projects", argString(args, "project"))
		}
		return fmt.Sprintf("No matches for %q.", query), nil
	}
	truncated := false
	if len(lines) > limit {
		lines, truncated = lines[:limit], true
	}

	var sb strings.Builder
	for _, l := range lines {
		if rel, err := filepath.Rel(d.root, l); err == nil && !strings.HasPrefix(rel, "..") {
			l = rel
		}
		sb.WriteString(clip(l, 300) + "\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "[showing first %d matches]\n", limit)
	}
	return strings.TrimSpace(sb.String()), nil
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
