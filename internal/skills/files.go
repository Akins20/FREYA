package skills

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/akins/jarvis/internal/docs"
	"github.com/akins/jarvis/internal/guard"
	"github.com/akins/jarvis/internal/llm"
)

// File and folder operations.
//
// # Surgical edits require a unique anchor
//
// file_edit replaces an exact string, and refuses when that string appears more
// than once. This is not pedantry: "replace foo with bar" against a file
// containing eight foos is an instruction with eight possible meanings, and
// picking one silently is how an assistant quietly corrupts a file. Ambiguity
// is reported so the caller can supply more surrounding context.
//
// # Reads are format-aware, writes are not
//
// Reading dispatches through internal/docs, so a .docx returns prose rather
// than ZIP noise. Writing deliberately does not: generating a valid Office
// container from text would produce a file that opens but loses every style
// the original had. Editing those formats belongs in the application that owns
// them.

const (
	// maxWriteBytes bounds a single write.
	maxWriteBytes = 32 << 20
	// maxReadChars bounds what is returned to the model in one call.
	maxReadChars = 100_000
	// maxTreeEntries bounds a directory walk.
	maxTreeEntries = 3000
)

// RegisterFiles adds file and folder skills, every mutating one gated.
// The place book is optional; when supplied, folder paths may be given by name.
func RegisterFiles(r *Registry, g *guard.Guard, places *PlaceBook) {
	if g == nil {
		return
	}
	f := &fileSkills{guard: g, places: places}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "file_read",
			Description: "Read a file. Understands plain text, code, PDF, Word (.docx), " +
				"Excel (.xlsx), PowerPoint (.pptx), OpenDocument and ZIP archives — " +
				"pass any of them and you get readable text back.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":       {Type: "string", Description: "Path to the file."},
				"offset":     {Type: "number", Description: "First line to return (1-based). Default 1."},
				"limit":      {Type: "number", Description: "Maximum lines to return. Default 500."},
				"whole_file": {Type: "boolean", Description: "Return everything, ignoring offset and limit."},
			}, "path"),
		},
		Handler: f.read,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "file_write",
			Description: "Create a file or replace its entire contents. For changing part " +
				"of an existing file use file_edit instead — it is far safer.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":    {Type: "string", Description: "Path to write."},
				"content": {Type: "string", Description: "The full new contents."},
				"reason":  {Type: "string", Description: "Why, shown when confirming."},
			}, "path", "content"),
		},
		Handler: f.write,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "file_edit",
			Description: "Replace an exact string in a file, leaving everything else " +
				"untouched. The old text must appear exactly once — include surrounding " +
				"lines to make it unique. Preferred over file_write for any change to " +
				"an existing file.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":     {Type: "string", Description: "Path to edit."},
				"old_text": {Type: "string", Description: "Exact text to replace, including indentation."},
				"new_text": {Type: "string", Description: "Replacement text."},
				"all":      {Type: "boolean", Description: "Replace every occurrence instead of requiring uniqueness."},
				"reason":   {Type: "string", Description: "Why, shown when confirming."},
			}, "path", "old_text", "new_text"),
		},
		Handler: f.edit,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "file_append",
			Description: "Add text to the end of a file, creating it if absent.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":    {Type: "string", Description: "Path to append to."},
				"content": {Type: "string", Description: "Text to add."},
			}, "path", "content"),
		},
		Handler: f.append,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "file_info",
			Description: "Report size, type, modification time and permissions for a path.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "Path to inspect."},
			}, "path"),
		},
		Handler: f.info,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "folder_list",
			Description: "List the contents of a directory with sizes and modification times.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":      {Type: "string", Description: "Directory to list."},
				"recursive": {Type: "boolean", Description: "Walk subdirectories too."},
				"pattern":   {Type: "string", Description: "Optional glob to filter names, e.g. '*.go'."},
			}, "path"),
		},
		Handler: f.list,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "folder_create",
			Description: "Create a directory, including any missing parents.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "Directory to create."},
			}, "path"),
		},
		Handler: f.mkdir,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "file_copy",
			Description: "Copy a file or directory to a new location.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"source":      {Type: "string", Description: "What to copy."},
				"destination": {Type: "string", Description: "Where to copy it."},
			}, "source", "destination"),
		},
		Handler: f.copy,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "file_move",
			Description: "Move or rename a file or directory.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"source":      {Type: "string", Description: "What to move."},
				"destination": {Type: "string", Description: "Where to move it."},
				"reason":      {Type: "string", Description: "Why, shown when confirming."},
			}, "source", "destination"),
		},
		Handler: f.move,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "file_delete",
			Description: "Delete files or directories. Always confirmed, with a count and " +
				"total size shown first.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":      {Type: "string", Description: "What to delete. Globs are allowed."},
				"recursive": {Type: "boolean", Description: "Required to delete a non-empty directory."},
				"reason":    {Type: "string", Description: "Why, shown when confirming."},
			}, "path"),
		},
		Handler: f.remove,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "archive_create",
			Description: "Create a ZIP archive from a file or directory.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"source":      {Type: "string", Description: "File or directory to archive."},
				"destination": {Type: "string", Description: "Path for the .zip file."},
			}, "source", "destination"),
		},
		Handler: f.archive,
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name:        "archive_extract",
			Description: "Extract a ZIP archive into a directory.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":        {Type: "string", Description: "The .zip file."},
				"destination": {Type: "string", Description: "Directory to extract into."},
			}, "path", "destination"),
		},
		Handler: f.extract,
	})
}

type fileSkills struct {
	guard  *guard.Guard
	places *PlaceBook
}

// expand resolves ~ and environment variables in a path.
func expand(p string) string {
	p = os.ExpandEnv(strings.TrimSpace(p))
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func (f *fileSkills) read(_ context.Context, args map[string]any) (string, error) {
	path := expand(argString(args, "path"))
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	doc, err := docs.Extract(path)
	if err != nil {
		return "", err
	}
	if doc.Text == "" {
		if doc.Note != "" {
			return doc.Summary(), nil
		}
		return fmt.Sprintf("%s is empty.", filepath.Base(path)), nil
	}

	header := doc.Summary()
	if argBool(args, "whole_file") {
		return header + "\n\n" + clipText(doc.Text, maxReadChars), nil
	}

	lines := strings.Split(doc.Text, "\n")
	offset := clamp(argInt(args, "offset", 1), 1, max(len(lines), 1))
	limit := clamp(argInt(args, "limit", 500), 1, 5000)

	end := min(offset-1+limit, len(lines))
	shown := lines[offset-1 : end]

	var sb strings.Builder
	sb.WriteString(header)
	fmt.Fprintf(&sb, " · lines %d-%d of %d\n\n", offset, end, len(lines))
	for i, l := range shown {
		fmt.Fprintf(&sb, "%6d  %s\n", offset+i, l)
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "\n[%d more lines]", len(lines)-end)
	}
	return clipText(sb.String(), maxReadChars), nil
}

func (f *fileSkills) write(ctx context.Context, args map[string]any) (string, error) {
	path := expand(argString(args, "path"))
	content := argRaw(args, "content")
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if len(content) > maxWriteBytes {
		return "", fmt.Errorf("content is %d bytes, over the %d limit", len(content), maxWriteBytes)
	}

	action := guard.Action{
		Kind:   guard.KindWrite,
		Paths:  []string{path},
		Reason: argString(args, "reason"),
	}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("create parent: %w", err)
			}
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("Wrote %d bytes to %s.", len(content), path), nil
	})
}

func (f *fileSkills) edit(ctx context.Context, args map[string]any) (string, error) {
	path := expand(argString(args, "path"))
	oldText := argRaw(args, "old_text")
	newText := argRaw(args, "new_text")
	if path == "" || oldText == "" {
		return "", fmt.Errorf("path and old_text are required")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	content := string(raw)

	count := strings.Count(content, oldText)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_text not found in %s — check exact whitespace and indentation",
			filepath.Base(path))
	case count > 1 && !argBool(args, "all"):
		// Refusing here is the whole point: several matches means several
		// possible meanings, and guessing silently corrupts files.
		return "", fmt.Errorf("old_text appears %d times in %s — include more surrounding "+
			"context to identify which one, or set all=true to replace every occurrence",
			count, filepath.Base(path))
	}

	updated := content
	if argBool(args, "all") {
		updated = strings.ReplaceAll(content, oldText, newText)
	} else {
		updated = strings.Replace(content, oldText, newText, 1)
	}

	reason := argString(args, "reason")
	if reason == "" {
		reason = fmt.Sprintf("replace %d line(s) in %s",
			strings.Count(oldText, "\n")+1, filepath.Base(path))
	}

	action := guard.Action{
		Kind:   guard.KindWrite,
		Paths:  []string{path},
		Reason: reason,
	}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		// Preserve the original mode rather than forcing 0644 onto a script.
		mode := fs.FileMode(0o644)
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(path, []byte(updated), mode); err != nil {
			return "", err
		}
		delta := len(updated) - len(content)
		return fmt.Sprintf("Edited %s: %d replacement(s), %+d bytes.",
			filepath.Base(path), count, delta), nil
	})
}

func (f *fileSkills) append(ctx context.Context, args map[string]any) (string, error) {
	path := expand(argString(args, "path"))
	content := argRaw(args, "content")
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	action := guard.Action{Kind: guard.KindWrite, Paths: []string{path},
		Reason: "append to " + filepath.Base(path)}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return "", err
		}
		defer file.Close()
		if _, err := file.WriteString(content); err != nil {
			return "", err
		}
		return fmt.Sprintf("Appended %d bytes to %s.", len(content), filepath.Base(path)), nil
	})
}

func (f *fileSkills) info(_ context.Context, args map[string]any) (string, error) {
	path := expand(argString(args, "path"))
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	kind := "file"
	switch {
	case info.IsDir():
		kind = "directory"
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
		if target, err := os.Readlink(path); err == nil {
			kind += " -> " + target
		}
	}
	fmt.Fprintf(&sb, "%s\n  type: %s\n  size: %s\n  modified: %s\n  mode: %s",
		path, kind, humanBytes(info.Size()),
		info.ModTime().Format("2 Jan 2006 15:04"), info.Mode().Perm())

	if !info.IsDir() {
		if format, err := docs.Detect(path); err == nil {
			fmt.Fprintf(&sb, "\n  format: %s", format)
		}
	} else {
		if entries, err := os.ReadDir(path); err == nil {
			fmt.Fprintf(&sb, "\n  entries: %d", len(entries))
		}
	}
	return sb.String(), nil
}

func (f *fileSkills) list(_ context.Context, args map[string]any) (string, error) {
	path := f.placeAware(argString(args, "path"))
	if path == "" {
		path = "."
	}
	pattern := argString(args, "pattern")

	type entry struct {
		rel  string
		size int64
		dir  bool
		mod  time.Time
	}
	var entries []entry

	if argBool(args, "recursive") {
		count := 0
		err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if count >= maxTreeEntries {
				return filepath.SkipAll
			}
			name := d.Name()
			// Skip the directories that dominate a tree and interest nobody.
			if d.IsDir() && (name == ".git" || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			if p == path {
				return nil
			}
			if pattern != "" && !d.IsDir() {
				if ok, _ := filepath.Match(pattern, name); !ok {
					return nil
				}
			}
			rel, _ := filepath.Rel(path, p)
			info, err := d.Info()
			if err != nil {
				return nil
			}
			entries = append(entries, entry{rel, info.Size(), d.IsDir(), info.ModTime()})
			count++
			return nil
		})
		if err != nil {
			return "", err
		}
	} else {
		items, err := os.ReadDir(path)
		if err != nil {
			return "", err
		}
		for _, d := range items {
			if pattern != "" && !d.IsDir() {
				if ok, _ := filepath.Match(pattern, d.Name()); !ok {
					continue
				}
			}
			info, err := d.Info()
			if err != nil {
				continue
			}
			entries = append(entries, entry{d.Name(), info.Size(), d.IsDir(), info.ModTime()})
		}
	}

	if len(entries) == 0 {
		return fmt.Sprintf("%s is empty or nothing matched.", path), nil
	}
	// Directories first, then alphabetical — how a person scans a listing.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].dir != entries[j].dir {
			return entries[i].dir
		}
		return entries[i].rel < entries[j].rel
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s — %d entries\n", path, len(entries))
	var total int64
	for _, e := range entries {
		if e.dir {
			fmt.Fprintf(&sb, "  %-50s <dir>\n", e.rel+"/")
			continue
		}
		total += e.size
		fmt.Fprintf(&sb, "  %-50s %10s  %s\n", e.rel, humanBytes(e.size),
			e.mod.Format("2 Jan 15:04"))
	}
	fmt.Fprintf(&sb, "total %s", humanBytes(total))
	return sb.String(), nil
}

func (f *fileSkills) mkdir(ctx context.Context, args map[string]any) (string, error) {
	path := expand(argString(args, "path"))
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	action := guard.Action{Kind: guard.KindWrite, Paths: []string{path},
		Reason: "create directory"}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", err
		}
		return "Created " + path + ".", nil
	})
}

func (f *fileSkills) copy(ctx context.Context, args map[string]any) (string, error) {
	src := expand(argString(args, "source"))
	dst := expand(argString(args, "destination"))
	if src == "" || dst == "" {
		return "", fmt.Errorf("source and destination are required")
	}

	action := guard.Action{Kind: guard.KindWrite, Paths: []string{src, dst},
		Reason: "copy " + filepath.Base(src)}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		n, err := copyPath(src, dst)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Copied %d item(s) to %s.", n, dst), nil
	})
}

func (f *fileSkills) move(ctx context.Context, args map[string]any) (string, error) {
	src := expand(argString(args, "source"))
	dst := expand(argString(args, "destination"))
	if src == "" || dst == "" {
		return "", fmt.Errorf("source and destination are required")
	}

	action := guard.Action{Kind: guard.KindMove, Paths: []string{src, dst},
		Reason: argString(args, "reason")}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		if err := os.Rename(src, dst); err != nil {
			// Rename fails across filesystems, which is routine here given the
			// NTFS drive; fall back to copy-then-delete.
			if _, cerr := copyPath(src, dst); cerr != nil {
				return "", fmt.Errorf("move failed: %w", err)
			}
			if rerr := os.RemoveAll(src); rerr != nil {
				return "", fmt.Errorf("copied but could not remove original: %w", rerr)
			}
		}
		return fmt.Sprintf("Moved %s to %s.", src, dst), nil
	})
}

func (f *fileSkills) remove(ctx context.Context, args map[string]any) (string, error) {
	pattern := expand(argString(args, "path"))
	if pattern == "" {
		return "", fmt.Errorf("path is required")
	}

	targets := []string{pattern}
	if strings.ContainsAny(pattern, "*?[") {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", fmt.Errorf("bad pattern: %w", err)
		}
		if len(matches) == 0 {
			return fmt.Sprintf("Nothing matches %s.", pattern), nil
		}
		targets = matches
	}

	// A non-empty directory needs the recursive flag stated explicitly, so
	// "delete this folder" cannot quietly become "delete everything under it".
	if !argBool(args, "recursive") {
		for _, t := range targets {
			if info, err := os.Stat(t); err == nil && info.IsDir() {
				if entries, err := os.ReadDir(t); err == nil && len(entries) > 0 {
					return "", fmt.Errorf("%s is a non-empty directory (%d entries) — "+
						"set recursive=true to delete it and everything inside",
						filepath.Base(t), len(entries))
				}
			}
		}
	}

	action := guard.Action{Kind: guard.KindDelete, Paths: targets,
		Reason: argString(args, "reason")}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		removed := 0
		for _, t := range targets {
			if err := os.RemoveAll(t); err != nil {
				return "", fmt.Errorf("removing %s: %w", t, err)
			}
			removed++
		}
		return fmt.Sprintf("Deleted %d item(s).", removed), nil
	})
}

func (f *fileSkills) archive(ctx context.Context, args map[string]any) (string, error) {
	src := expand(argString(args, "source"))
	dst := expand(argString(args, "destination"))
	if src == "" || dst == "" {
		return "", fmt.Errorf("source and destination are required")
	}
	if !strings.HasSuffix(strings.ToLower(dst), ".zip") {
		dst += ".zip"
	}

	action := guard.Action{Kind: guard.KindWrite, Paths: []string{dst},
		Reason: "create archive from " + filepath.Base(src)}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		n, size, err := zipPath(src, dst)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Archived %d file(s) into %s (%s).", n, dst, humanBytes(size)), nil
	})
}

func (f *fileSkills) extract(ctx context.Context, args map[string]any) (string, error) {
	src := expand(argString(args, "path"))
	dst := expand(argString(args, "destination"))
	if src == "" || dst == "" {
		return "", fmt.Errorf("path and destination are required")
	}

	action := guard.Action{Kind: guard.KindWrite, Paths: []string{dst},
		Reason: "extract " + filepath.Base(src)}
	return f.guard.Run(ctx, action, func(context.Context) (string, error) {
		n, err := unzipPath(src, dst)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Extracted %d file(s) into %s.", n, dst), nil
	})
}

// --- helpers ----------------------------------------------------------------

func copyPath(src, dst string) (int, error) {
	info, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 1, copyFile(src, dst, info.Mode())
	}

	count := 0
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		count++
		return copyFile(p, target, info.Mode())
	})
	return count, err
}

func copyFile(src, dst string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func zipPath(src, dst string) (count int, total int64, err error) {
	out, err := os.Create(dst)
	if err != nil {
		return 0, 0, err
	}
	defer out.Close()

	w := zip.NewWriter(out)
	defer w.Close()

	info, err := os.Stat(src)
	if err != nil {
		return 0, 0, err
	}
	base := filepath.Dir(src)
	if !info.IsDir() {
		base = filepath.Dir(src)
	}

	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		f, err := w.Create(rel)
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		n, err := io.Copy(f, in)
		total += n
		count++
		return err
	})
	return count, total, err
}

func unzipPath(src, dst string) (int, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}

	count := 0
	for _, f := range r.File {
		target := filepath.Join(dst, f.Name)

		// Zip-slip: a crafted archive can contain "../../etc/passwd" and escape
		// the destination entirely. Every entry is checked before it is written.
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) {
			return count, fmt.Errorf("archive entry %q escapes the destination directory", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return count, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return count, err
		}

		rc, err := f.Open()
		if err != nil {
			return count, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return count, err
		}
		_, err = io.Copy(out, io.LimitReader(rc, maxZipEntry))
		out.Close()
		rc.Close()
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

const maxZipEntry = 512 << 20

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n[truncated at %d characters]", n)
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

// RegisterDocWriting adds the ability to produce Word and Excel files.
//
// Reading was already format-aware; without this, "read my notes and give me a
// docx" fails at the last step, which is the step that matters to the user.
func RegisterDocWriting(r *Registry, g *guard.Guard) {
	if g == nil {
		return
	}

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "docx_write",
			Description: "Create a Word document. Supply the body as markdown — " +
				"# headings, - bullets, | pipe tables | and blank-line-separated " +
				"paragraphs are all converted to real Word formatting.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":    {Type: "string", Description: "Where to write the .docx."},
				"content": {Type: "string", Description: "Document body as markdown."},
				"reason":  {Type: "string", Description: "Why, shown when confirming."},
			}, "path", "content"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path := expand(argString(args, "path"))
			content := argRaw(args, "content")
			if path == "" || content == "" {
				return "", fmt.Errorf("path and content are required")
			}
			if !strings.HasSuffix(strings.ToLower(path), ".docx") {
				path += ".docx"
			}

			action := guard.Action{Kind: guard.KindWrite, Paths: []string{path},
				Reason: argString(args, "reason")}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				blocks := docs.ParseBlocks(content)
				if err := docs.WriteDOCX(path, blocks); err != nil {
					return "", err
				}
				info, _ := os.Stat(path)
				var size int64
				if info != nil {
					size = info.Size()
				}
				return fmt.Sprintf("Wrote %s — %d blocks, %d words, %s.",
					path, len(blocks), len(strings.Fields(content)), humanBytes(size)), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "pdf_write",
			Description: "Create a PDF. Supply the body as markdown, exactly as for " +
				"docx_write — it is laid out as a Word document and then rendered, so " +
				"the PDF and a docx of the same content look identical.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path":    {Type: "string", Description: "Where to write the .pdf."},
				"content": {Type: "string", Description: "Document body as markdown."},
				"reason":  {Type: "string", Description: "Why, shown when confirming."},
			}, "path", "content"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path := expand(argString(args, "path"))
			content := argRaw(args, "content")
			if path == "" || content == "" {
				return "", fmt.Errorf("path and content are required")
			}
			if !strings.HasSuffix(strings.ToLower(path), ".pdf") {
				path += ".pdf"
			}

			action := guard.Action{Kind: guard.KindWrite, Paths: []string{path},
				Reason: argString(args, "reason")}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				blocks := docs.ParseBlocks(content)
				if err := docs.WritePDF(path, blocks); err != nil {
					return "", err
				}
				info, _ := os.Stat(path)
				var size int64
				if info != nil {
					size = info.Size()
				}
				return fmt.Sprintf("Wrote %s — %d blocks, %s.",
					path, len(blocks), humanBytes(size)), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "document_convert",
			Description: "Convert a document between formats — docx, pdf, odt, xlsx, " +
				"csv, html, txt. Use for 'turn this into a PDF' on a file that already " +
				"exists, rather than regenerating it.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "The existing file."},
				"format": {Type: "string", Description: "Target format.",
					Enum: []string{"pdf", "docx", "odt", "xlsx", "csv", "html", "txt"}},
				"destination": {Type: "string", Description: "Optional output directory. " +
					"Defaults to alongside the source."},
			}, "path", "format"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			src := expand(argString(args, "path"))
			format := argString(args, "format")
			if src == "" || format == "" {
				return "", fmt.Errorf("path and format are required")
			}
			if _, err := os.Stat(src); err != nil {
				return "", fmt.Errorf("no such file: %s", src)
			}
			outDir := expand(argString(args, "destination"))
			if outDir == "" {
				outDir = filepath.Dir(src)
			}
			target := filepath.Join(outDir,
				strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+"."+format)

			action := guard.Action{Kind: guard.KindWrite, Paths: []string{target},
				Reason: fmt.Sprintf("convert %s to %s", filepath.Base(src), format)}
			return g.Run(ctx, action, func(ctx context.Context) (string, error) {
				// PDF is a dead end for every converter on this machine:
				// LibreOffice imports it into Draw, which has no Word export
				// filter, and the attempt fails with "no export filter found".
				// Extracting the text and rebuilding a document is the route
				// that actually produces something usable.
				if strings.EqualFold(filepath.Ext(src), ".pdf") &&
					(format == "docx" || format == "odt") {
					doc, err := docs.Extract(src)
					if err != nil {
						return "", err
					}
					if strings.TrimSpace(doc.Text) == "" {
						return "", fmt.Errorf("%s has no text layer — it is probably "+
							"scanned images and would need OCR", filepath.Base(src))
					}
					blocks := docs.Reconstruct(doc.Text)
					if err := docs.WriteDOCX(target, blocks); err != nil {
						return "", err
					}
					info, _ := os.Stat(target)
					var size int64
					if info != nil {
						size = info.Size()
					}
					return fmt.Sprintf("Rebuilt %s as %s — %d blocks, %s. "+
						"Structure is inferred from the text layer, since PDF records "+
						"words rather than intent; check the headings.",
						filepath.Base(src), target, len(blocks), humanBytes(size)), nil
				}

				if !have("soffice") {
					return "", fmt.Errorf("conversion needs LibreOffice (soffice)")
				}
				if _, err := run(ctx, 180*time.Second, "soffice", "--headless",
					"--norestore", "--convert-to", format, src, "--outdir", outDir); err != nil {
					return "", err
				}
				info, err := os.Stat(target)
				if err != nil {
					return "", fmt.Errorf("conversion reported success but produced no %s", format)
				}
				return fmt.Sprintf("Converted to %s (%s).", target, humanBytes(info.Size())), nil
			})
		},
	})

	r.Register(Skill{
		Tool: llm.Tool{
			Name: "xlsx_write",
			Description: "Create an Excel spreadsheet. Supply rows as CSV text; use " +
				"'---SHEET: Name---' on its own line to start a new sheet. Values that " +
				"look numeric are stored as real numbers so they can be summed and charted.",
			Params: llm.ObjectSchema(map[string]llm.Property{
				"path": {Type: "string", Description: "Where to write the .xlsx."},
				"content": {Type: "string", Description: "CSV rows, optionally split " +
					"into sheets with '---SHEET: Name---' separators."},
				"reason": {Type: "string", Description: "Why, shown when confirming."},
			}, "path", "content"),
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			path := expand(argString(args, "path"))
			content := argRaw(args, "content")
			if path == "" || content == "" {
				return "", fmt.Errorf("path and content are required")
			}
			if !strings.HasSuffix(strings.ToLower(path), ".xlsx") {
				path += ".xlsx"
			}

			sheets := parseSheets(content)
			if len(sheets) == 0 {
				return "", fmt.Errorf("no rows found in content")
			}

			action := guard.Action{Kind: guard.KindWrite, Paths: []string{path},
				Reason: argString(args, "reason")}
			return g.Run(ctx, action, func(context.Context) (string, error) {
				if err := docs.WriteXLSX(path, sheets); err != nil {
					return "", err
				}
				rows := 0
				for _, s := range sheets {
					rows += len(s.Rows)
				}
				info, _ := os.Stat(path)
				var size int64
				if info != nil {
					size = info.Size()
				}
				return fmt.Sprintf("Wrote %s — %d sheet(s), %d rows, %s.",
					path, len(sheets), rows, humanBytes(size)), nil
			})
		},
	})
}

// parseSheets splits CSV text into sheets, honouring ---SHEET: Name--- markers.
func parseSheets(content string) []docs.Sheet {
	var sheets []docs.Sheet
	current := docs.Sheet{Name: "Sheet1"}

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "---SHEET:") {
			if len(current.Rows) > 0 {
				sheets = append(sheets, current)
			}
			name := strings.TrimSpace(strings.TrimSuffix(
				strings.TrimPrefix(trimmed, "---SHEET:"), "---"))
			current = docs.Sheet{Name: name}
			continue
		}
		if trimmed == "" {
			continue
		}
		current.Rows = append(current.Rows, splitCSVLine(trimmed))
	}
	if len(current.Rows) > 0 {
		sheets = append(sheets, current)
	}
	return sheets
}

// splitCSVLine splits a CSV row, honouring quoted fields so a comma inside a
// quoted cell does not silently create an extra column.
func splitCSVLine(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			// A doubled quote inside a quoted field is a literal quote.
			if inQuote && i+1 < len(line) && line[i+1] == '"' {
				cur.WriteByte('"')
				i++
				continue
			}
			inQuote = !inQuote
		case c == ',' && !inQuote:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, strings.TrimSpace(cur.String()))
	return out
}

// ResolvePlaces lets file paths accept remembered names.
//
// Real paths always win. A place called "documents" must never hijack an actual
// ./documents directory, so the lookup only runs when the literal path does not
// exist.
func (f *fileSkills) ResolvePlaces(pb *PlaceBook) { f.places = pb }

// placeAware resolves a path, falling back to the place book by name.
func (f *fileSkills) placeAware(path string) string {
	resolved := expand(path)
	if _, err := os.Stat(resolved); err == nil {
		return resolved
	}
	if f.places == nil {
		return resolved
	}
	// Only a bare name can be a place; anything with a separator is a path
	// the user got wrong, and silently redirecting it would be worse.
	if strings.ContainsRune(path, os.PathSeparator) {
		return resolved
	}
	if p, ok := f.places.Resolve(path); ok {
		return p.Path
	}
	return resolved
}
