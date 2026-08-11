# Freya

**An AI assistant that counts what it just wrote, and hands the number back.**

Written in Go, with zero external dependencies. One binary, builds in seconds.
47,000 lines of standard library, 17,000 lines of tests. Developed on a 2014
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

**Drives a real browser.** 42 of her 136 tools are Chrome, over the DevTools
Protocol: click, type, drag, right-click, upload, download, switch tabs, read
pages that lazy-load, work pagination, save a page as PDF. Clicks go through the
browser's own input pipeline rather than `element.click()`, because the pages
where that distinction matters are the pages worth automating. It is a real
browser on a real display and not a headless one, for the same reason: a window
you can watch, on the machine you are sitting at.

**Finishes what she starts.** Writes files, checks the syntax, checks that every
link goes somewhere, serves it, and puts it on your screen. `site_check` resolves
every link, anchor, local file and external image; the external ones matter,
because one site passed every local check and rendered with two blank tiles after
two of its six image URLs turned out to be invented. When she tries to end a turn
with a page she wrote still leading nowhere, the loop hands it back to her.

**Makes documents that look made.** DOCX, multi-sheet XLSX with charts, and PDFs
rendered from her own HTML and CSS through Chrome, so gradients, web fonts and
real layout survive into print.

**Remembers.** Facts, episodes and a searchable archive of every conversation,
assembled into each prompt by a tiered budget rather than by sending everything.

**Runs things.** Persistent terminal sessions, local dev servers she keeps track
of, git, project search.

**Works while you are not there.** A daemon with background jobs, watchers,
scheduled self-tasks, and a self-repair loop that files a defect report when a
task goes badly and runs an engineer against its own source to find out why.

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

The difference is what kind of decision it is. Emoji, and the shape of a section,
are decided once and a rule can reach them. An `href` is written mid-markup,
one attribute at a time, a hundred times a page, and no instruction operates at
that level. The rule that came out of it, and that now governs this codebase:
**if something survives being named twice, stop writing rules about it and count
it.**

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

### The gate, and how often it fires

Nothing here asks her whether she is done. It looks, because asking does not
work: [recent work](https://arxiv.org/abs/2606.09863) measures agents asserting
completion against an environment that says otherwise in **75.8%** of
self-assessing coding trajectories, and measures LLM judges detecting it at AUROC
0.65 and 0.54, close enough to chance to be useless, because judges key on
confident closing language and on how much the agent did. Lightweight detectors
over the trajectory reach 0.83 to 0.95, so the signal is in the record of what
happened and asking a model for a verdict on it is what loses it.

So when she tries to end a turn, four things are checked against the record
rather than against her account of it:

- A page she wrote this turn that still leads nowhere, or a step she wrote down
  and never settled. The verdict comes from re-reading the file on disk, never
  from the trail, because she may have fixed it with any tool in any order.
- A site built this turn that nobody looked at, where `review` is available.
- A long answer built on search results with no page ever opened.
- Sources cited in the answer that were never fetched, which is a different set
  from the pages she saw listed.

One push per exchange, then it lets go. A gate that will not take an answer is a
hang.

**How often it has fired: once, in ten measured runs.** Site builds of one, two,
two and four pages, a research task with citations, documents, a data question
and a destructive request, all against a live model. Nine times something earlier
got there first: the house-style count caught a tell at the moment of the write
and it was fixed a round later, `site_check` caught a form posting to `#` on a
page that had passed everything else, the playbook got `review` called with
no gate involved, and on the research task every URL in the answer had actually
been fetched, so the citation check had nothing to say.

The tenth is worth the whole section. `review` could not start a browser, so it
failed. She tried three other ways to get a page rendered, could not, and went to
answer anyway — and the gate stopped her, said nobody had looked at this, and she
went back and rewrote both pages and re-checked them.

That run only happened because a bug was fixed a few hours earlier. `review` used
to return success when it had rendered nothing, so the gate that exists to force
it was satisfied by a call in which nobody looked, and the turn ended clean. Both
halves of that are the same lesson: the gate is only ever as honest as the tool
it reads, and a tool reporting success for work it did not do defeats every check
built on top of it.

That is the result, and it is not the one this section originally claimed. The
gate exists because of a bike-shop build where the note fired, was read, and lost
anyway: she wrote two more files, ran `code_check` three times, served the site,
put it on the screen and called it done with the dead link still in it. It is
worth keeping for that. But on the evidence so far it is a backstop and not the
mechanism, and the mechanism is the rung below it.

### Memory is a budget, not a bucket

A million-token window is not a reason to send everything. Tiers are ordered
most-stable-first, because the prompt cache rewards a stable prefix:

```
[identity] [facts] [episodes] [working history] [retrieved] [current turn]
 static     rare     append      append-only      volatile    volatile
```

Eviction is chunked rather than sliding, so the cached prefix survives many turns
instead of being invalidated every request. Budgets cascade: unspent allowance
falls through to the tier below, so short conversations send short prompts.

On my own telemetry the hit rate has run around 79% over 196M input tokens. That
comes from a data directory that is not in this repo, so take it as my number
rather than one you can reproduce by cloning. `usage_report` computes the same
figure from whatever you accumulate, out of the provider's own accounting rather
than an estimate.

The archive is append-only and never destroys anything. Turns degrade verbatim →
summarised episode → still searchable by BM25.

### A background job is an isolated conversation

Interleaving two threads of work into one append-only archive corrupts the
transcript and the cached prefix at the same time. So a job runs against a
branch: identity and facts read live, the conversation frozen at spawn, its own
turns accumulating separately, one summary turn rejoining when it ends.

Two things must never vary per job, and a test pins each: **tool declarations**
and the **persona prefix**. Both lead the request, so a per-worker difference
gives every thread its own cacheable prefix and none can reuse another's.

### Nothing runs without passing the guard

Every action is classified and either allowed, confirmed with a concrete preview,
or refused. Deleting data, touching system paths and escalating to root are
refused outright when there is nobody to ask.

Skills that shell out invoke the binary directly with a timeout and never through
a shell, so arguments cannot inject commands. `run_shell` is the one deliberate
exception, for the cases that genuinely need pipes or redirection, and the guard
classifies it as higher risk for exactly that reason: a bare shell string is
medium, and any chaining metacharacter in a string that can modify state raises
it to high.

Credential fields report their length and never their contents. `browser_inspect`
once labelled an input with its own value, which put a real password into the
archive and into every subsequent request.

The microphone is off unless you turn it on. There is no local wake-word model,
so always-on listening means the room is recorded and uploaded, and that is not
something to arrive at by leaving a variable unset.

### Two behaviours that took measurement to get right

**Speech recognition goes to the cloud, not to local Whisper.** On this hardware
cloud STT is both faster and more accurate. Audio is encoded to Ogg during
capture because upload dominates latency: 190KB of WAV takes 8.3s, 31KB of Ogg
takes 1.8s, same transcript.

**Speaker verification is implemented and unvalidated, and says so.** On real
audio the nearest impostor scored 0.022 below the owner, so no threshold
separates them. That measurement is written up in `internal/voice/verify.go` with
the four voices it was taken on; it has no fixtures in the repo, so it is my
number and not a reproducible one. The default policy warns and never enforces.

Two fixes that mattered are in the code: cepstral coefficient c0 is excluded
because it encodes loudness rather than identity, and embeddings are centred
before normalising, which turns cosine into correlation. On synthetic tones the
test suite measures the owner/impostor gap every run, and it currently sits at
about 0.72 against a floor of 0.15. Synthetic tones are an easy case and that
number says nothing about real voices.

## Layout

```
cmd/freya         REPL, slash commands, voice daemon
internal/agent    the think-act loop, persona, the checks that refuse an answer
internal/work     background jobs: bounded pool, cancellation, isolation
internal/memory   tiered memory, context assembly, BM25 retrieval
internal/skills   the tool registry and every capability
internal/browser  CDP client, gestures, page state, event stream
internal/wiring   whether the thing she built is actually joined up
internal/playbook know-how, as distinct from tools
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

`go test ./...` runs 670 tests. On a bare machine 648 pass, 22 skip and none
fail; the skips are the ones that need Chrome running, or the LibreOffice-produced
documents that `FREYA_PROBE_DOCS` points at. The race detector is clean.

## What it needs

Linux, and it has only ever been built and run there. Nothing in it is knowingly
platform-specific beyond the terminal and process handling, but macOS and Windows
are untested and `internal/term` will not compile on Windows as it stands.

The browser tools drive a real Chrome window on a real display, which is the
design rather than an omission: clicks go through the browser's own input
pipeline and the window is one you can watch. On a headless server they will not
start. Everything else works without a display.

Developed on Linux Lite 8, i7-4600U, no GPU. It is meant to be comfortable there.

## License

MIT. See [LICENSE](LICENSE).
