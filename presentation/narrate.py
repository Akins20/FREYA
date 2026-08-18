#!/usr/bin/env python3
"""Synthesise the film's narration with Gemini, one file per line.

    GEMINI_API_KEY=... python presentation/narrate.py

The key is read from the environment, or from the repository's .env if it is not
set there. Nothing is printed that could leak it.

# Why one file per line rather than one long track

The film plays in two modes. In film mode the scenes run back to back on a fixed
clock, so a single track would sync. In explore mode a person steps between
scenes with the arrow keys, and a single track would be meaningless the moment
they went back a scene. Per-line files play when their subtitle appears, which is
correct in both, and the capture can still lay them out on the absolute clock
because the manifest carries every line's offset.

# Standard library only

Same rule as the rest of the repository. urllib does the HTTP, the wave module
writes the container, and the only outside program is ffmpeg, which is used once
at the end to lay the lines out on the film clock and is skipped if it is absent.
"""

import base64
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
import wave
from pathlib import Path

HERE = Path(__file__).resolve().parent
FILM = HERE / "film"
OUT = FILM / "vo"

# Gemini returns raw signed 16-bit PCM at 24kHz, mono. It is not a WAV: there is
# no header, so one has to be written or every player rejects the file.
RATE, CHANNELS, WIDTH = 24000, 1, 2

MODEL = "gemini-2.5-flash-preview-tts"
ENDPOINT = "https://generativelanguage.googleapis.com/v1beta/models/{}:generateContent"

# Kore is even and unhurried. The delivery note matters more than the voice name:
# without it the model reads advertising copy as advertising copy.
VOICE = os.environ.get("FREYA_VOICE_NAME", "Kore")
DIRECTION = (
    "Read this as the narrator of a short documentary about a piece of software. "
    "Calm, warm, unhurried, and completely unsalesy. Do not sound excited. "
    "Let the sentence end. Say it plainly, like you mean it:\n\n"
)


def api_key():
    key = os.environ.get("GEMINI_API_KEY")
    if key:
        return key
    env = HERE.parent / ".env"
    if env.exists():
        for line in env.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line.startswith("GEMINI_API_KEY="):
                return line.split("=", 1)[1].strip().strip('"').strip("'")
    sys.exit("GEMINI_API_KEY is not set, and there is no .env with one in it.")


def lines():
    """Read the narration out of narration.js, which is the page's own copy.

    Parsed rather than duplicated, so the voice track and the subtitles cannot
    say different things.
    """
    src = (FILM / "narration.js").read_text(encoding="utf-8")
    body = src[src.index("const NARRATION = ") + len("const NARRATION = "):]
    body = body[: body.rindex("]") + 1]
    # JavaScript object literals use bare keys and allow a trailing comma; JSON
    # allows neither, so both are normalised before parsing.
    body = re.sub(r"([{,]\s*)(\w+):", r'\1"\2":', body)
    body = re.sub(r",(\s*[\]}])", r"\1", body)
    return json.loads(body)


def say(key, text):
    req = urllib.request.Request(
        ENDPOINT.format(MODEL),
        data=json.dumps({
            "contents": [{"parts": [{"text": DIRECTION + text}]}],
            "generationConfig": {
                "responseModalities": ["AUDIO"],
                "speechConfig": {
                    "voiceConfig": {"prebuiltVoiceConfig": {"voiceName": VOICE}}
                },
            },
        }).encode(),
        headers={"Content-Type": "application/json", "x-goog-api-key": key},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=120) as r:
        payload = json.load(r)
    part = payload["candidates"][0]["content"]["parts"][0]
    return base64.b64decode(part["inlineData"]["data"])


def write_wav(path, pcm):
    with wave.open(str(path), "wb") as w:
        w.setnchannels(CHANNELS)
        w.setsampwidth(WIDTH)
        w.setframerate(RATE)
        w.writeframes(pcm)
    return len(pcm) / (RATE * CHANNELS * WIDTH)


def scene_starts(scenes_js):
    """Absolute start time of each scene, so the capture can lay the audio out.

    The durations live in scenes.js next to the scenes they belong to, and the
    dissolve between two scenes takes a fixed amount of time that film.js owns.
    Both are read from the source rather than restated here, because a number
    restated in two files is a number that will disagree with itself.
    """
    durs = [int(m) for m in re.findall(r"dur:\s*(\d+)", scenes_js)]
    film_js = (FILM / "film.js").read_text(encoding="utf-8")
    cover, reveal = re.search(r"COVER = (\d+), REVEAL = (\d+)", film_js).groups()
    gap = int(cover) + 20 + int(reveal) + 30
    starts, t = [], 0
    for d in durs:
        starts.append(t)
        t += d + gap
    return starts, t


def main():
    key = api_key()
    OUT.mkdir(exist_ok=True)
    starts, total = scene_starts((FILM / "scenes.js").read_text(encoding="utf-8"))

    out = []
    for i, line in enumerate(lines()):
        name = "%03d.wav" % i
        pcm = say(key, line["text"])
        dur = write_wav(OUT / name, pcm)
        out.append({
            "index": i,
            "scene": line["scene"],
            "at": line["at"],
            "abs": starts[line["scene"]] + line["at"],
            "file": name,
            "dur": round(dur, 3),
            "text": line["text"],
        })
        print("%2d  %5.2fs  %s" % (i, dur, line["text"][:64]))

    (OUT / "manifest.json").write_text(
        json.dumps({"total": total, "lines": out}, indent=2), encoding="utf-8")

    mix(out, total)
    print("\n%d lines, film runs %.1fs" % (len(out), total / 1000))


def mix(spoken, total_ms):
    """Lay every line on the film clock as one track, for the video capture."""
    if not spoken:
        return
    try:
        subprocess.run(["ffmpeg", "-version"], capture_output=True, check=True)
    except (OSError, subprocess.CalledProcessError):
        print("\nffmpeg not found, so the individual lines are all there is. "
              "The page plays those directly; only the video mux needs the mix.")
        return

    args = ["ffmpeg", "-y"]
    for s in spoken:
        args += ["-i", str(OUT / s["file"])]
    delays = ";".join(
        "[%d:a]adelay=%d:all=1[a%d]" % (i, s["abs"], i) for i, s in enumerate(spoken))
    merge = "".join("[a%d]" % i for i in range(len(spoken)))
    args += [
        "-filter_complex",
        "%s;%samix=inputs=%d:normalize=0[out]" % (delays, merge, len(spoken)),
        "-map", "[out]", "-t", "%.3f" % (total_ms / 1000.0),
        "-ar", str(RATE), "-ac", "1",
        str(OUT / "voice.wav"),
    ]
    subprocess.run(args, capture_output=True, check=True)
    print("\nlaid out on the film clock: %s" % (OUT / "voice.wav"))


if __name__ == "__main__":
    main()
