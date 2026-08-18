# Freya, the film

A short film about what she is for. Not a slide deck with numbers on it, and not
a feature list read aloud.

## The message

Everything else you talk to lives somewhere else. You copy things over to it, you
paste the answers back, and you do all the clicking in between. You are the
integration layer.

She lives here, on your machine, in your browser, in your files. So the whole
film is one job that nobody could do without every part of her working in
sequence, and the point is the handoff between them rather than any one trick.

## The shape

Four movements, eighteen scenes, about three minutes.

**I. The ask.** A Tuesday morning and one sentence typed into a chat window:
check the supplier statement against our sheet, put a summary in the shared
drive, book the four o'clock call. Nobody touches the keyboard after that.

**II. The errand.** Nine scenes that are really one continuous shot. The chat
window and a running list of what she has done stay on screen the whole way
through while the working window changes hands:

1. she asks the one thing she cannot safely assume, which account
2. five Chrome profiles, and she opens the right one
3. the inbox, and the message that came in at 07:41
4. two attachments come down onto the disk
5. she reads the PDF, line by line
6. their numbers next to ours, and one line does not match
7. she writes it up as a Word document, laid out rather than dumped
8. it goes into the shared drive
9. the call goes in the diary with the file attached
10. she reports back, including the thing she could not settle

Every step maps to a tool that exists: `browser_profiles`, `service_open`,
`browser_downloads`, `file_read`, `xlsx_write`, `docx_write`, `browser_upload`,
`pdf_write`, `document_convert`.

**III. Not a one-off.** The browsing measured on its own: fifteen WebVoyager
tasks on eight live sites, all fifteen answered, none blocked, none abandoned.
Then the desktop, where four toolkits disagree about how a button is pressed and
each application confirms in its own log that she pressed it.

**IV. Why you can leave it alone.** Memory that goes back months and can find the
part that matters. A daemon that stays quiet unless it is worth speaking. And
then who owns it: one file, no account, no subscription, nobody else's server.

## Why the honest half is in it

Scene 6 turns on a discrepancy she finds and cannot resolve, and scene 10 has her
say so rather than round it off. A film that only shows the win is an
advertisement. The measured version of that is real: given an ecommerce brief
covering front end and back end, she built the front end and did not mention the
rest, and the five completion checks in `internal/agent` were each written after
a specific run went wrong.

## The look

Near-black ground, warm off-white type, one amber that only ever means "this
actually happened", and one red that only ever means "this did not add up".
Paper is cream: the documents and spreadsheets she produces are the only bright
objects in the film, which is the entire colour logic.

Nothing bounces. The easing curve has no overshoot anywhere. Figures settle like
an instrument coming to rest rather than spinning like a scoreboard.

Everything is composed inside a fixed 1600x900 stage that is then scaled to
whatever window is playing it, so a coordinate in the script is a coordinate in
the finished frame and the video capture needs no separate layout.

## The motion layer is Go

`presentation/wasm/effects.go` compiles to WebAssembly and renders the per-pixel
work at half resolution, thirty times a second:

- film grain that walks rather than flickering, which is what separates grain
  from television static
- dust drifting up through the pool of light on the desk
- a warm bloom that pools over the conversation and stays off the paper
- the dissolve between movements, where a noise edge eats the frame and flares
  amber at the boundary

The dissolve gets its own canvas because the additive plate can only lighten and
a cut has to be able to take the picture away.

## The voice

`presentation/narrate.py` reads `film/narration.js`, synthesises each line with
Gemini and writes `film/vo/`, one file per line plus a manifest carrying every
line's offset on the film clock. The page plays the per-line files as their
subtitles appear, which stays in sync whether the film is running itself or
somebody is stepping through it with the arrow keys. The capture uses the mixed
track laid out on the same clock.

```bash
GEMINI_API_KEY=... python presentation/narrate.py
```

Written to be spoken. Short sentences, contractions, and no word a person would
have to already know. The technical detail is on screen for anyone who wants it
and the voice stays out of its way.

## Two modes, from the first line of code

`?mode=explore` lets a person step through with the arrow keys. `?mode=film` runs
the whole thing on a fixed clock with the chrome hidden, which is what
`presentation/capture/record.sh` records. Frame accuracy cannot be retrofitted,
so every beat declares its time in milliseconds and every reveal hangs off that
same clock.

```bash
presentation/capture/record.sh freya.mp4
```

## Files

```
presentation/
  DESIGN.md          this
  narrate.py         the voice track, via Gemini
  capture/record.sh  the video
  wasm/effects.go    grain, dust, bloom, dissolve
  film/
    index.html       the stage
    film.css         palette, windows, type
    errand.css       the surfaces movement II passes through
    film.js          the projector
    scenes.js        the script
    narration.js     what is said, and when
    data.js          generated from docs/benchmarks, never typed by hand
    effects.wasm     built from ../wasm
```
