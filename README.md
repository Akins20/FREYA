# JARVIS

A personal AI assistant that runs on your own machine. The project is JARVIS;
the assistant is **Freya**.

Written in Go with **zero external dependencies** — the entire thing is standard
library. It builds in seconds, offline, and ships as one static binary.

```
❯ how much space is left on my drives?
  → system_status
You're getting dangerously close to full on that secondary drive — only 14 gigs
left. The main drive is sitting at 39 gigs free, so you've got a bit more
breathing room there, but maybe clean house soon.
```

## Quick start

```bash
cp .env.example .env     # add your keys
make run
```

No keys yet? It still runs:

```bash
make offline             # rule-based stand-in model, no network
```

## What it can do

**System** — disk, memory, uptime, battery, volume, brightness, launch apps
**Memory** — durable facts it chooses to keep, searchable across every past session
**Notes** — notes and reminders with due times
**Web** — search, news, page fetch, and multi-source deep research
**Dev** — list projects, git status, find files, grep, read source

## Memory

Freya remembers across sessions, and the memory design is the interesting part.

A 1M-token context window is not a reason to resend everything every turn. Memory
is tiered, ordered most-stable-first so the model's prompt cache keeps hitting:

| Tier | Holds | Budget |
|---|---|---|
| Identity | Persona, curated profile, standing directives | 8K |
| Facts | Durable extracted knowledge | 32K |
| Episodes | Summaries of conversation that aged out | 96K |
| Working | Verbatim recent turns | 704K |
| Retrieved | BM25 hits pulled back from the archive | 160K |

Budgets cascade — a short conversation sends a short prompt, and the window fills
only as the relationship accumulates.

Nothing is ever destroyed. Turns degrade from verbatim, to summarised, to still
searchable in an append-only archive. Retrieval is BM25 in pure Go: no embedding
service, no vector store, no network round-trip per turn.

Everything lives in `~/.local/share/freya` as plain files you can read, edit, or
delete.

## Personality

Default is sassy, friendly, casual, blunt, and direct. Change it whenever:

```
/persona                          show current
/persona dry, concise, formal     set traits
/persona address boss             what she calls you
/persona custom Reply in British English
/persona reset
/traits                           list all available
```

Not adjustable: she will not flatter you. No opening compliments, no agreeing to
be agreeable, no performed enthusiasm. That's wired in, not a setting.

## Commands

```
/help      /skills     /memory    /context
/persona   /traits     /verbose   /quit
```

```bash
./bin/freya -ask "one-shot question"
./bin/freya -v                          # show tool calls + token accounting
./bin/freya -provider mock              # offline
```

## Configuration

Copy `.env.example` to `.env`:

- `GEMINI_API_KEY` — the reasoning model ([AI Studio](https://aistudio.google.com/apikey))
- `SERPER_API_KEY` — web search ([serper.dev](https://serper.dev))
- `ANTHROPIC_API_KEY` — optional alternative provider

Default model is `gemini-3.1-flash-lite`: a 1M-token window at low cost, which is
what makes the generous memory budgets affordable.

## Development

```bash
make check                        # fmt, vet, test, build
go test ./... -race
go test ./internal/memory/ -v
```

Adding a skill is one handler and one tool declaration in `internal/skills`.
Adding a model provider is one file in `internal/llm`. Neither requires touching
the agent loop.

See `CLAUDE.md` for architecture and `docs/TODO.md` for what's next.

## Status

Text-first and working. Voice is Phase 2 — local whisper.cpp for speech-to-text
and piper for speech, with push-to-talk before any wake word.
