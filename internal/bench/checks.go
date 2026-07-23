package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akins/jarvis/internal/docs"
)

// Check helpers.
//
// These are the vocabulary benchmarks are written in, and they encode what
// "strict" means here: an artifact must exist, and its *content* must be right.
// A benchmark that only checked a file existed would pass on an empty file; one
// that reads a generated docx and asserts it contains the figures from three
// source files cannot be faked by a plausible-sounding reply. The document
// helpers reuse the same extractor the assistant itself uses, so a docx is
// verified as a reader would see it, not by grepping XML.

// Path joins a relative path against the workspace.
func (w *World) Path(rel string) string { return filepath.Join(w.Dir, rel) }

// Exists reports whether a workspace-relative file is present and non-empty.
func (w *World) Exists(rel string) bool {
	info, err := os.Stat(w.Path(rel))
	return err == nil && !info.IsDir() && info.Size() > 0
}

// Find returns the first workspace file matching a glob, or "".
func (w *World) Find(glob string) string {
	matches, _ := filepath.Glob(filepath.Join(w.Dir, glob))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && !info.IsDir() && info.Size() > 0 {
			return m
		}
	}
	return ""
}

// ReadText returns the plain text of a file, extracting from docx/xlsx/pdf when
// needed so a Check can assert on what the document actually says.
func (w *World) ReadText(rel string) (string, error) {
	path := rel
	if !filepath.IsAbs(rel) {
		path = w.Path(rel)
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	doc, err := docs.Extract(path)
	if err != nil {
		// Fall back to raw bytes for formats the extractor does not model.
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return "", err
		}
		return string(raw), nil
	}
	return doc.Text, nil
}

// ---- outcome predicates, each returning (pass, reason) ----------------------

// FileHas passes when a file exists and its text contains every needle
// (case-insensitive).
func FileHas(rel string, needles ...string) func(*World) (bool, string) {
	return func(w *World) (bool, string) {
		text, err := w.ReadText(rel)
		if err != nil {
			return false, fmt.Sprintf("%s was not created", rel)
		}
		low := strings.ToLower(text)
		var missing []string
		for _, n := range needles {
			if !strings.Contains(low, strings.ToLower(n)) {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			return false, fmt.Sprintf("%s is missing: %s", rel, strings.Join(missing, ", "))
		}
		return true, fmt.Sprintf("%s contains all %d required items", rel, len(needles))
	}
}

// GlobHas passes when some file matching glob contains every needle.
func GlobHas(glob string, needles ...string) func(*World) (bool, string) {
	return func(w *World) (bool, string) {
		matches, _ := filepath.Glob(filepath.Join(w.Dir, glob))
		if len(matches) == 0 {
			return false, "no file matched " + glob
		}
		for _, m := range matches {
			text, err := w.ReadText(m)
			if err != nil {
				continue
			}
			low := strings.ToLower(text)
			ok := true
			for _, n := range needles {
				if !strings.Contains(low, strings.ToLower(n)) {
					ok = false
					break
				}
			}
			if ok {
				return true, filepath.Base(m) + " contains all required items"
			}
		}
		return false, fmt.Sprintf("a file matched %s but none contained all of: %s", glob, strings.Join(needles, ", "))
	}
}

// UsedTool passes when a tool was called at least min times.
func UsedTool(name string, min int) func(*World) (bool, string) {
	return func(w *World) (bool, string) {
		n := 0
		for _, c := range w.ToolCalls {
			if c == name {
				n++
			}
		}
		if n >= min {
			return true, fmt.Sprintf("%s used %d times", name, n)
		}
		return false, fmt.Sprintf("%s used %d times, needed %d", name, n, min)
	}
}

// Drove passes when the agent actually did substantial work rather than
// answering conversationally — a floor under "it must be an agent, not a chat".
func Drove(minTools int) func(*World) (bool, string) {
	return func(w *World) (bool, string) {
		if len(w.ToolCalls) >= minTools {
			return true, fmt.Sprintf("drove %d tool calls", len(w.ToolCalls))
		}
		return false, fmt.Sprintf("only %d tool calls — did not drive the work (needed %d)", len(w.ToolCalls), minTools)
	}
}

// chatbotTells are phrases that betray a task abandoned mid-flight instead of
// finished. A super-agent does not sign off with any of these.
var chatbotTells = []string{
	"i'll get started", "i'll work on it", "i will work on it",
	"let me know if you'd like me to", "i'll begin", "i can do that for you",
	"still loading", "is loading", "you'll need to", "please try again",
	"i'll go ahead and", "would you like me to proceed",
}

// FinishedCleanly fails a reply that trails off into a promise or a stall
// instead of a completed result. Combine with an artifact check; alone it is
// only a smell test.
func FinishedCleanly() func(*World) (bool, string) {
	return func(w *World) (bool, string) {
		low := strings.ToLower(w.Reply)
		for _, tell := range chatbotTells {
			if strings.Contains(low, tell) {
				return false, "reply stalled like a chatbot: " + tell
			}
		}
		return true, "finished without stalling"
	}
}

// All passes only when every sub-check passes; the reason names the first
// failure, or summarises success.
func All(checks ...func(*World) (bool, string)) func(*World) (bool, string) {
	return func(w *World) (bool, string) {
		for _, c := range checks {
			ok, reason := c(w)
			if !ok {
				return false, reason
			}
		}
		return true, fmt.Sprintf("all %d checks passed", len(checks))
	}
}

// ---- setup helpers ----------------------------------------------------------

// WriteFile creates a workspace file during Setup.
func WriteFile(rel, content string) func(string) error {
	return func(workdir string) error {
		path := filepath.Join(workdir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
}

// Setups runs several setup steps in order.
func Setups(steps ...func(string) error) func(string) error {
	return func(workdir string) error {
		for _, s := range steps {
			if err := s(workdir); err != nil {
				return err
			}
		}
		return nil
	}
}
