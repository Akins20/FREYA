# Freya

**An AI assistant that counts what it just wrote, and hands the number back.**

Written in Go, with zero external dependencies. One binary, builds in seconds.
52,000 lines of standard library, 21,000 lines of tests. Developed on a 2014
laptop with no GPU and meant to be comfortable there.

The reasoning runs on a hosted model (Gemini or Anthropic). There is an offline
mode, but the stand-in it uses is a keyword matcher for exercising the loop, not
a local LLM.

```
❯ build a four page site for my plant nursery

  → file_write   index.html · catalog.html · care.html · contact.html
  → site_check   contact.html: form action="#" submits to nowhere
  → file_edit    contact.html
  → site_check   4 pages, nothing leads nowhere
  → review       three weakest things, from someone who has never seen her work
  → file_write   ×4  rewritten against the review
  → site_check   still clean

24 rounds. Nobody asked her to check the form or have the thing looked at.
```

Those are the calls from a real run, in the order they happened, with the
reasoning between them cut for length. `-v` prints all of it.

A dead link is the whole idea in one line. Told not to leave them, she left
fewer. Told twice, more firmly, she left fewer still and never none. Counting
them at the moment of the write and handing the number back took it to zero, and
kept it there. Most of this README is about why those two things are different.

## Start her

```bash
cp .env.example .env     # add your keys
make run
```

No keys? She still runs:

```bash
make offline             # rule-based stand-in model, no network
```

Speak to her instead of typing: `/voice on`, or `freya -daemon` for the
always-there version with a push-to-talk key.

## What she does

147 tools offline, 152 with a provider that can see and a microphone attached.
`find_tools` finds the rest when a request needs something she was not offered.

**Drives a real browser.** 43 of those tools are Chrome, over the DevTools
Protocol: click, type, drag, right-click, upload, download, switch tabs, read
pages that lazy-load, work pagination, save a page as PDF. Clicks go through the
browser's own input pipeline rather than `element.click()`, because the pages
where that distinction matters are the pages worth automating. It is a real
browser on a real display and not a headless one, for the same reason: a window
you can watch, on the machine you are sitting at. She can also read the
browser's own history, bookmarks and saved usernames, which is how she answers
"that site I was on last week" without being told the address.

Chrome profiles are a first-class thing rather than an assumption. A machine with
a work profile and a personal one has two different answers to "my account", and
picking wrong does not produce a worse answer, it produces a confident one about
somebody else's inbox. `browser_profiles` lists them by name and signed-in
account, the history search and the login sync both take one, and a name that
fits two profiles is refused rather than resolved by guessing.

**Reads and drives native applications.** Ten tools for native windows. Six work
at the display: list windows, focus one, move or resize it, send keys, type at
whatever has focus, screenshot. Windows are arranged by name rather than by pixel,
so "docs on the left, editor on the right" is two calls, and the answer says where
the window actually ended up, because a tiling manager overrides placement and a
maximised window will not move until it is restored.

The other four ask the application itself, over AT-SPI, and that is the half that
does not work from a photograph. `desktop_inspect` reads what a window says it
contains: buttons, fields, labels, menus, by name. `desktop_type_into` fills a
named field and reports what the field holds afterwards rather than what was sent.
`desktop_menu` walks a menu path by name, opening each level and reading it again,
because a menu is not the same object before and after it opens. `desktop_click`
presses a control through the application's own handler where the toolkit
publishes one, which needs no coordinates and therefore works on a control that is
scrolled out of view, minimised, or in a window that is not in front; it falls
back to the pointer only when there is no handler to call. What each toolkit will
and will not tell her is measured, and is its own section below.

**Takes part in copy and paste.** The system clipboard, both directions, which
is how a workstation actually moves data between applications. Reading it is
deliberately not automatic and its contents arrive fenced as untrusted, because
whatever the user last copied is sometimes a password and sometimes a web page
that says "ignore your instructions".

**Knows where your things are, and works it out rather than being told.** There
is no Gmail tool here, and there will not be one. `service_find` reads the user's
own browsing history and ranks the sites they actually go to, so "which email do
I use" is answered from evidence about this person rather than from a default.
Once confirmed, `service_learn` remembers it, along with the places inside it
that took several steps to find: where compose is, where today's agenda is.
`service_open` then goes there in one call, checks the page it landed on is
really that service, and records the answer itself rather than asking her to
report it. Every answer carries how old the
knowledge is, two failures in a row mark a route stale rather than trusted, and a
route is an address and never a credential. Anything can be learned by name, so a
self-hosted mail server or a university portal is a first-class route rather than
an unsupported case.

**Finishes what she starts.** Writes files, checks the syntax, checks that every
link goes somewhere, serves it, and puts it on your screen. `site_check`
resolves every link, anchor, local file and external image; the external ones
matter, because one site passed every local check and rendered with two blank
tiles after two of its six image URLs turned out to be invented.

**Makes documents that look made.** DOCX, multi-sheet XLSX with charts, slide
decks, and PDFs rendered from her own HTML and CSS through Chrome, so gradients,
web fonts and real layout survive into print.

**Reads the documents you already have.** docx, xlsx, pptx, odt and ods are ZIP
containers full of XML, so `archive/zip` and `encoding/xml` read them directly:
no LibreOffice, no temp files, no process start per document. PDF is the one
exception and shells out to pdftotext.

**Remembers.** Facts, episodes and a searchable archive of every conversation,
assembled into each prompt by a tiered budget rather than by sending everything.

**Runs things.** Persistent terminal sessions that outlive the turn that started
them, local dev servers she keeps track of, git, project search.

**Works while you are not there.** A daemon owns the watchers and the
notifications; a session owns the conversation and every write to memory, so two
processes never corrupt the same files. Watchers score what they notice on
consequence, actionability and novelty, and stay quiet below the bar, because an
assistant that reports everything gets muted within a day. She can schedule a
task for her own future self and the daemon runs it when it comes due, which is
what makes "I'll check back on that shortly" a promise she can structurally
keep.

**Delegates.** Claude Code is available as a subordinate for sustained work over
a codebase, with sessions resumed rather than restarted, and a budget ceiling
because an ambiguous instruction given to an agentic tool can loop.

**Learns how, not just what.** Playbooks are know-how as distinct from tools:
"modern portals lazy-load, so wait for real content and work the pagination
before deciding something is missing" is a playbook, and clicking a selector is
a tool. The built-in ones are embedded strings. She can also write new ones from
routes she works out the hard way, and a consolidation pass notices when she has
learned the same thing twice.

**Reports on herself.** Token counts come from the provider and are exact; money
is computed from a rate table and is labelled an estimate, because a figure like
"$0.0231" invites a trust it has not earned. There is a benchmark suite that
drives the real binary as a subprocess in a throwaway workspace and checks the
world it left behind rather than the words it left in the terminal.

## The parts worth reading

### Count it, do not instruct it

Every rule in her design playbook was written after measuring her own output.
Some rules worked and some did nothing, and the pattern took a while to see.

These are counts off my own builds, kept as I went. They are not in the repo and
nobody can re-run them, so weigh them as a log rather than as a result. What is
in the repo is the mechanism they produced, in `wiring.HouseStyle`, and you can
watch it fire on the first page you ask her to write.

| tell | before | named | named harder | counted |
|---|---|---|---|---|
| emoji | 26 / 6 / 3 | 0 | 0 / 0 | n/a |
| cards per page | 6 / 6 / 9 | 0 | 3 / 3 / 4 / **11** | not yet measured |
| dead links | 5 of 15 | 2 of 13 | 1 of 16 | **0 of 52, 0 of 62** |

Emoji is the easy case. One line in a playbook, gone, stayed gone.

Read the dead links row across. Naming the rule helped and never finished the
job: five of fifteen, then two of thirteen, then one of sixteen, with the rule
getting firmer each time. One dead link is not better than five, because the
person clicking it does not know they got the good build. Counting them at the
moment of the write took it to none of fifty-two, and then none of sixty-two.

Cards are the same story from the other side. Named once, they went to zero.
Then they crept back, and the worst page of any build was the one rewritten to
act on a review asking for more visual variety, because "vary the layout" gets
implemented as more boxes. They are counted now and there has not yet been a
build to measure it on, which is why that cell says so rather than guessing.

The difference is what kind of decision it is. Emoji, and the shape of a
section, are decided once and a rule can reach them. An `href` is written
mid-markup, one attribute at a time, a hundred times a page, and no instruction
operates at that level. The rule that came out of it, and that now governs this
codebase: **if something survives being named twice, stop writing rules about it
and count it.**

The counting never rewrites anything for her. `wiring.HouseStyle` reports the
number and stops. Silently fixing her prose would improve the page and teach her
nothing, and the point is the next page.

### Three rungs, in order

The same shape appeared often enough to be a rule rather than a discovery:

1. **Put it in a playbook.** Reaches structural decisions and nothing else.
2. **Attach it to a call she already makes.** Where nearly all of the work
   happens. The design rules ride on `project_new`; the wiring report and the
   house-style count ride on `file_write`.
3. **Refuse to finish without it.** The backstop, for when rung two is read and
   then outrun.

Rung two is the one to copy. Rung three is the one that sounds impressive, and
the section below is honest about how often it has actually been needed.

### Five checks before she says it is done

Nothing here asks her whether she is done. It looks, because asking does not
work: [recent work](https://arxiv.org/abs/2606.09863) measures agents asserting
completion against an environment that says otherwise in **75.8%** of
self-assessing coding trajectories, and measures LLM judges detecting it at
AUROC 0.65 and 0.54, close enough to chance to be useless, because judges key on
confident closing language and on how much the agent did. Lightweight detectors
over the trajectory reach 0.83 to 0.95, so the signal is in the record of what
happened, and asking a model for a verdict on it is what loses it.

Each of the five was written after a specific measured failure, and each reads
the record rather than her account of it.

**Unfinished work.** A page she wrote this turn that still leads nowhere, a step
she wrote down and never settled, or a site nobody looked at where `review` is
available. The verdict comes from re-reading the file on disk, never from the
trail, because she may have fixed it with any tool in any order.

**Saying it worked when it did not.** One exchange, fourteen tool calls, every
one failed or refused, nothing clicked and nothing submitted, and the answer
began "Self-Quiz Unit 5 is submitted".

**Claiming all of something.** Asked to audit every project in a folder, she
enumerated twenty-eight, worked through eleven, and wrote a genuinely good
report that described itself as covering all of them.

**Saying she made something, having made nothing.** Asked to redo an audit, she
made one call, opened the report she had written half an hour earlier, and said
she had updated it. The file was byte-identical.

**Running out of rounds mid-job.** When the tool-call cap stops her partway, the
salvage question used to produce "I couldn't finish". The machinery was fine;
the question was wrong. It now asks for what was actually done and what remains.

One push per exchange, then it lets go. A gate that will not take an answer is a
hang.

**How often the first one has fired: once, in ten measured runs.** Nine times
something earlier got there first: the house-style count caught a tell at the
moment of the write, `site_check` caught a form posting to `#` on a page that
had passed everything else, the playbook got `review` called with no gate
involved. The tenth is worth the whole section. `review` could not start a
browser, so it failed. She tried three other ways to get a page rendered, could
not, and went to answer anyway, and the gate stopped her, said nobody had looked
at this, and she went back and rewrote both pages and re-checked them.

That run only happened because a bug was fixed a few hours earlier. `review`
used to return success when it had rendered nothing, so the gate that exists to
force it was satisfied by a call in which nobody looked. Both halves of that are
the same lesson: a gate is only ever as honest as the tool it reads, and a tool
reporting success for work it did not do defeats every check built on top of it.

### What a native window will and will not tell you

Every desktop toolkit publishes an accessibility tree, and the useful discovery
is how little they agree about what is in it. All four of these were measured
against a live application whose own handlers wrote to a log file, so the
application is the witness rather than her account of the click.

| | tree | how a control is pressed | where a menu's items live |
|---|---|---|---|
| GTK | full | `click` | under the menu, before it opens |
| Qt | full | `Press`, and `SetFocus` on a field | behind an unnamed popup wrapper |
| Tk | none at all | nothing to press | nothing to read |
| Electron | none by default | `doDefault`, `press`, `activate`, `click`, `clickAncestor` | a separate top-level window |

Four consequences are worth stating, because each one cost a bug.

**A missing tree is a distinct answer.** A Tk window is on screen, focusable and
typeable, and publishes nothing. Reported as an empty result it reads as an
empty window, so it is named as its own outcome, with the keyboard route that
does still work.

**An action is chosen by name, never by position.** Qt lists `SetFocus`
alongside `Press`, and lists it alone on a text field. Taking whichever action
came first would focus a field, return success, and report having pressed it.

**Chromium withholds rather than lacks.** An Electron application publishes its
window frame and nothing inside it unless it was started with
`--force-renderer-accessibility`. That is VS Code, Slack, Discord and Teams. She
cannot restart your Slack, so she says which windows are withholding and what
would fix it, because "this window is empty" and "this window is not talking"
lead to completely different next moves and read identically. Chromium also
publishes no writable text interface at all, so a field there is focused and
typed into with the keyboard, and still read back afterwards.

**Choosing a menu item does not always close the menu.** On Qt the popup keeps
its pointer grab after the item fires, so the next click anywhere is spent
dismissing it and never lands, silently, because a synthetic click cannot tell
it was eaten. The menus are asked whether they are still on screen rather than
assumed closed.

`what_can_i_do_here` answers all of this for the machine in front of her, and
separately for the windows open on it right now.

### Memory is a budget, not a bucket

A million-token window is not a reason to send everything. Tiers are ordered
most-stable-first, because the prompt cache rewards a stable prefix:

```
[identity] [facts] [episodes] [working history] [retrieved] [current turn]
 static     rare     append      append-only      volatile    volatile
```

Eviction is chunked rather than sliding, so the cached prefix survives many
turns instead of being invalidated every request. Budgets cascade: unspent
allowance falls through to the tier below, so short conversations send short
prompts.

On my own telemetry the hit rate has run around 79% over 196M input tokens. That
comes from a data directory that is not in this repo, so take it as my number
rather than one you can reproduce by cloning. `usage_report` computes the same
figure from whatever you accumulate, out of the provider's own accounting rather
than an estimate.

The archive is append-only and never destroys anything. Turns degrade verbatim,
then to a summarised episode, and stay searchable by BM25 throughout.

There is also more than one way to read it back. A single retrieval pass returns
a single angle, and cannot surface "you tried this in March and it went badly"
unless March happened to use the same nouns. So several lenses each search the
archive differently, by time slice and by comparison rather than by vocabulary
alone. The obvious alternative, a crowd of background agents with different
personalities, fails for a reason worth writing down: agents differing only by a
persona label over the same model and the same memory converge, and you get
twenty rephrasings of the same three observations at twenty times the cost.

### A background job is an isolated conversation

Interleaving two threads of work into one append-only archive corrupts the
transcript and the cached prefix at the same time. So a job runs against a
branch: identity and facts read live, the conversation frozen at spawn, its own
turns accumulating separately, one summary turn rejoining when it ends.

Two things must never vary per job, and a test pins each: **tool declarations**
and the **persona prefix**. Both lead the request, so a per-worker difference
gives every thread its own cacheable prefix and none can reuse another's.

Tools that drive one global resource are the exception to the concurrency.
Calls in a round otherwise run at once, which is right for independent lookups
and wrong for the keyboard, the pointer and the focused window: asked to type a
name and then press Return, an application logging every key recorded the Return
first. Those tools declare themselves serial and run in request order.

### Nothing runs without passing the guard

Every action is classified and either allowed, confirmed with a concrete
preview, or refused. The forbidden tier is the one that matters: confirmation
dialogs are answered by tired people, and no human approval makes `dd` onto a
mounted system disk survivable. The dangerous actor in the threat model is not a
malicious user, it is a capable model acting on an ambiguous instruction, and
"clean up the old project files" is a reasonable request that becomes
catastrophic if "old" resolves to the wrong directory.

Skills that shell out invoke the binary directly with a timeout and never
through a shell, so arguments cannot inject commands. `run_shell` is the one
deliberate exception, for the cases that genuinely need pipes or redirection,
and the guard prices it accordingly.

Credential fields report their length and never their contents.
`browser_inspect` once labelled an input with its own value, which put a real
password into the archive and into every subsequent request. The desktop side
refuses password fields outright rather than handling them carefully, because
the cheapest way not to repeat that is to have no code path that can.

The microphone is off unless you turn it on. There is no local wake-word model,
so always-on listening means the room is recorded and uploaded, and that is not
something to arrive at by leaving a variable unset.

### She writes her own failures down

Every diagnosis in this codebase followed the same path: she fails, a person
reads her telemetry, finds the cause a layer beneath the symptom, and changes
the software. That person is the bottleneck, and the failures nobody happened to
look at simply persisted.

So a bad exchange writes itself down: what she was asked, what she tried, what
came back, and the trail, with no conclusions, because a report that guesses at
the cause sends whoever reads it down the guess. A bad enough one gets looked at
by an engineer with her source in front of them.

It does not deploy. The consult ends on a branch with the test results attached,
and the running daemon keeps running what it was running. Twice in one week a
change passed its tests and then broke her in production, and both were caught
by a human reading the diff.

Her report is data, not instructions. It quotes web pages, file contents and
command output, any of which may be hostile, and a page that says "ignore your
instructions" arrives here quoted verbatim inside an error message. It is fenced
on the way in, and the brief says plainly that it is content.

### Two behaviours that took measurement to get right

**Speech recognition goes to the cloud, not to local Whisper.** On this hardware
cloud STT is both faster and more accurate. Audio is encoded to Ogg during
capture because upload dominates latency: 190KB of WAV takes 8.3s, 31KB of Ogg
takes 1.8s, same transcript.

**Speaker verification is implemented and unvalidated, and says so.** On real
audio the nearest impostor scored 0.022 below the owner, so no threshold
separates them. That measurement is written up in `internal/voice/verify.go`
with the four voices it was taken on; it has no fixtures in the repo, so it is
my number and not a reproducible one. The default policy warns and never
enforces.

Two fixes that mattered are in the code: cepstral coefficient c0 is excluded
because it encodes loudness rather than identity, and embeddings are centred
before normalising, which turns cosine into correlation. On synthetic tones the
test suite measures the owner and impostor gap every run, and it currently sits
at about 0.72 against a floor of 0.15. Synthetic tones are an easy case and that
number says nothing about real voices.

## Layout

```
cmd/freya         REPL, slash commands, voice daemon, the self-repair consult
internal/agent    the think-act loop, persona, the five checks on an answer
internal/work     background jobs: bounded pool, cancellation, isolation
internal/memory   tiered memory, context assembly, BM25 retrieval
internal/reflect  several different readings of that memory
internal/skills   the tool registry and every capability
internal/browser  CDP client, gestures, page state, event stream, history
internal/a11y     the accessibility tree: what a native window says it contains
internal/platform what this machine can actually do, asked rather than assumed
internal/wiring   whether the thing she built is actually joined up
internal/playbook know-how, as distinct from tools, including what she learns
internal/routes   where the user's own services live, learned from their browsing
internal/daemon   the always-on half, and who owns which files
internal/sentinel noticing things, and deciding which are worth saying
internal/schedule a task set for her own future self
internal/defect   what she could not do, written down for an engineer
internal/claude   Claude Code as a subordinate for heavy work
internal/bench    outcome-checked benchmarks against the real binary
internal/telemetry what ran, what it cost, what failed
internal/term     persistent terminal sessions
internal/docs     reading docx, xlsx, pptx, odt, ods and pdf
internal/llm      provider-agnostic model interface
internal/voice    record, recognise, synthesise, speaker gate
internal/guard    risk classification, confirmation, audit log
```

Adding a model provider means adding one file. Nothing in `agent` or `skills`
changes.

## Configure

`.env` or the environment; real environment variables always win.

| Variable | Purpose |
|---|---|
| `GEMINI_API_KEY` | Reasoning, vision, speech. |
| `SERPER_API_KEY` | Search, news, page scraping. |
| `FREYA_PROVIDER` | `gemini` \| `anthropic` \| `mock`. Auto-detects. |
| `FREYA_MODEL` | Defaults to `gemini-3.5-flash-lite`. |
| `FREYA_DATA_DIR` | Where memory lives. Never the repo. |
| `FREYA_WORK_DIR` | The folder she works in. |
| `FREYA_WAKE` | Always-on listening. **Off unless set.** |
| `FREYA_TTS` / `FREYA_STT` | `gemini` \| `espeak` \| `piper` / `gemini` \| `whisper`. |
| `FREYA_TOOL_ROUTING` | Show only the tools a request calls for. Everything stays executable either way, and a miss is counted. |

The full table, including the reasoning window and the self-repair loop, is in
[CLAUDE.md](CLAUDE.md).

## Build

```bash
make check          # fmt, vet, test, build
make test
make install        # to ~/.local/bin
go test ./... -race
```

`go test ./...` runs 747 test functions: 726 pass, 21 skip, none fail, and the
race detector is clean. Every skip names what would run it: seven need a headless
Chrome and `FREYA_BROWSER_E2E=1`, seven need the LibreOffice-produced documents
that `FREYA_PROBE_DOCS` points at, four are live diagnostics behind
`FREYA_LIVE_DIAG`, and three want a browser profile, browser history or sox. A
barer machine skips more.

## What it needs

Linux, and it has only ever been run there in earnest. `internal/term` will not
compile on Windows as it stands, which takes the whole test suite with it, and
macOS is untested. The capability layer reports what is missing rather than
failing at the point of use, and says what would fix it.

The browser tools drive a real Chrome window on a real display, which is the
design rather than an omission: clicks go through the browser's own input
pipeline and the window is one you can watch. On a headless server they will not
start. The desktop tools need an X session and an accessibility bus; under
Wayland they drive the X11 half and say so, and under a Wayland session with no
X server at all they refuse and explain why. Everything else works without a
display.

Developed on Linux Lite 8, i7-4600U, no GPU. It is meant to be comfortable
there.

## License

MIT. See [LICENSE](LICENSE).
