# JARVIS / Freya — build tracker

Living checklist. Updated as work lands.

## Phase 0 — environment verification ✅

- [x] Confirm hardware: i7-4600U, 11GB RAM, no GPU, Intel HD graphics
- [x] Confirm Go 1.26.3 installed; build cache on ext4, not the 99%-full NTFS drive
- [x] Confirm NTFS mount supports exec bit + symlinks (`acl` option) — no workarounds needed
- [x] Confirm audio stack: pactl/pulseaudio/arecord present; mic ALC3232 detected
- [x] Verify Gemini key auth mode → works as API key via `x-goog-api-key`
- [x] Confirm `gemini-3.1-flash-lite` exists: 1,048,576 in / 65,536 out
- [x] Verify Gemini function-calling wire format (uppercase schema enums)
- [x] Discover `thoughtSignature` requirement for Gemini 3.x multi-turn tool use
- [x] Calibrate token estimate against `countTokens` (≈4.9 chars/token measured)
- [x] Verify Serper `/search`, `/news`, `/scrape` response shapes

## Phase 1 — core (text-first) ✅

- [x] `internal/llm` — provider-agnostic interface
- [x] `internal/llm/gemini.go` — Gemini provider incl. thought signatures
- [x] `internal/llm/anthropic.go` — Claude provider (swappable alternative)
- [x] `internal/llm/mock.go` — offline provider, runs with no API key
- [x] `internal/config` — env + .env loading, data dir on ext4
- [x] `internal/memory/types.go` — Turn, Episode, Fact, Snapshot
- [x] `internal/memory/budget.go` — tier budgets, cascading allocator
- [x] `internal/memory/store.go` — append-only JSONL archive, atomic JSON state
- [x] `internal/memory/retrieval.go` — BM25 index, pure Go
- [x] `internal/memory/builder.go` — tiered context assembly
- [x] `internal/skills/registry.go` — tool registry + arg coercion + safe exec
- [x] `internal/skills/system.go` — status, volume, brightness, open
- [x] `internal/skills/memory.go` — remember/recall/forget/stats
- [x] `internal/skills/notes.go` — notes + reminders
- [x] `internal/skills/web.go` — Serper search/news/fetch/research
- [x] `internal/skills/dev.go` — projects, git, find, read, grep
- [x] `internal/agent/persona.go` — configurable personality
- [x] `internal/agent/agent.go` — tool-calling loop
- [x] `cmd/freya/main.go` — REPL
- [x] Makefile, .env.example, README, CLAUDE.md

## Phase 2 — verification ✅

- [x] `go vet` clean
- [x] Unit tests: budget allocation, eviction, BM25, slugify, due parsing
- [x] Build succeeds; binary runs offline with mock provider
- [x] End-to-end against real Gemini 3.1 Flash-Lite
- [x] Multi-step tool chains (thought-signature replay)
- [x] Memory persistence across restarts
- [x] Path-escape refusal in dev skills
- [x] Quirk hunt: empty input, huge input, unknown tool, bad args, no network
- [x] Personality switching verified live

### Bugs found during verification, and fixed

1. **Eviction never got below budget.** The target was computed from the current
   total instead of the budget, so the working set stayed over the limit and
   re-evicted on *every* exchange — destroying the prompt-prefix cache the
   chunking exists to protect. Caught by `TestWorkingSetEvictsInChunks`.
2. **Eviction could empty the working set.** A turn larger than the whole budget
   drained every turn including the one in flight. Now the newest turn is never
   evicted. Caught by `TestWorkingSetProgressesOnOversizedTurn`.
3. **`df` parsing broke on spaces in mount paths.** `/run/media/akins/Akins Drive1`
   split into two fields and printed garbage. Now anchors on the four trailing
   columns.
4. **Mock provider substring collision — caused a real side effect.** A 500KB
   input containing "mute" as a substring routed to `system_volume` and set the
   machine to 0%. Matching is now whole-word within a bounded prefix. Regression
   test: `TestMockIgnoresChanceSubstringsInLargeInput`.
5. **Dead mock rules.** `reminder_add` / `reminder_list` were never registered
   skills, so those rules could never fire. Reminders now route to `note_add`
   with a parsed due time.
6. **Round-cap discarded all work.** Hitting the tool-round limit returned an
   apology and threw away every result gathered. Now makes a final tool-free
   call so the model must answer from what it has. Cap raised 8 → 12.

## Phase 3 — voice (next)

- [ ] Install `cmake` (required to build whisper.cpp and piper)
- [ ] Build whisper.cpp; fetch `base.en` model to ext4, not the external drive
- [ ] `internal/voice/stt.go` — whisper.cpp binding or CLI shim
- [ ] Build/fetch piper + a voice model
- [ ] `internal/voice/tts.go` — piper shim, streaming sentence-by-sentence
- [ ] Push-to-talk hotkey capture
- [ ] Barge-in: stop speaking when the user starts talking
- [ ] Optional wake word ("Hey Freya") once push-to-talk is solid

## Phase 4 — later

- [ ] Model-written episode summaries (replace mechanical distillation)
- [ ] Streaming responses (needed for low-latency TTS)
- [ ] Reminder daemon that actually fires notifications
- [ ] Embedding-based retrieval as a `Retriever` implementation
- [ ] Swap JSON fact store for SQLite if the archive outgrows RAM

## Known limitations (accepted for now)

- Archive loads fully into RAM at startup. Fine to ~100k turns; revisit after that.
- Episode summaries are mechanical, not model-written (Phase 4).
- BM25 is lexical — it will miss paraphrases that share no vocabulary.
- Reminders store a due time but nothing fires them yet (Phase 4).
