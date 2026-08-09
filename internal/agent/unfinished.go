package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/akins/jarvis/internal/wiring"
)

// Refusing to call it finished while something measurable is still broken.
//
// # What a note is worth
//
// Three interventions were tried against the same failure — a page whose links
// go nowhere — and measured on real builds:
//
//	nothing            flower shop      5 dead of 15 links
//	persona rule       grooming shop    2 dead of 13
//	rule + a note on the write   bike shop   1 dead of 16
//
// The last run is the interesting one. The note fired, correctly, naming the
// href="#" the moment she wrote the file. It is in the archive. She read it,
// wrote two more files, ran code_check three times, served the site, opened it
// on the user's screen, and said it was done — with the dead link still in it.
//
// So a note in a tool result is advice, and advice loses to the momentum of a
// task that feels finished. The thing that was actually missing is the refusal:
// the exchange should not be allowed to END while a defect it was told about is
// still standing.
//
// # The verdict comes from disk, never from the trail
//
// Because she might have fixed it, with any tool, in any order. The first
// version worked out whether a page was still broken by matching the wording of
// the write tool's output back out of the trail, and the very first time she
// repaired a page with file_edit instead of a second file_write it accused her
// of leaving a dead link in a file that was, on disk, clean. So the trail
// supplies the list of pages she touched and nothing else; internal/wiring reads
// each one as it stands now.
//
// # One push, then it lets go
//
// Fired once per exchange. If she is told plainly and still answers without
// fixing it, that is her call and the turn ends; a gate that will not take an
// answer is a hang, and the round cap is not a good place to discover that.

// touchedPath pulls the file out of a write or edit result. Both lead with a
// verb and the path; only the name is needed, because the verdict comes from
// disk afterwards rather than from anything the tool said.
var touchedPath = regexp.MustCompile(`^(?:Wrote \d+ bytes to|Created|Replaced|Rewrote|Edited) (\S+)`)

// stillOpen returns the dead ends standing at the end of the exchange, read
// from the files as they are now.
//
// The trail only supplies the LIST OF PAGES she touched. Whether any of them is
// still broken is answered by internal/wiring against the current file — the
// first version asked the trail that question too, by matching the wording of
// the write tool's output, and told her a page was unfinished after she had
// fixed it with file_edit. The file was clean and the exchange still ended with
// an accusation.
func stillOpen(work *trail) []string {
	if work == nil {
		return nil
	}
	var pages []string
	seen := map[string]bool{}
	for _, s := range work.snapshot() {
		if s.failed || (s.tool != "file_write" && s.tool != "file_edit") {
			continue
		}
		m := touchedPath.FindStringSubmatch(strings.TrimSpace(s.output))
		if m == nil {
			continue
		}
		path := strings.TrimRight(m[1], ".,:")
		// file_edit reports a base name, which cannot be re-read on its own. The
		// write that created it in the same exchange carries the full path, so an
		// edited page is already in the list; one that is not was written in an
		// earlier turn and is not this exchange's promise to keep.
		if !strings.Contains(path, "/") || !wiring.IsHTML(path) || seen[path] {
			continue
		}
		seen[path] = true
		pages = append(pages, path)
	}

	var out []string
	for _, p := range pages {
		for _, problem := range wiring.Open(p) {
			out = append(out, shortPath(p)+": "+problem)
		}
	}
	return out
}

// shortPath keeps the filename and its folder, which is enough to act on and
// short enough to read.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 2 {
		return p
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// finishBrief is the push. Written as a tool result rather than as the user
// speaking, because it is the machine reporting the state of the work — and
// putting words in the user's mouth is how a turn ends up archived as a request
// they never made.
func finishBrief(ends []string) string {
	var sb strings.Builder
	sb.WriteString("HOLD ON — you are about to call this finished and it is not.\n\n")
	sb.WriteString("Still standing, from your own writes this turn:\n")
	for _, e := range ends {
		sb.WriteString("  · " + e + "\n")
	}
	sb.WriteString("\nThese were reported to you when you wrote the file and nothing has been " +
		"done about them since. Each one is a promise on the screen with nothing behind it; " +
		"they find out by clicking.\n\n" +
		"Go back and deal with all of them now — add the section or page the link points at, " +
		"or take the link out. Both are fine. Then re-read what you changed and check the " +
		"whole folder with site_check before you answer.\n\n" +
		"While you are in there, look again with fresh eyes rather than only patching these " +
		"lines: a section that is merely acceptable is worth making good, and you are the " +
		"one who can see it. Do not answer until the work is actually done.")
	return sb.String()
}

// nudgeNote is the last word when the push did not take. The framework is the
// only party here that knows this for a fact, so it says so rather than leaving
// the user to discover it.
func nudgeNote(ends []string) string {
	if len(ends) == 1 {
		return fmt.Sprintf("\n\n[Not finished: %s. It was flagged twice and left as it is.]", ends[0])
	}
	return fmt.Sprintf("\n\n[Not finished — %d dead links left in place after being flagged "+
		"twice: %s]", len(ends), strings.Join(ends, "; "))
}
