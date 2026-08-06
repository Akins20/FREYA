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
	"apps": {
		Name:    "apps",
		Summary: "web applications: files, menus, selection, uploads, downloads, dialogs",
		Body: `WORKING A WEB APPLICATION

Drive, a file manager, a mail client, a project board. These are programs that
happen to be drawn in a page, and most of what they can do is not a button
sitting in the open. Reading the page and clicking links is enough for a
document; it is not enough for an application.

1. WHEN YOU CAN SEE A THING BUT NOT A BUTTON FOR WHAT YOU WANT — RIGHT-CLICK IT.
   Download, Rename, Copy link, Remove, Share, Open in new tab: in most
   applications these live ONLY in the context menu. browser_right_click on the
   file name, then READ THE PAGE — apps like Drive draw their menu into the page
   and you then click an item by its text.
   If the page looks unchanged afterwards, the browser drew its own menu
   instead. That one is not page content and nothing can click it, so press
   escape and take another route. Do not right-click again.

2. ONE CLICK SELECTS. TWO CLICKS OPEN.
   In any list, grid or tree, a single click usually only highlights the row —
   which is why one click so often looks like it did nothing. browser_double_click
   is how you open a folder or a file.

3. SEVERAL THINGS AT ONCE: CLICK THE FIRST, THEN browser_select_also.
   "Download the two pictures" is one action on two files, not two actions.
   Click the first normally, browser_select_also for each of the others
   (extend=true for a whole range), and the application will then offer a
   command that applies to all of them. Many apps offer bulk commands ONLY when
   more than one thing is selected.

4. UPLOADS DO NOT GO THROUGH THE PRETTY BUTTON.
   Clicking Attach or Upload opens the operating system's file chooser, which is
   not part of the page and which nothing here can drive — it will simply sit
   there. Use browser_upload against the input[type=file] itself. It is usually
   hidden behind the styled button; browser_inspect will show it, and its being
   invisible does not matter because this never clicks it.

5. DOWNLOADS ARRIVE ON DISK, NOT IN THE PAGE.
   A download changes nothing about the page, so a successful one looks exactly
   like a click that failed. Do not click again — clicking again is how you end
   up with four copies, or four dialogs. The tool result tells you when
   a download started and finished, and browser_downloads lists what has landed.
   Trust those, not the page.

6. THINGS THAT SCROLL INSIDE THEMSELVES.
   Chat threads, file grids, long dialogs and code viewers keep their own
   scrollbar. Scrolling the page does nothing to them, and you will read the
   same rows and conclude that is everything there is. browser_scroll_within on
   the panel.

7. DRAGGING IS A GESTURE, NOT TWO CLICKS.
   Moving a file into a folder, reordering, dropping onto an upload zone: use
   browser_drag. Clicking the source and then the target does not do it, because
   what these listen for is the movement in between.

8. A DIALOG THE PAGE OPENS IS HANDLED FOR YOU, AND YOU ARE TOLD.
   An alert or confirm freezes the page until it is answered; that now happens
   automatically and the tool result says what it said. If a result mentions a
   dialog, that is what just happened — treat it as the outcome of your action,
   not as noise.

9. SIGNING IN, WHEN THERE ARE SEVERAL SAVED ACCOUNTS.
   You cannot choose between saved passwords: the chooser is browser UI and the
   password is never readable. If Chrome fills the wrong account, do not retype
   anything — use browser_sync_logins to copy the user's live session across
   (ask them to close Chrome first), or ask them to sign in once themselves.

10. KEYS THE APPLICATION BINDS.
   browser_press knows f1-f12 and "menu" (the context-menu key) as well as
   enter/tab/escape/arrows, and modifiers combine with +: shift+f10 opens a
   context menu from the keyboard, ctrl+a selects everything. When an
   application has a shortcut, it is often more reliable than hunting for the
   control.`,
	},

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

3. CLICK BY WHAT IT SAYS. SELECTORS COME FROM SCOUTING, NEVER FROM YOUR HEAD.
   browser_click_text is the default and it is a REAL, trusted click — the same
   gesture a person makes — so it works even on portal links that ignore scripted
   clicks. Click the thing by its visible text: browser_click_text "Self-Quiz
   Unit 5". You almost never need anything else.
   If click_text can't find the wording, the answer is to SCOUT, not to guess a
   selector. browser_inspect lists the real controls with their real labels and
   ids; browser_read shows the visible text. Use one, see what is actually there,
   then click the real label by text. A CSS selector is legitimate ONLY if you
   copied it, character for character, from an inspect you just ran — never one
   you composed, remembered, or inferred from a pattern. A made-up selector always
   misses, because app pages mint fresh ids every visit; and each miss just tempts
   the next guess. If a click fails, the tool now shows you what IS clickable —
   click one of those by text; do not answer a failed selector with another
   selector.
   After ANY click, wait for the content to settle and read again to confirm it
   did what you expected. A click you didn't verify is a click you don't know
   happened — and if the page didn't change, look before acting again.

4. WHEN A CONTROL RESISTS, CHANGE APPROACH — DON'T HAMMER.
   If a button won't respond after a real click and a wait, it may be off-screen,
   covered by an overlay, or not yet interactive. Scroll it into view, wait,
   retry once. If it still resists, the approach is wrong: try a different
   selector, dismiss the overlay, navigate directly to the target URL, or reach
   the same goal another way. Clicking the same dead element ten times is how a
   turn's whole budget gets burned for nothing.

5. WHAT YOU WANT IS OFTEN HIDDEN — REVEAL IT.
   A read shows only what is VISIBLE, so if you can't find something, it may be
   there but not shown yet. Work through the ways content hides:
   - Below the fold or infinite scroll: scroll down (browser_scroll to 'bottom'),
     then read again; more rows and sections render as you go.
   - Behind a button: "Show", "Show more", "Details", "View" — click it, wait,
     read. The content was display:none until you did.
   - In a modal or dialog: an "Open"/"View" button opens an overlay; its content
     is only readable once open. Read it, then close it if it blocks the rest.
   - Under another tab: content lives under a tab that isn't selected. Click the
     tab, wait, read.
   - Behind a search or filter: type the relevant word into the search box
     (browser_type), which filters the list so the matching item appears.
   - In a collapsed panel or accordion: click the header/expander to open it,
     then read.
   - In an embedded frame: a quiz, editor or form is often served inside an
     iframe. Reads AND actions now reach into same-origin frames, so click,
     click_text and fill work on that content just like the rest of the page —
     select an answer with browser_click_text, submit with it too. You do not
     need to do anything special for a frame; treat what you read as clickable.
   Reads and clicks already see through web components (shadow DOM) and same-origin
   iframes — portals like the university's are built entirely from them — so a
   task list or quiz that looks "empty" is a wait problem (point 1), not a hidden-
   content problem. If it's genuinely not shown, it's one of the reveals above.
   The rule: if it isn't visible, ask which of these is hiding it, do the reveal,
   wait, and read again. Concluding "it's not there" before trying the reveals is
   the mistake.

You never need permission to open, read, scroll, click, or reveal. Work the page.`,
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

	"shell": {
		Name:    "shell",
		Summary: "running commands — which of the three ways to reach for, pipes/redirection, long-running and interactive",
		Body: `RUNNING COMMANDS

You have three ways to run things, and picking the wrong one is why a command
"succeeds" and yet nothing happened. Choose deliberately:

1. run_command — a single program, run directly, no shell.
   Use it for one clean invocation: ls, git status, go build, cp a b. It is the
   default and the safest. But it is NOT a shell: a > or | or * you put in the
   arguments is a literal string, not redirection, a pipe, or a glob. "echo hi >
   out.txt" through run_command writes the characters "> out.txt" as arguments and
   creates no file. This is the single most common mistake — if your command has
   a >, >>, |, *, &&, or a $VAR, run_command is the wrong tool.

2. run_shell — a real shell line, for when you genuinely need shell features.
   Redirection (echo x > out.txt), pipes (cat f | grep x), globs (rm *.tmp),
   chaining (make && ./run). This is how you send output to a file or feed one
   command into another. Reach for it the moment you need any of those; do not
   try to fake them with run_command.

3. terminal_open / terminal_run / terminal_read / terminal_send — a PERSISTENT
   session that survives between commands.
   Use it when state must carry over (a cd, an exported variable, a running
   server), for an interactive program that prompts (python, a REPL, an installer
   that asks a question — answer with terminal_send), or for long-running work you
   want to check on. terminal_read collects what a session has produced since you
   last looked.

DON'T OVER-BUILD A ONE-OFF. If the task is just to produce some output, one
run_shell line usually does it — "seq 1 5 > out.txt", "sort names.txt | uniq".
Writing a script file, chmod-ing it, and running it is only worth it when the
task actually asks for a reusable script; otherwise it is three ways to
introduce a bug for no gain.

WRITE SCRIPTS THAT ACTUALLY RUN. A shell script needs real newlines between its
lines and the right separators — a for-loop is "for i in a b c; do echo $i;
done", with the semicolon before done, not "do echo $i done". Cramming it onto
one line or dropping a separator gives a syntax error that produces no output.

ALWAYS VERIFY THE RESULT, NOT THE EXIT. A command returning no error is not proof
it did what you meant. If it was supposed to write a file, read the file back and
check it has the right content. An EMPTY output file is the classic tell that the
script or command was broken — do not report success on it; the script errored,
so fix the script and run it again. "Ran without error" and "did the job" are
different claims, and the second is the one that matters.`,
	},

	"files": {
		Name:    "files",
		Summary: "reading, editing and finding files without losing content or clobbering the rest",
		Body: `WORKING WITH FILES

- READING A LARGE FILE. file_read returns only the first stretch of a big file
  and clips the rest — so on a long file you have NOT seen all of it. Do not
  answer as if you had. Read the part you need, or use run_shell with grep, head,
  tail, or sed to find and pull the exact lines. Never invent content you did not
  actually read; that is the file equivalent of a hallucination.

- CHANGING AN EXISTING FILE. Use file_edit, which replaces one exact string and
  leaves everything else untouched. Do NOT rewrite the whole file with file_write
  from memory — that quietly drops or alters the parts you did not focus on. Put
  enough surrounding text in old_text to make the match unique. file_write is for
  a NEW file or a deliberate full replacement, not for a tweak.

- FINDING SOMETHING. Don't guess a path and don't re-scan the whole disk every
  time. folder_list explores a directory; run_shell with find (by name) or grep
  -r (by content) searches a tree. When the user names a place they've referred
  to before — "my assignment folder" — resolve it from what you know rather than
  hunting from root.

- PATHS WITH SPACES. Paths on this machine contain spaces. Pass a path as one
  whole value; when it goes through a shell, quote it. A path split on spaces is
  a path that points nowhere.

- AFTER WRITING. Confirm the file is where you meant and holds what you intended,
  especially for anything generated (a document, a converted file). Deleting is
  the one file action that is hard to undo — treat it with the care that earns.`,
	},

	"assessments": {
		Name:    "assessments",
		Summary: "quizzes, tests and forms — finishing every question, reading confirmations, not guessing, before you submit",
		Body: `ATTEMPTING A QUIZ, TEST OR FORM

Submitting is usually irreversible, and a half-finished submission is worse than
none — it can spend an attempt or lock in a low score. Work an assessment like
someone who intends to get it right, not someone racing to click Submit.

1. FINISH EVERY QUESTION BEFORE YOU SUBMIT.
   First find out how many questions there are — read the whole attempt, scroll
   through it, note the count. Answer each one. Before any final Submit/Finish,
   check that NONE are still blank. An unanswered question is unfinished work you
   are about to make permanent. If you notice you skipped two, the thought is not
   "submit anyway" — it is "I missed two, let me go back and finish them first."

2. AFTER YOU CLICK SUBMIT, LOOK — THEN DECIDE FROM WHAT YOU SEE.
   Clicking a Submit/Finish control does not reliably submit on its own, and sites
   differ in what happens next — you cannot assume. It might jump straight to a
   result page; it might raise a confirmation or a warning first; it might reveal
   one in place so the page barely appears to move; it might do nothing because
   something is required. So don't follow a fixed script — read the page after the
   click and respond to what is actually there:
   - a confirmation/dialog asking you to proceed → read it; if you agree with what
     it says, click ITS OWN button by its visible text to finish;
   - a warning that you're not done (unanswered questions, an empty field) → go
     back and fix it before committing, don't push past it;
   - the result or receipt → verify it really went through, then report it;
   - nothing visibly changed → the click may have opened something in place or
     failed; READ to find out — do not re-click the same thing or guess a button id.
   The point is not to memorise a flow. It is: act, observe, decide — every time,
   on whatever this particular page actually does.

3. IF YOU DON'T KNOW AN ANSWER, FIND IT — DON'T GUESS.
   Not sure of an answer? The move is not to pick one to keep moving. Look it up:
   research the concept with a web search, reason it through, then answer. A
   question you could have gotten right and guessed wrong is a mark you threw away.
   (For an ungraded self-quiz this is how you learn it; for anything graded or a
   real exam, if you genuinely cannot determine the answer, say so to the user
   rather than guessing on their record.)

4. VERIFY EACH ANSWER LANDED.
   After selecting an option, confirm it shows as selected before moving on — a
   click that didn't register is a silent blank. After submitting, read the result
   page and confirm it actually went through and what the outcome was; report that,
   not "it should be submitted".

5. KNOW WHAT YOU'RE SUBMITTING.
   A practice/self-quiz is safe to attempt and submit. A graded quiz, a final, or
   anything with limited attempts deserves more care — if you're unsure whether
   it's graded or final, the page usually says (attempts, due date, weight); check,
   and when it matters and you're still unsure, ask before committing.`,
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
