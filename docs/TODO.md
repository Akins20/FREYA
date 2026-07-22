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

## Phase 3 — voice ✅

Plan changed on measurement. Local Whisper was the original design; benchmarking
killed it. Gemini already holds the conversation, accepts audio natively, and on
this hardware is both faster and more accurate than on-device inference. Neither
cmake nor whisper.cpp nor any model download was needed.

Transcription latency by upload format, 6s clip, gemini-3.1-flash-lite:

| format | size | round trip | transcript |
|---|---|---|---|
| wav | 190 KB | 8.34 s | perfect |
| mp3 | 18 KB | 2.89 s | perfect |
| **ogg** | **31 KB** | **1.83 s** | **perfect** |

Upload dominates latency, so recordings are encoded to Ogg during capture.

- [x] `llm.AudioTranscriber` — optional provider capability, Gemini implements it
- [x] `voice/record.go` — sox capture, auto-stops after 1.5s of silence
- [x] `voice/stt.go` — GeminiSTT (default) + WhisperSTT (offline fallback)
- [x] `voice/tts.go` — espeak (offline fallback) + piper (optional)
- [x] `voice/tts_gemini.go` — Gemini neural TTS, **default**, sentence-pipelined
- [x] `voice/style.go` — pace/pitch/tone/voice profile, persisted
- [x] `skills/voice.go` — `voice_adjust` so Freya retunes her own delivery
- [x] `voice/session.go` — listen → think → speak loop
- [x] `voice/mfcc.go` — pure-Go FFT, mel filterbank, cepstral embedding
- [x] `voice/verify.go` — speaker profile, enrolment, policy gate
- [x] REPL: `/voice on|off|enroll|test|policy|threshold|say`, Enter = push to talk
- [x] Verified live: espeak TTS audible, GeminiSTT 2.24s on the real code path

### Speaker verification: implemented, NOT validated

The honest result. Measured on real audio (four espeak voices, one enrolled):

| voice | cosine | population |
|---|---|---|
| OWNER (unseen phrase) | 0.981 | 0.561 |
| stranger en-us | 0.963 | 0.435 |
| stranger en-sc | 0.980 | **0.539** |
| stranger en+f3 | 0.722 | 0.219 |

The nearest impostor sits 0.022 below the owner. **No threshold separates them.**
Two fixes already landed and helped enormously — dropping cepstral coefficient
c0 (loudness, not identity) and centring the embedding before normalising, which
together widened the synthetic-audio margin from 0.016 to 0.480 — but real
speech still defeats it.

Caveat in its favour: all four test voices come from the same formant
synthesiser and share machinery real vocal tracts would not. Human voices may
separate better. That is a hypothesis, not a result.

Therefore: default policy is `warn`, never `enforce`. `/voice test` exists so the
claim can be measured against a real second person before anyone relies on it.

Cost is not the problem — verification benchmarks at **17.6 ms** per utterance
against a ~1,830 ms STT round trip, under 1% of the pipeline.

- [ ] Validate with two real human voices via `/voice test`
- [ ] If it fails there too: neural speaker embedding (ECAPA-TDNN via ONNX).
      The `Verifier` interface exists so this drops in without touching the loop.
### Voice quality

espeak was the first cut and sounded it: male, monotone, deaf to punctuation.
Gemini's speech models are now the default. They take delivery direction in
plain English, which means the persona can drive how she *sounds*, not just
what she writes.

| engine | latency | quality |
|---|---|---|
| espeak | instant | male, monotone, ignores punctuation |
| **gemini** | **3.5-4.0s** | natural prosody, 26 voices, style-steerable |
| piper | ~0.5s (est.) | good neural, needs a 60MB download |

Latency is mitigated by pipelining: sentence N+1 synthesises while sentence N
plays, so only the first sentence is waited on.

Delivery is a persisted profile — voice, pace (120-240 wpm), pitch, tone — and
`voice_adjust` exposes it as a tool, so "slow down, you sound too cheerful"
retunes her mid-conversation. Verified: she called
`voice_adjust tone=calm,professional pace=slow` unprompted and it persisted.

- [ ] Consider piper as a low-latency offline option (needs a download)
- [ ] Barge-in: stop speaking when the user starts talking
- [ ] Wake word ("Hey Freya") once push-to-talk is proven

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
