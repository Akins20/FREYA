# The window

A desktop window for Freya, built the same way as everything else here: standard
library only, hand-written HTML, CSS and JavaScript, no framework.

## Why a served page rather than a native toolkit

Every native option costs the thing that makes this project what it is. Electron
is a browser and a Node runtime; Tauri is a Rust toolchain; Fyne and Wails are
large dependency trees with cgo underneath. Any of them ends "builds in seconds,
offline, one static binary".

The page costs nothing. `net/http` and `embed` are standard library, so the
markup ships inside the same binary, and Chrome opens it with `--app=` — no tab
strip, no address bar, its own entry in the window list. It is a window.

It is also the only option that reuses what is already here rather than
duplicating it. The GUI is a **view**: the agent, memory, guard, tools and voice
are the ones already running. Nothing about her moves into the front end, which
is why the window can be thrown away and rewritten without touching her.

## Phases

**1 — The transport.** An HTTP server on loopback only, with a token minted per
run. Server-sent events carry what happens during a turn; a POST starts one.
Refuses anything that is not local and not carrying the token, because this
endpoint can run shell commands.

**2 — The shell.** The window itself: conversation, composer, sidebar. Her own
palette from `presentation/brand` — amber on near-black, warm white in light —
so it is recognisably hers rather than a copy of somebody else's product.

**3 — The turn, live.** Thinking, tool calls and interim narration stream in as
they happen, each foldable, so a long turn is legible rather than a spinner.

**4 — History.** The archive is already the source of truth and already
searchable. The sidebar reads it.

**5 — What only she has.** A guard confirmation answered in the window instead of
at a terminal. The plan, with steps ticking over. Voice on and off. What the day
has cost.

**6 — Launch.** `freya -gui` opens the window against a daemon that is already
running, or starts one.

## What the front end may not become

It renders and it sends. It does not decide, remember, or hold state that matters
— every one of those already has an owner on the Go side, and a second copy in
JavaScript would be a second answer to questions that must have one.
