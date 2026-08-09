# JARVIS

A personal AI assistant that runs on your own machine. The project is JARVIS;
the assistant is **Freya**.

Written in Go with **zero external dependencies**. The whole thing is standard
library: 47,000 lines of it, 613 tests, one static binary, builds in seconds,
offline, on a 2014 laptop with no GPU.

```
❯ build a site for my plant nursery

  → project_new       name=plant-nursery
  → file_write        index.html · catalog.html · care.html · contact.html · style.css
  → code_check        ×5
  → site_check        found a background image returning 404
  → file_edit         index.html
  → site_check        4 pages, 52 links, nothing leads nowhere
  → review            "the hero says nothing about what you sell"
  → file_write        ×4  rewritten against the review
  → serve · system_open

Four pages, on your screen. I rewrote the homepage after having it looked at
cold — the original hero could have been any garden centre in the country.
```

She was not told to check the links, notice the dead image, have it reviewed,
or go back and rewrite it. Most of this README is about why.

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

**Drives a real browser.** 43 of her 148 tools are Chrome, over the DevTools
Protocol: click, type, drag, right-click, upload, download, switch tabs, read
pages that lazy-load, work pagination, save a page as PDF. Clicks go through the
browser's own input pipeline rather than `element.click()`, because the pages
where that distinction matters are the pages worth automating.

**Finishes what she starts.** Writes files, checks the syntax, checks that every
link goes somewhere, serves it, and puts it on your screen. When she tries to end
a turn with a page she wrote still leading nowhere, the loop hands it back to her
and she goes and fixes it.

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

### She cannot claim to be finished

Every check that made a difference here is a state check, not a judgement, and
that is not an aesthetic preference. [Recent
work](https://arxiv.org/abs/2606.09863) measures agents asserting completion
against an environment that says otherwise in **75.8%** of self-assessing coding
trajectories, and measures LLM judges detecting it at AUROC 0.65 and 0.54 —
close enough to chance to be useless, because judges key on confident closing
language and on how much the agent did. Detectors that look at state reach 0.83
to 0.95.

So nothing here asks her whether she is done. It looks:

- `site_check` resolves every link, anchor, local file and external image. The
  external ones matter: one site passed every local check and rendered with two
  blank tiles, because two of its six image URLs were invented.
- The agent refuses a final answer while a page she wrote this turn is broken,
  or a step she wrote down is unsettled. One push per exchange, then it lets go.
- Sources she cites are checked against the pages she actually opened, which is
  a different set from the pages she saw in search results.
- Claims of having produced something are checked against what was produced.

### Instructions do not work, and here is the measurement

Every rule in her design playbook was written after measuring her own output.
Some rules worked and some did nothing, and the pattern took a while to see.

| tell | before | named | later builds | counted |
|---|---|---|---|---|
| emoji | 26 / 6 / 3 | 0 | 0 / 0 | — |
| cards per site | 6 / 6 / 9 | 0 | 3 / 3 / 4 / 11 | not yet measured |
| em dashes | 1 / 1 / 1 | 0 | 5 / **7** | **0 / 0** |
| dead links | 5 of 15 | 2 of 13 | 1 of 16 | **0 of 52, 0 of 62** |

Read the em dash row across. Naming it worked, then stopped working. When it came
back the rule was sharpened from "em dashes give you away" to "ZERO EM DASHES.
Not one." and the count went from five to **seven**. Counting them at write time
took it to zero on the next build and the one after.

Cards are the same story one step behind: named, gone, then back, and worst on
the page rewritten to act on a review, because "vary the layout" gets implemented
as more boxes. They are counted now and there has not yet been a build to measure
it on, which is why that cell says so rather than guessing.

A card is a structural decision made once, and a rule reaches it. Punctuation
emitted mid-sentence is a habit below the level any instruction operates at. The
rule that came out of it, and that now governs this codebase: **if something
survives being named twice, stop writing rules about it and count it.**

### Three rungs, in order

The same shape appeared often enough to be a rule rather than a discovery:

1. **Put it in a playbook.** Reaches structural decisions and nothing else.
2. **Attach it to a call she already makes.** Works often. The design rules ride
   on `project_new`; the wiring report rides on `file_write`.
3. **Refuse to finish without it.** Works.

`review` — a fresh pair of eyes that sees a screenshot of the rendered page and
nothing about her, no conversation, no persona, no tool trail — had rungs one and
two and was never called across two four-page builds. With the gate, she ran it
and then rewrote four files against what came back.

### Memory is a budget, not a bucket

A million-token window is not a reason to send everything. Tiers are ordered
most-stable-first, because the prompt cache rewards a stable prefix:

```
[identity] [facts] [episodes] [working history] [retrieved] [current turn]
 static     rare     append      append-only      volatile    volatile
```

Eviction is chunked rather than sliding, so the cached prefix survives many turns
instead of being invalidated every request. Budgets cascade: unspent allowance
falls through to the tier below, so short conversations send short prompts. The
measured cache hit rate is **79%** across 196M input tokens.

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
refused outright when there is nobody to ask. Shell-outs invoke binaries directly
with a timeout, never through a shell, so arguments cannot inject commands.

Credential fields report their length and never their contents — `browser_inspect`
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
separates them. The default policy warns and never enforces. Two fixes that
mattered are recorded in the code: cepstral coefficient c0 is excluded because it
encodes loudness rather than identity, and embeddings are centred before
normalising, which turns cosine into correlation. Together they widened the
synthetic margin from 0.016 to 0.480.

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
make check          # fmt, vet, 613 tests, build
make test
make install        # to ~/.local/bin
go test ./... -race
```

Developed on Linux Lite 8, i7-4600U, no GPU. It is meant to be comfortable there.
