// Package playbook holds Freya's skills — her know-how, as distinct from her
// tools.
//
// # The distinction, which matters
//
// A tool is a verb she can perform: open a browser tab, click a selector, write
// a file, search the web. A skill is knowledge about how to perform a *kind of
// work* well — the hard-won practice that a raw tool does not carry. "Click this
// selector" is a tool; "modern portals lazy-load, so wait for real content and
// work the pagination before deciding something is missing" is a skill.
//
// The tools live in internal/skills (a historical name — those are the verbs).
// The skills — the playbooks — live here, and she consults the relevant one
// before that class of work, exactly as a competent person reads the runbook
// before touching an unfamiliar system. This is what raises her ceiling: the
// tools were never the limit; knowing how to use them together was.
//
// Playbooks are embedded strings, not files: always present, no path to resolve,
// no dependency to load, consistent with the rest of this project.
package playbook

import (
	"fmt"
	"sort"
	"strings"
)

// Skill is one playbook: a named body of know-how with a one-line summary so she
// can tell, from the index alone, when to reach for it.
type Skill struct {
	Name    string
	Summary string
	Body    string
}

// skills is the library, keyed by name.
var skills = map[string]Skill{
	"web": {
		Name:    "web",
		Summary: "driving a web page — waiting for real content, pagination, clicking things that resist",
		Body: `DRIVING A WEB PAGE

Modern sites are applications. The page arrives before its content does, lists
load in the background, and controls do nothing until their handlers attach.
Reading a page the instant it "loads" shows you skeletons, not data. Work a page
in this order, every time:

1. WAIT FOR THE REAL CONTENT, NOT THE SHELL.
   After opening a page or clicking something that changes it, the content you
   want is not there yet. If what you read is empty rows, grey skeleton bars,
   spinners, or the word "loading", that is the page mid-breath — not the answer.
   Use browser_wait for a specific element or phrase you expect, then read again.
   Never report placeholders as the result. "It's showing empty" means you looked
   too early, not that the data is missing.

2. LOOK BEFORE YOU CONCLUDE IT'S MISSING.
   Scroll to the very bottom — lists lazy-load more rows as you scroll, so what
   you want may not have rendered yet. Then look for pagination: a "next" arrow,
   numbered pages, a "show more" or "load more" button, tabs, or a filter. The
   item you're after is very often on page two or under another tab, not absent.
   Only after scrolling fully and checking every page/tab should you say
   something isn't there.

3. CLICK LIKE A PERSON, THEN VERIFY.
   App buttons often ignore a scripted click. If browser_click does nothing
   (the page didn't change), use browser_click_real — genuine mouse events that
   the app's handlers actually receive. After ANY click, wait for the content to
   settle and read the page again to confirm it did what you expected. A click
   you didn't verify is a click you don't know happened.

4. WHEN A CONTROL RESISTS, CHANGE APPROACH — DON'T HAMMER.
   If a button won't respond after a real click and a wait, it may be off-screen,
   covered by an overlay, or not yet interactive. Scroll it into view, wait,
   retry once. If it still resists, the approach is wrong: try a different
   selector, dismiss the overlay, navigate directly to the target URL, or reach
   the same goal another way. Clicking the same dead element ten times is how a
   turn's whole budget gets burned for nothing.

You never need permission to open, read, scroll, or click. Just work the page.`,
	},

	"signin": {
		Name:    "signin",
		Summary: "signing into the user's accounts with their saved Chrome credentials",
		Body: `SIGNING IN

You act as the user on their own accounts. Two facts make this easy:

- The 'auth' browser context IS their Chrome — their cookies, their live
  sessions. Open account pages there and you are usually already signed in;
  just proceed. Never sit in the guest context and then say you can't sign in.

- When a login form does appear, you do not type the password — Chrome does.
  The move: check browser_accounts for the site first. If there is exactly one
  saved login, use it; if there is more than one, ASK the user which account,
  then use that one. Click the sign-in field with a real click (Chrome only
  offers saved passwords on a genuine gesture), and its saved-logins dropdown
  appears. Press Down then Enter to choose one — Chrome fills both username and
  password. Then submit and wait for the signed-in page.

The only hard line: never type a raw password, card number, or other secret into
a field yourself. Let Chrome's saved credentials do it. Everything else — using
their session, choosing a login, submitting — is ordinary work.`,
	},

	"documents": {
		Name:    "documents",
		Summary: "producing real docx / xlsx / pdf files with proper structure and correct data",
		Body: `PRODUCING DOCUMENTS

When the user wants a document — a report, a spreadsheet, a filled PDF — produce
the actual file, not a description of one, and verify it after.

- Choose the format they asked for. "Word document" / "report to send" -> .docx.
  "Spreadsheet" / data with formulas -> .xlsx. A form to fill -> .pdf. A quick
  write-up they'll read now, with no format named -> a .md or .txt file.

- Structure it like the real thing. A report gets a title, sections, and a table
  where the data belongs — not one wall of text. A spreadsheet gets headers and
  the actual rows, and any total or average you were asked for must be COMPUTED,
  not left as a label. Embed an image by writing ![caption](/path) on its own
  line. Headers, footers, and page numbers are available when it's a formal
  deliverable.

- Pull real content from the sources. If you're combining several files, open
  every one and carry facts from each into the output — a summary that dropped
  two of the three inputs is wrong.

- Verify. After writing the file, open it back and confirm the content and the
  numbers are actually in it. Then tell the user where it is, in one line.`,
	},

	"research": {
		Name:    "research",
		Summary: "web research that ends in a real answer or artifact, not a link dump",
		Body: `RESEARCH

Research is not searching — it is arriving at an answer and doing something with
it. Scale the effort to the question: one search for a single fact, several for a
comparison or a moving topic.

- Search with short queries (1-4 words), broaden then narrow. Don't repeat a
  near-identical query; it returns the same results. Prefer original sources
  (company posts, papers, official sites) over aggregators.

- A snippet is a lead, not the answer. When a result matters, fetch the full
  page to read it properly before relying on it.

- End in something concrete. If the user wanted a file, write it. If they wanted
  an answer, give the answer — synthesised in your own words, not a list of
  links — and act on it if the task was to act. Don't hand back "here are some
  pages"; that's the raw material, not the work.`,
	},

	"delegation": {
		Name:    "delegation",
		Summary: "when to hand heavy engineering to Claude instead of doing it yourself",
		Body: `DELEGATING TO CLAUDE

You have another, stronger engineer available. Use it for what it's for, and not
for what it isn't.

- Hand off genuine software work: writing or refactoring a real codebase,
  debugging across many files, anything that needs a coding agent with its own
  filesystem and many steps. Prefer resuming an existing Claude session over
  starting fresh — continuity keeps the context and memory.

- Do NOT delegate what you can already do with your own tools: reading a page,
  writing a document, a shell command or two, a search. Delegation has overhead;
  spending it on a task you could finish yourself just makes you slower.

- When you do delegate, give a precise brief the first time — the goal, the
  constraints, where the files are — rather than launching and re-explaining.`,
	},
}

// Get returns a skill by name.
func Get(name string) (Skill, bool) {
	s, ok := skills[strings.ToLower(strings.TrimSpace(name))]
	return s, ok
}

// Names lists the skill names, sorted.
func Names() []string {
	out := make([]string, 0, len(skills))
	for n := range skills {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Index renders the one-line summaries, so she knows what know-how exists and
// when each applies without reading every body.
func Index() string {
	var b strings.Builder
	for _, n := range Names() {
		fmt.Fprintf(&b, "  %-12s %s\n", n, skills[n].Summary)
	}
	return strings.TrimRight(b.String(), "\n")
}
