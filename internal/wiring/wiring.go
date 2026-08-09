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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

// StyleNote is the house-style count, appended to a write. Separate from Note
// because it applies to stylesheets too, and because a dead link and an em dash
// are different kinds of problem and should not arrive as one paragraph.
func StyleNote(path, content string) string {
	found := HouseStyle(path, content)
	if len(found) == 0 {
		return ""
	}
	return "\n\n[House style, counted: " + strings.Join(found, "; ") +
		". These are the tells that make work read as generated, and they are the ones no " +
		"instruction has moved — so they are counted instead. Fix them now while the file " +
		"is in front of you.]"
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

// reRemote finds the URLs a page depends on but does not own: img and script
// src, stylesheet href, and CSS url() — the last matters most, because a
// background-image is invisible to every check that only reads HTML.
var reRemote = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*"(https?://[^"]+)"|url\(\s*['"]?(https?://[^'")]+)`)

// Remote reports the external assets a folder depends on that do not answer.
//
// # Why this is worth a network call
//
// A pottery site passed every check here — four pages, fifty-seven links, none
// dead — and rendered with two blank tiles, because two of its six background
// images were Unsplash photo IDs that do not exist. She had invented them. The
// markup is perfect, the files are all on disk, and a third of the gallery is
// empty when anyone opens it.
//
// Nothing local can catch that. The URL is well-formed, it is in a stylesheet
// rather than the HTML, and the only way to know is to ask.
//
// # Bounded, and honest when it cannot tell
//
// HEAD, a short timeout, a cap on how many. A checker that hangs on a slow host
// is a checker that gets switched off. A URL that times out is reported as
// unknown rather than broken — accusing a page of a dead image because the
// network was busy is the failure mode this package exists to avoid.
func Remote(dir string, timeout time.Duration, max int) (broken []string, unknown int) {
	urls := map[string][]string{} // url -> the files that want it
	var order []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !IsHTML(e.Name()) && ext != ".css" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, m := range reRemote.FindAllStringSubmatch(string(raw), -1) {
			u := m[1]
			if u == "" {
				u = m[2]
			}
			if u == "" {
				continue
			}
			if _, seen := urls[u]; !seen {
				order = append(order, u)
			}
			urls[u] = append(urls[u], e.Name())
		}
	}
	if len(order) > max {
		order = order[:max]
	}

	client := &http.Client{Timeout: timeout}
	type verdict struct {
		url  string
		code int
		err  error
	}
	out := make(chan verdict, len(order))
	sem := make(chan struct{}, 6)
	for _, u := range order {
		go func(u string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			// HEAD first; some CDNs refuse it, so a rejection falls back to a GET
			// that is abandoned as soon as the status is known.
			resp, err := client.Head(u)
			if err != nil || resp.StatusCode == http.StatusMethodNotAllowed {
				if resp != nil {
					resp.Body.Close()
				}
				resp, err = client.Get(u)
			}
			if err != nil {
				out <- verdict{u, 0, err}
				return
			}
			resp.Body.Close()
			out <- verdict{u, resp.StatusCode, nil}
		}(u)
	}

	seen := map[string]bool{}
	for range order {
		v := <-out
		switch {
		case v.err != nil:
			unknown++
		case v.code >= 400:
			if !seen[v.url] {
				seen[v.url] = true
				broken = append(broken, fmt.Sprintf("%s: %s returns %d — it will render "+
					"as an empty box. That image does not exist; find one that does, or "+
					"stop referencing it",
					strings.Join(dedupe(urls[v.url]), ", "), clipURL(v.url), v.code))
			}
		}
	}
	sort.Strings(broken)
	return broken, unknown
}

// clipURL keeps a URL readable in a report.
func clipURL(u string) string {
	if i := strings.IndexByte(u, '?'); i > 0 {
		u = u[:i]
	}
	if len(u) > 88 {
		return u[:88] + "…"
	}
	return u
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// House style, for the tells that no instruction has ever moved.
//
// # Why these and not the others
//
// Most of the generated-look tells went to zero when the design playbook named
// them: cards, emoji, auto-fit grids, 135deg gradients, 768px breakpoints. Two
// did not, and one got worse under a stronger rule.
//
//	                   named descriptively   named as a hard count
//	em dashes                  5                      7
//	uppercase eyebrows         1                      4
//
// The rule for em dashes was rewritten from "em dashes give you away" to "ZERO
// EM DASHES. Not one." and the count went up. That rules out the theory that
// naming a removable thing is what works, because this is as removable and as
// named as anything gets.
//
// The difference is what kind of decision each one is. A card is a structural
// choice made once, deliberately, and a rule can reach it. An em dash is
// punctuation emitted mid-sentence by a habit far below the level any
// instruction operates at. Asking a model not to reach for a token it reaches
// for constantly is asking the wrong layer.
//
// So they get counted instead, like everything else here that stuck. Reported,
// never rewritten: silently editing her prose would make the page better and
// teach her nothing, and the point is that she stops producing them.
func HouseStyle(path, content string) []string {
	var out []string
	switch {
	case IsHTML(path):
		// Only the prose. An em dash inside a <style> or <script> block is not
		// something a reader sees.
		text := stripTags(content)
		if n := strings.Count(text, "—"); n > 0 {
			out = append(out, fmt.Sprintf("%d em dash(es) in the copy — every one of them is a "+
				"full stop, a comma, or two sentences", n))
		}
		// Cards drift back. Zero when the playbook first named them, then three,
		// then four, then eleven on a page rewritten to ACT on a review that asked
		// for more visual variety — because "vary the layout" gets implemented as
		// more boxes. Counted for the same reason as the em dashes: the rule is
		// read, agreed with, and then not followed.
		if n := len(reCardClass.FindAllString(content, -1)); n > cardsPerPage {
			out = append(out, fmt.Sprintf("%d card elements — a page made of boxes. Some of "+
				"these are a list, a table, or just text with space around it", n))
		}
	case strings.EqualFold(filepath.Ext(path), ".css"):
		if n := len(reUppercase.FindAllString(content, -1)); n > 1 {
			out = append(out, fmt.Sprintf("%d uppercase letter-spaced elements — one eyebrow "+
				"on a page, or none", n))
		}
		if n := strings.Count(content, "135deg"); n > 0 {
			out = append(out, fmt.Sprintf("%d gradient(s) at 135deg — take the angle from the "+
				"layout, or use 180deg", n))
		}
		if n := strings.Count(content, "auto-fit"); n > 0 {
			out = append(out, fmt.Sprintf("%d auto-fit grid(s) — decide how many columns the "+
				"content wants", n))
		}
	}
	return out
}

// cardsPerPage is where a few grouped things becomes a page of boxes. Generous:
// three is a normal row and this is not meant to fire on it.
const cardsPerPage = 4

var (
	reCardClass = regexp.MustCompile(`(?i)class\s*=\s*"[^"]*\bcard\b[^"]*"`)
	reUppercase = regexp.MustCompile(`(?i)text-transform\s*:\s*uppercase`)
	// Two patterns rather than one with a backreference: Go's regexp is RE2 and
	// has none, and MustCompile panics at init, so this is caught by any test at
	// all rather than at runtime on a page with a <script> in it.
	reScript = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	reStyle  = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	reAnyTag = regexp.MustCompile(`(?s)<[^>]*>`)
)

// stripTags leaves roughly what a reader sees, which is all this needs: the
// count only has to be right about prose, and a stray attribute value counted or
// missed changes nothing about the advice.
func stripTags(html string) string {
	html = reScript.ReplaceAllString(html, " ")
	html = reStyle.ReplaceAllString(html, " ")
	return reAnyTag.ReplaceAllString(html, " ")
}
