// Package wiring answers one question about a page: does everything on it that
// looks like it leads somewhere actually lead somewhere.
//
// # Why it is its own package
//
// Two callers need the same verdict and must not disagree about it. The write
// tool reports a page's dead ends the moment she writes it; the agent re-checks
// at the end of the exchange and refuses to call the work finished while any
// remain. The first version had the check in the skill and had the agent read
// the wording of the skill's output back out of the tool trail — which broke the
// first time she fixed a page with file_edit instead of file_write. The file was
// clean on disk and the exchange still ended with "Not finished", because the
// agent was replaying what a tool had said rather than looking.
//
// So: one implementation, and the answer always comes from the file as it is
// now.
//
// # What counts as a dead end
//
// Only what is decidable from the file itself, for the write-time check: an
// href that goes nowhere, and an anchor to an id that is not on the page. Every
// dead end in her real output has been one of those two — five in the flower
// shop, two in the grooming salon, one in the bike shop — and both are immune to
// the ordering problem that makes cross-file checks useless mid-build, where
// index.html routinely links the stylesheet that gets written next. Page() adds
// the cross-file checks once the folder is complete.
//
// # The rule that has to be right before it is strict
//
// A form rule that only looked for action= reported a form wired with onsubmit,
// and she duly "fixed" it by replacing the working handler with
// action="javascript:void(0)". A checker that is wrong does not merely get
// ignored; it gets obeyed, and the code gets worse. Every rule here treats every
// legitimate way of wiring something as wired, and stays quiet when it cannot
// tell.
package wiring

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	reHref   = regexp.MustCompile(`href\s*=\s*"([^"]*)"`)
	reSrc    = regexp.MustCompile(`src\s*=\s*"([^"]*)"`)
	reID     = regexp.MustCompile(`id\s*=\s*"([^"]+)"`)
	reName   = regexp.MustCompile(`name\s*=\s*"([^"]+)"`)
	reForm   = regexp.MustCompile(`(?is)<form[^>]*>`)
	reButton = regexp.MustCompile(`(?is)<button[^>]*>`)
	reAction = regexp.MustCompile(`(?is)action\s*=\s*"([^"]*)"`)
)

// IsHTML reports whether a path is a page worth checking.
func IsHTML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".html" || ext == ".htm"
}

// InPage returns the dead ends decidable from one file's own content, in the
// words to show her. Empty means nothing on this page makes a promise it cannot
// keep — as far as this file alone can say.
func InPage(content string) []string {
	ids := map[string]bool{}
	for _, m := range reID.FindAllStringSubmatch(content, -1) {
		ids[m[1]] = true
	}
	// A named anchor is as good as an id to jump to.
	for _, m := range reName.FindAllStringSubmatch(content, -1) {
		ids[m[1]] = true
	}

	var nowhere, missing []string
	seen := map[string]bool{}
	for _, m := range reHref.FindAllStringSubmatch(content, -1) {
		h := strings.TrimSpace(m[1])
		switch {
		case h == "" || h == "#" || isNoop(h):
			if !seen["nowhere"] {
				seen["nowhere"] = true
				nowhere = append(nowhere, `href="`+m[1]+`"`)
			}
		case strings.HasPrefix(h, "#"):
			if t := h[1:]; !ids[t] && !seen[h] {
				seen[h] = true
				missing = append(missing, h)
			}
		}
	}

	// A form whose action is a no-op swallows what they typed. Checked here
	// rather than only in Page, because it is decidable from the file — but only
	// when the action IS a no-op. A form with no action at all is handled by its
	// onsubmit, or by a listener on its id, and saying otherwise is what made her
	// throw a working handler away.
	for _, f := range reForm.FindAllString(content, -1) {
		if m := reAction.FindStringSubmatch(f); m != nil {
			if a := strings.TrimSpace(m[1]); a == "" || a == "#" || isNoop(a) {
				if !seen["form"] {
					seen["form"] = true
					nowhere = append(nowhere, fmt.Sprintf("a form with action=%q", m[1]))
				}
			}
		}
	}

	var out []string
	if len(nowhere) > 0 {
		out = append(out, strings.Join(nowhere, ", ")+" — goes nowhere")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		out = append(out, strings.Join(missing, ", ")+" — no element on this page has that id")
	}
	return out
}

// isNoop covers the several ways of writing "do nothing" that look like a
// destination. javascript:void(0) is the one she reached for when a rule of mine
// was wrong about forms.
func isNoop(ref string) bool {
	r := strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(r, "javascript:") && (strings.Contains(r, "void") ||
		r == "javascript:" || r == "javascript:;")
}

// Note is the write-time report, appended to a tool result. Empty when the page
// is clean or the file is not a page.
func Note(path, content string) string {
	if !IsHTML(path) {
		return ""
	}
	problems := InPage(content)
	if len(problems) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n[This page makes promises it does not keep: %s. Clicking any of "+
		"them does nothing. Either add what they point at or take the link out, before you "+
		"call this finished — and run site_check on the folder when the rest is written, for "+
		"the ones that span files.]", strings.Join(problems, "; "))
}

// Open re-reads a page from disk and says what is still wrong with it. Used at
// the end of an exchange, where the only trustworthy answer is the current file
// — she may have fixed it with any tool, or with three.
func Open(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		// Gone or unreadable is not a dead link, and guessing here would produce
		// exactly the false accusation this package exists to stop making.
		return nil
	}
	return InPage(string(raw))
}

// Page is the full check for a finished folder: everything InPage finds, plus
// the references that can only be resolved against what is on disk beside it.
func Page(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("%s could not be read: %v", filepath.Base(path), err)}
	}
	s := string(raw)
	base := filepath.Base(path)
	dir := filepath.Dir(path)

	var out []string
	seen := map[string]bool{}
	add := func(msg string) {
		if !seen[msg] {
			seen[msg] = true
			out = append(out, msg)
		}
	}

	for _, p := range InPage(s) {
		add(base + ": " + p)
	}

	for _, m := range reHref.FindAllStringSubmatch(s, -1) {
		h := strings.TrimSpace(m[1])
		if isLocalRef(h) && !exists(dir, h) {
			add(fmt.Sprintf("%s: links to %s, which is not on disk — build that page or "+
				"take the link out", base, h))
		}
	}
	for _, m := range reSrc.FindAllStringSubmatch(s, -1) {
		if src := strings.TrimSpace(m[1]); isLocalRef(src) && !exists(dir, src) {
			add(fmt.Sprintf("%s: loads %s, which is not on disk — it will render as a "+
				"broken image or a missing script", base, src))
		}
	}

	// A button does something if a script can reach it or a form surrounds it.
	// Both are invisible to a regex, so this only fires when the page has
	// neither — the one case where no button on it can possibly do anything.
	if n := len(reButton.FindAllString(s, -1)); n > 0 {
		lower := strings.ToLower(s)
		if !strings.Contains(lower, "addeventlistener") && !strings.Contains(lower, "onclick") &&
			!strings.Contains(lower, "<script") && !strings.Contains(lower, "<form") {
			add(fmt.Sprintf("%s: %d button(s), and the page has no script and no form — "+
				"nothing happens when they are pressed", base, n))
		}
	}
	return out
}

// isLocalRef reports whether a reference points at something that ought to be on
// disk beside the page.
func isLocalRef(ref string) bool {
	if ref == "" {
		return false
	}
	for _, skip := range []string{"http://", "https://", "//", "mailto:", "tel:", "data:", "#", "javascript:"} {
		if strings.HasPrefix(strings.ToLower(ref), skip) {
			return false
		}
	}
	return true
}

// exists resolves a reference relative to the page, ignoring any query or hash.
func exists(dir, ref string) bool {
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	if ref == "" {
		return true
	}
	if filepath.IsAbs(ref) {
		_, err := os.Stat(ref)
		return err == nil
	}
	_, err := os.Stat(filepath.Join(dir, ref))
	return err == nil
}
