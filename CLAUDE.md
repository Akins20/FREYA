# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Freya is the assistant; the repository is named for her. A personal AI assistant
in Go with a terminal REPL and an optional spoken mode (`/voice on`).

**Zero external dependencies — pure standard library.** This is deliberate: builds are
instant and offline on modest hardware, there is no supply-chain surface, and the binary
is fully static when built with `CGO_ENABLED=0`, which the Makefile sets. Do not add a
dependency without a concrete reason that outweighs this.

## Commands

```bash
make build          # compile to ./bin/freya
make run            # build and start the REPL
make offline        # REPL with the no-key mock provider
make test           # all tests
make check          # fmt + vet + test + build
make install        # copy to ~/.local/bin

go test ./internal/memory/                              # one package
go test ./internal/agent/ -run TestToolCallRoundTrip -v  # one test
go test ./... -race                                      # race detector

./bin/freya -ask "question"          # one-shot, no REPL
./bin/freya -v                       # REPL with tool tracing + token accounting
./bin/freya -provider mock           # offline, no API key needed
./bin/freya -provider gemini -model gemini-2.5-flash
```

## Configuration

`.env` (gitignored) or environment. `internal/config` loads .env first; real environment
variables always win.

| Variable | Purpose |
|---|---|
| `GEMINI_API_KEY` | Reasoning model. Auth is the `x-goog-api-key` header, not `?key=`. |
| `SERPER_API_KEY` | Web search, news, page scraping. |
| `ANTHROPIC_API_KEY` | Alternative provider. |
| `FREYA_PROVIDER` | `gemini` \| `anthropic` \| `mock`. Auto-detects from available keys. |
| `FREYA_MODEL` | Defaults to `gemini-3.5-flash-lite`. Newer is not better here: `gemini-3.8-flash` measured 20% against its 70% on the same suite — see `docs/benchmarks/model-comparison-2026-09-07.md`. |
| `FREYA_THINKING` | Reasoning-before-acting + the visible thinking window. `on`/empty (default) lets the model decide depth; `off` disables; a number caps thinking tokens. Thoughts surface via `OnThought` (shown as `💭` in the REPL, journalled by the daemon). Thinking tokens bill at the output rate, so lower it if latency/cost bite. |
| `FREYA_FOLLOWUP` | Quiet-moment re-engagement: after this long without conversation (default 6m; a duration like `5m`; `off` disables), the daemon reviews the recent exchange and, once per lull, follows up on a genuine loose end (a deadline you're behind on, a task worth starting). Reflection-only (no tools), gated by chattiness, silence by default. |
| `FREYA_ACTIVITY_DEPTH` | The daemon samples the focused window every 60s (classified: browser/terminal/files/editor) for ambient context. For a **browser or folder** that just changed, it also screenshots that window and vision-reads the actual content (page, files) — set `off` to disable that deeper read (it costs a vision call and shows the window to the model); title-level sampling stays on regardless. |
| `FREYA_DATA_DIR` | Memory location. Defaults to `~/.local/share/freya`. |
| `FREYA_PROJECTS_DIR` | What the dev skills scan. Defaults to the repo's parent. |
| `FREYA_SOURCE_DIR` | Her own checkout, for the self-repair loop. When a task fails badly she files a report (`defects.jsonl`), and the daemon runs an engineer against this repo to decide whether it was her software. Work lands on a **branch** and is never deployed — see `cmd/freya/consult.go`. Unset disables the loop; the journal still records. |
| `FREYA_WORK_DIR` | Fixed working dir she anchors to at startup (file + shell tools share it). Empty leaves her where launched — the benchmark relies on this. The daemon sets it to `~/freya-workspace`. She moves within it via the `change_dir` tool. |
| `FREYA_TTS` | `gemini` (default) \| `espeak` \| `piper` \| `none`. |
| `FREYA_STT` | `gemini` (default) \| `whisper` (offline). |
| `FREYA_TOOL_ROUTING` | Narrows the tools she is **shown** to those a request calls for. The 96-down-to-27-53 figure was measured at 96 registered tools and the ratio is the point, not the range. `/tools` prints the live count; this line deliberately does not, having been wrong about it three times. `off` disables it. Everything stays **executable** either way: a tool she names that was not offered still runs and the miss is counted (`/tools`). See `internal/skills/kits.go` — narrowing is the one change here that fails silently, so the valve and the counter are the design, not extras. |
| `FREYA_DOWNLOAD_DIR` | Where browser downloads land. Set explicitly on every tab so the OS "Save as" window never opens — that window is not page content, so nothing can drive it and she cannot even tell it is there. Defaults to `~/Downloads`. |
| `FREYA_WAKE` | Always-on wake-word listening. **Off unless you set it** — `on` enables it, a duration like `2h` enables it with a timeout. There is no local wake-word model, so while it is on, speech near the mic is recorded and sent for transcription whether or not it was meant for her; that is opt-in only. It had no off switch at first, and was quiet only because starting the listener happened to fail — then the switch landed with the default still on, because an empty string fell through to `forever`, and the mic came on for a user who had set nothing. Push-to-talk needs none of this and is unaffected. |
| `FREYA_VOICE_POLICY` | `off` \| `warn` (default) \| `enforce`. Never default to enforce. |
| `FREYA_SAFETY` | Gemini's content filter threshold. **Defaults to `OFF`** — set it to a Gemini threshold name (`BLOCK_MEDIUM_AND_ABOVE`, etc.) to turn filtering on. Undocumented until an audit found six settings the code reads and nothing described; this one decides whether a filter runs at all, so it is the one worth knowing about. |
| `FREYA_DRY_RUN` | Assess every action and execute none. The `-dry-run` flag, as an environment variable, for the daemon. |
| `FREYA_CHATTINESS` | How readily the daemon speaks unprompted. |
| `FREYA_WAKE_ACK` | What she says when the wake word fires. Only reachable when `FREYA_WAKE` is on, which is off by default. |
| `FREYA_GLOW` | The status light. `off` disables it; it once cost an evening to track down. |
| `FREYA_HOTKEY_KEYCODE` | Overrides the push-to-talk keycode for keyboards where the default is wrong. |
| `FREYA_ADDRESS` | What she calls you. Overrides the persona's stored form of address. |
| `FREYA_VERBOSE` | Tool tracing and token accounting, as the `-v` flag but for the daemon. |
| `FREYA_VOICE_NAME` | Which Gemini neural voice to synthesise with. |
| `FREYA_WHISPER_MODEL` | Model path for offline STT, used only when `FREYA_STT=whisper`. |
| `FREYA_PIPER_MODEL` | Model path for Piper TTS, used only when `FREYA_TTS=piper`. |

State lives in `$FREYA_DATA_DIR`, never in the repo: `archive.jsonl`, `defects.jsonl`, `facts.json`,
`episodes.json`, `notes.json`, `persona.json`, `voicestyle.json`, `voiceprint.json`
(mode 600, biometric), `state.json`, and hand-editable `identity.md`.

## Architecture

Four layers, each depending only on those above it:

```
cmd/freya         REPL, slash commands, ANSI output, the window's wiring
internal/gui      the desktop window: served page, SSE, permission, voice
internal/agent    think-act loop + persona
internal/work     background jobs: bounded pool, cancellation
internal/memory   tiered memory, context assembly, BM25 retrieval
internal/skills   the tool registry and every capability
internal/llm      provider-agnostic model interface
internal/voice    record, recognise, synthesise, speaker gate
```

### A background job is an isolated conversation

`internal/memory/journal.go` explains this in full; the short version is that
interleaving two threads of work into one append-only archive corrupts both the
transcript and the cached prefix. So a job runs against a `memory.Branch`:
identity/facts/episodes read live from the real store, the conversation frozen at
spawn, its own turns accumulating separately. One summary turn rejoins the archive
when it ends; the full transcript goes to `jobs.jsonl`.

Two things must not be varied per job, and a test pins each: **tool declarations**
and the **persona prefix**. Both lead the request, so a per-worker difference gives
every thread its own cacheable prefix and neither can reuse the other's.

### The memory architecture is the load-bearing design

Read `internal/memory/types.go` — its package doc explains the whole model. The short
version: a 1M-token window is not an excuse to send everything every turn.

Tiers are ordered **most-stable first**, because Gemini caches stable prompt *prefixes*.
A prompt whose opening bytes change each turn never hits cache:

```
[identity] [facts] [episodes] [working history] [retrieved] [current turn]
 static     rare     append      append-only      volatile    volatile
```

Two consequences that are easy to break by accident:

- **Eviction is chunked, not sliding.** When the verbatim working set overflows, the
  anchor jumps forward by `EvictionChunk` (25%) and then holds still for many turns.
  Sliding by one turn per exchange would invalidate the cached prefix every request.
  Eviction targets a level *below* budget, never merely equal to it — see the comment
  in `store.go WorkingSet`, which two tests pin down.
- **Tool declarations are sorted** in `registry.Tools()` for the same reason. Do not
  make tool order depend on map iteration.

Budgets cascade: `internal/memory/budget.go` hands each tier an allowance and passes
whatever is unspent to the tiers below, so short conversations send short prompts.
`Budget.ScaleTo` retargets the whole architecture at a smaller window unchanged.

The archive (`archive.jsonl`) is append-only and is the source of truth. Nothing is ever
destroyed: turns degrade verbatim → summarised episode → still BM25-searchable.

Token estimation deliberately runs *high* (4.0 chars/token against a measured ~4.9) so
budgets under-fill rather than overflow mid-request.

### The browser is more than the DOM

`internal/browser/events.go` is the second channel. CDP multiplexes replies and
**events** over one socket, and the read loop used to discard every event — so
downloads, javascript dialogs, and windows opening were all invisible. A click
that started a download looked exactly like a click that did nothing, which is
how four clicks and four dialogs happen.

Three consequences worth keeping:

- **Downloads are redirected**, not prompted (`Browser.setDownloadBehavior` on
  every tab). The native chooser is an OS window; nothing here can reach it.
- **JS dialogs are answered**, because an unanswered one blocks the renderer and
  hangs every later call against that page. `beforeunload` is dismissed rather
  than accepted — accepting throws the page away.
- **Side effects are reported.** Actions carry `browser.Describe(client.Since(t))`,
  so a gesture says what happened outside the page as well as inside it.

`internal/browser/gestures.go` holds everything that is not a plain left click —
right-click, double-click, ctrl/shift-click, drag. They share one dispatcher on
purpose: written separately they drift, and the hover-first and press-duration
fixes then exist on some gestures and not others.

### The window is served by whoever owns the archive

`freya -gui` builds no agent. It finds the daemon that already holds the store,
asks it where its window is (`daemon.Ask(dir, "window")`), opens Chrome on that
and exits. Serving a window from a second process would make the daemon yield the
store to it — two agents on one append-only archive, which
`internal/memory/journal.go` explains corrupts the transcript and the cached
prefix together. If nothing is running, `daemon.Spawn` starts one and waits.

Three constraints hold this together and each has a test:

- **One owner for the trace.** `OnThought`/`OnInterim`/`OnTool` are assigned once
  at startup and published to `cmd/freya/trace.go`'s hub; the terminal, the
  daemon's speaker and the window are subscribers. Swapping the fields per turn
  is a data race and silently steals the trace from whoever assigned first.
- **Every turn is registered.** Window turns go through `beginTurn`, so Ctrl-C,
  the spoken "stop" and the daemon's yield loop can see them.
- **"Nobody answered" is not "the user said no."** `gui.Server.Confirm` returns
  `(answer, asked)`, and `cmd/freya/confirm.go`'s router tries window, terminal,
  then speech. Speech is last because a typed answer is the word the person
  meant. `guard.Attended` is set once, from the router, never reassigned.
  **The daemon registers no terminal channel at all**, whatever stdin claims: a
  detached process gets `/dev/null` on fd 0, `/dev/null` is a character device,
  and the old char-device `isTerminal` therefore answered "yes, somebody is
  there" — so the prompt read EOF and refused on behalf of a user who was never
  asked. `isTerminal` is a real `TCGETS` now (`cmd/freya/tty_unix.go`).

The window's address is a **one-shot handoff nonce**, not the token: Chrome keeps
its argv, `/proc/<pid>/cmdline` is world-readable, and loopback is reachable by
every local uid — a token there would leave an endpoint that runs shell commands
effectively unauthenticated. See `gui.Server.HandoffURL`.

An outstanding permission question is **held and replayed** to any window that
connects, and carried on `/state` as well as the event stream. `Emit` drops
events when a subscriber is slow, which is right for a thought bubble and turns
this one into a silent five-minute refusal.

Voice in the window presses `pushToTalk` — her existing pipeline, verification
included. Do not add `getUserMedia`: it is a second recorder on one device, and
audio from a web page has walked around the voiceprint.

### How a turn is drawn

One attribute, `data-phase` on `aside.work`, with three values and three writers
(`beginFreyaTurn` → `live`, `sealWork` → `fresh`, `settle` → `settled`). Nothing
is created or re-parented between them; CSS renders the same subtree three ways.
The demotion fires when the **next** turn starts, so the thread quiets behind
you — that one line is most of the answer to a scrollback full of boxes
summarising work nobody is looking at.

Four invariants, each with a test:

- **No code path runs from a thought to the waterfall.** `say()` writes the
  one-line slot and the fold; `addCall()` writes the flow. The shared `trace()`
  sink they both used is deleted, so there is nowhere to put both.
- **A group of one has no group chrome**, and a group with a failure in it never
  folds — not on close, not at `fresh`. A failure you have to click to find is a
  failure you do not see.
- **Nothing in the flow re-sorts.** Grouping folds *consecutive* entries of one
  family; a waterfall that reorders is a lie about time.
- **Zero `innerHTML`.** Tool names, arguments, error text and archive content all
  go in as `textContent`, which makes the XSS boundary a grep.

`Event.Call` identifies one invocation. A round's tools run on separate
goroutines, so six `file_read` at once is ordinary, and pairing a finish to a
start by name alone was a guess — survivable when a call was one dot, a visible
lie now the row carries arguments, a duration, a bar and an error message.

The permission request is a card in the thread, appended to `#thread` and never
to the turn: a confirm can arrive with no turn running and can be replayed to a
window that connects late, so there may be no turn to write into.

### Working in a document that is already open

`internal/skills/document.go`. `internal/docs` writes files; this drives the one
on screen, with the user's unsaved edits in it. Four skills: `document_list`,
`document_read`, `document_write`, `document_save`.

**The clipboard, not the accessibility tree, and that was measured.** LibreOffice
does expose its grid over AT-SPI, down to `Table` and `TableCell` — and it is
unusable: the sheet reports its size as the whole addressable grid (1,048,576 x
16,384) rather than the used range, cell objects exist only while on screen, and
`Text.GetText` on a cell holding "region" came back empty. Selecting and copying
returned the entire used range first go, tab-separated, **including the computed
value of a formula** — the number the user can see and the file on disk does not
contain.

Three consequences worth keeping:

- **A sheet is selected with Ctrl+End then Ctrl+Shift+Home**, never Ctrl+A. In
  Calc, Ctrl+A selects a million empty rows and copying that looks like a hang.
- **Saving answers the format dialog by its button label.** A .xlsx save puts up
  "Non-standard file format", and the wrong button silently rewrites it as .ods.
  Enter happens to keep the format on this build; that is a fact about one build.
  The dialog publishes no window title, so it is found by walking LibreOffice's
  windows for a button that says what it does.
- **A paste into an open document is RiskHigh, so it outranks autonomy.** Every
  other synthetic input types into a window the user is watching. This one
  overwrites a region of a document that may hold work existing in no file, since
  the file on disk is the last save. At the KindInput default of medium it
  auto-approved under `-yes` and in the daemon. `internal/guard/rules.go` raises
  it and a test pins that.

### Provider abstraction

`internal/llm/llm.go` defines neutral `Message`/`Tool`/`Response` types. Each provider
file translates to its own wire format. Adding a provider means adding one file; nothing
in `agent` or `skills` changes.

Provider-specific traps already handled — don't regress them:

- **Gemini** wants schema types upper-cased (`OBJECT`, not `object`), uses roles
  `user`/`model`, and returns a **`thoughtSignature`** on tool calls that must be echoed
  back verbatim on later turns or 3.x models lose reasoning across tool steps. This rides
  on `ToolCall.Signature`.
- **Anthropic** requires consecutive tool results coalesced into a *single* user message;
  emitting one message each is a 400.
- **Mock** (`mock.go`) is the offline stand-in that makes the whole loop testable with no
  key. It matches on **whole words within a bounded prefix** — a regression test pins this,
  after a large paste containing "mute" as a substring once set the machine's volume to 0.

### Agent loop

`internal/agent/agent.go`. Assemble context → ask → execute tools → feed results back →
repeat, capped at `maxToolRounds`. Two behaviours worth preserving:

- A failing tool is **not** fatal. The error text goes back to the model as a tool result
  so it can adapt.
- On hitting the round cap, the agent makes one final call **with no tools offered**,
  forcing an answer from what it already gathered rather than discarding the work.

### Skills

One `Handler` plus one `llm.Tool` declaration, registered in `internal/skills`. Arguments
arrive as JSON, so numbers are `float64` and any field may be absent — always use the
`argString`/`argInt`/`argBool` helpers, which coerce leniently rather than panicking.

Shell-outs go through `run()`, which invokes binaries directly with a timeout — never
through a shell, so arguments cannot inject commands. Use `have()` to degrade with a
useful message when a binary is missing. The single exception is the `run_shell` skill,
which exists for pipes and redirection and hands `bash -c` a string; it is deliberate,
and the guard prices it accordingly. Do not add a second one.

Dev skills are confined to `FREYA_PROJECTS_DIR` by `devSkills.resolve`, which clamps
traversal and absolute paths back into root. Tests assert this.

### Persona

`internal/agent/persona.go`. Personality is **data, not prose**: traits are keys into a
catalogue of behavioural instructions, adjustable at runtime via `/persona` and persisted
to `persona.json`. Default is sassy + friendly + casual + blunt + direct.

The **anti-sycophancy block is not a trait** — it is unconditional and must survive any
trait change. A test enforces this. The user has explicitly ruled out sycophancy.

### Voice

`internal/voice`. The pipeline is record (sox, auto-stops on silence) →
Recognizer → agent → Synthesizer, each an interface.

Two decisions were made by measurement, not preference, and the numbers are in
the package docs:

- **Speech recognition goes to Gemini, not local Whisper.** On this hardware
  cloud STT is faster *and* more accurate. Recordings are encoded to Ogg during
  capture: upload dominates latency (190KB WAV = 8.3s, 31KB Ogg = 1.8s, same
  transcript). Do not switch the capture format to WAV for convenience.
- **Synthesis defaults to Gemini's neural voices.** espeak is the offline
  fallback and sounds like it. Sentences are synthesised one ahead of playback,
  so only the first is waited on.

`Style` (voice/style.go) holds pace, pitch, tone and voice, persisted to
`voicestyle.json`. It is expressed in words rather than numbers because Gemini
takes natural-language delivery direction; `WPM()` and `PitchValue()` convert
for espeak. The `voice_adjust` skill exposes it so Freya retunes herself when
asked.

**Speaker verification is implemented but unvalidated.** Measured on real audio
the nearest impostor scored 0.022 below the owner — no threshold separates them.
Default policy is `warn`; never change the default to `enforce`. Two fixes
already landed and matter: cepstral coefficient c0 is excluded (it encodes
loudness, not identity, and dominates the vector) and embeddings are centred
before normalising (turning cosine into correlation). Together those took the
synthetic margin from 0.016 to well clear of the 0.15 floor the suite asserts;
`TestEnrollmentAndVerification` logs the live figure every run, so read it there
rather than quoting a number that ages. Do not reintroduce c0.

## Platform notes

Developed on Linux Lite 8 (Ubuntu 26.04), i7-4600U, no GPU.

- The repo lives on an **NTFS drive mounted with `acl`** — exec bits and symlinks work,
  but the drive sits at ~99% capacity. Keep models, caches, and data off it.
- Mount paths **contain spaces** (`/run/media/akins/Akins Drive1`). Parsing `df` output
  by leading fields breaks here; `system_status` anchors on trailing columns instead.
- `cmake` is **not installed** and is required before Phase 2 voice work.

## Conventions

- Comments explain *why*, especially where a subtle constraint drove the code. The
  codebase is written to be re-read; keep that density.
- Every behaviour with a non-obvious failure mode gets a test that names the failure.
- `docs/TODO.md` tracks the build. Update it as work lands.
