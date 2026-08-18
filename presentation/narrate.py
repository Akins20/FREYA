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


# Where a key has been found before. The repository's own .env is the first
# place to look; the rest are sibling projects on the same machine, which is
# where it lives when this is run from a checkout that has no .env of its own.
ENVS = [
    HERE.parent / ".env",
    HERE.parent.parent / "anima" / ".env",
    HERE.parent.parent / "GeminiChat" / "server" / ".env",
]


def api_key():
    key = os.environ.get("GEMINI_API_KEY")
    if key:
        return key
    for env in ENVS:
        if not env.exists():
            continue
        for line in env.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if line.startswith("GEMINI_API_KEY="):
                found = line.split("=", 1)[1].strip().strip('"').strip("'")
                if found:
                    return found
    sys.exit("No GEMINI_API_KEY in the environment or in " +
             ", ".join(str(e) for e in ENVS))


def lines():
    """Read the narration out of narration.js, which is the page's own copy.

    Parsed rather than duplicated, so the voice track and the subtitles cannot
    say different things.
    """
    src = (FILM / "narration.js").read_text(encoding="utf-8")
    body = src[src.index("const NARRATION = ") + len("const NARRATION = "):]
    body = body[: body.rindex("]") + 1]
    body = re.sub(r"/\*.*?\*/", "", body, flags=re.S)   # the movement headings
    # JavaScript object literals use bare keys and allow a trailing comma; JSON
    # allows neither, so both are normalised before parsing.
    body = re.sub(r"([{,]\s*)(\w+):", r'\1"\2":', body)
    body = re.sub(r",(\s*[\]}])", r"\1", body)
    return json.loads(body)


def say(key, text):
    """One synthesis call. The whole script goes through here in one piece."""
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
    with urllib.request.urlopen(req, timeout=300) as r:
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


def pauses(path):
    """Every pause in the reading, as (start, end) seconds."""
    out = subprocess.run(
        ["ffmpeg", "-hide_banner", "-nostats", "-i", str(path),
         "-af", "silencedetect=noise=-32dB:d=0.15", "-f", "null", "-"],
        capture_output=True, text=True).stderr
    found, start = [], None
    for line in out.splitlines():
        m = re.search(r"silence_start: ([\d.]+)", line)
        if m:
            start = float(m.group(1))
        m = re.search(r"silence_end: ([\d.]+)", line)
        if m and start is not None:
            found.append((start, float(m.group(1))))
            start = None
    return found


def cut_points(path, said, length):
    """Where one continuous reading divides into the lines that were written.

    # Why this is not just the longest silences

    It was, and it put twenty three seconds of audio against a sixteen word
    sentence. A reader pauses inside a long sentence as readily as between two of
    them, so the biggest gaps are not reliably the ones between lines.

    What is reliable is that a take is read at a roughly even pace, so each line's
    share of the words predicts where it should end. The job is then to choose
    which of the candidate pauses best fits that prediction, which is a small
    dynamic program.

    # And why the prediction alone is not enough

    Minimising the error against the prediction, and nothing else, will happily
    put four breaks within half a second of each other at the end of the take,
    because by then every candidate is close to the answer. So a segment is only
    allowed if its length is credible for the number of words in it. That single
    constraint is what turned a nonsense split into a correct one.
    """
    gaps = pauses(path)
    words = [len(l["text"].split()) for l in said]
    k = len(said) - 1
    if len(gaps) < k:
        sys.exit("only %d pauses in the reading and %d breaks are needed" % (len(gaps), k))

    # Time is spoken words plus the pauses between lines, so the pauses have to
    # come out of the budget before the words are spread across what is left.
    pause = sorted((b - a for a, b in gaps), reverse=True)[:k]
    hold = sum(pause) / len(pause)
    speech = max(1.0, length - hold * k)

    total = float(sum(words))
    want, run = [], 0.0
    for i, w in enumerate(words[:-1]):
        run += w
        want.append(run / total * speech + (i + 1) * hold)

    cand = sorted((a + b) / 2 for a, b in gaps)
    ends = cand + [length]

    # A segment is credible only if its pace is somewhere a person could read at.
    # The upper bound carries a fixed allowance as well as a rate, because a
    # segment holds the pause after it and the last one holds the tail of the file.
    FAST, SLOW, SLACK = 5.5, 1.6, 3.0
    def ok(i, a, b):
        span = b - a
        return words[i] / FAST <= span <= words[i] / SLOW + SLACK

    INF = float("inf")
    n = len(cand)
    best = [[INF] * k for _ in range(n)]
    back = [[-1] * k for _ in range(n)]
    for i in range(n):
        if ok(0, 0.0, cand[i]):
            best[i][0] = (cand[i] - want[0]) ** 2
    for j in range(1, k):
        for i in range(j, n):
            for h in range(j - 1, i):
                if best[h][j - 1] == INF or not ok(j, cand[h], cand[i]):
                    continue
                v = best[h][j - 1] + (cand[i] - want[j]) ** 2
                if v < best[i][j]:
                    best[i][j] = v
                    back[i][j] = h
    finish = [i for i in range(n) if best[i][k - 1] < INF and ok(k, cand[i], length)]
    if not finish:
        sys.exit("no split of the reading is credible for the script it was read from")
    end = min(finish, key=lambda i: best[i][k - 1])

    picked, j = [], k - 1
    while j >= 0:
        picked.append(cand[end])
        end = back[end][j]
        j -= 1
    picked.reverse()
    return [0.0] + picked + [length]


def trim(pcm, frame):
    """Take the silence off the front and back of one line."""
    step = frame // 50                                  # twenty millisecond steps
    def quiet(chunk):
        peak = 0
        for i in range(0, len(chunk) - 1, 32):
            v = int.from_bytes(chunk[i:i + 2], "little", signed=True)
            peak = max(peak, abs(v))
        return peak < 500
    a, b = 0, len(pcm)
    while a + step < b and quiet(pcm[a:a + step]):
        a += step
    while b - step > a and quiet(pcm[b - step:b]):
        b -= step
    return pcm[max(0, a - step):min(len(pcm), b + step)]


def scene_starts(scenes_js):
    """Absolute start time of each scene, so the capture can lay the audio out.

    Two things decide this and both are read from the source rather than restated
    here, because a number restated in two files is a number that will eventually
    disagree with itself: the durations live beside the scenes in scenes.js, and
    the length of a dissolve belongs to film.js.

    A scene marked `keep` is the next beat of the same shot, so nothing dissolves
    into it and there is no gap in front of it. Getting that wrong would push the
    whole voice track later and later against the picture.
    """
    scenes = re.findall(r"dur:\s*(\d+)(,\s*keep:\s*true)?", scenes_js)
    film_js = (FILM / "film.js").read_text(encoding="utf-8")
    cover, reveal = re.search(r"COVER = (\d+), REVEAL = (\d+)", film_js).groups()
    gap = int(cover) + 20 + int(reveal) + 30

    starts, t = [], 0
    for i, (dur, _) in enumerate(scenes):
        starts.append(t)
        nxt_keeps = i + 1 < len(scenes) and bool(scenes[i + 1][1])
        t += int(dur) + (0 if nxt_keeps else gap)
    return starts, t


def main():
    """Synthesise the whole script in one call, then cut it into lines.

    # Why one call

    Every line used to be its own request, and every request is its own
    generation, so the reader's pitch, pace and apparent age wandered from scene
    to scene: it sounded like several people had narrated the film between them.
    Within a single generation the voice holds, so the whole script is read in one
    take and the take is then cut at the pauses, which is how it would be done
    with a person in a booth.
    """
    key = api_key()
    OUT.mkdir(exist_ok=True)
    said = lines()

    # Paragraph breaks are what the reader turns into pauses, and the pauses are
    # what the cut is found by, so the joining string is load-bearing.
    script = ("\n\n").join(l["text"] for l in said)
    print("reading %d lines, %d words, in one take" %
          (len(said), len(script.split())))

    frame = RATE * CHANNELS * WIDTH
    # The take arrives with silence at both ends. Left on, the tail alone is
    # longer than the closing line is allowed to be, and no split of the reading
    # is then credible.
    pcm = trim(say(key, script), frame)
    whole = OUT / "whole.wav"
    length = write_wav(whole, pcm)
    print("%.1fs of audio" % length)

    cuts = cut_points(whole, said, length)

    starts, total = scene_starts((FILM / "scenes.js").read_text(encoding="utf-8"))
    out = []
    for i, l in enumerate(said):
        a, b = cuts[i], cuts[i + 1]
        name = "%03d.wav" % i
        dur = write_wav(OUT / name, trim(pcm[int(a * frame):int(b * frame)], frame))
        out.append({"index": i, "scene": l["scene"], "at": l["at"],
                    "abs": starts[l["scene"]] + l["at"], "file": name,
                    "dur": round(dur, 3), "text": l["text"]})
        print("%2d  %5.2fs  %s" % (i, dur, l["text"][:60]))

    (OUT / "manifest.json").write_text(
        json.dumps({"total": total, "lines": out}, indent=2), encoding="utf-8")
    print("\nnow retiming the film to what was actually said")
    retime()


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
        # apad, because amix ends with the last line and the picture does not.
        # Without it the mux trims the film to the final word and the closing card
        # never gets its hold.
        "-filter_complex",
        "%s;%samix=inputs=%d:normalize=0,apad[out]" % (delays, merge, len(spoken)),
        "-map", "[out]", "-t", "%.3f" % (total_ms / 1000.0),
        "-ar", str(RATE), "-ac", "1",
        str(OUT / "voice.wav"),
    ]
    subprocess.run(args, capture_output=True, check=True)
    print("\nlaid out on the film clock: %s" % (OUT / "voice.wav"))




# --- retiming ---------------------------------------------------------------
#
# The timings in narration.js are written by hand against a guess at how fast a
# line will be read, and the guess is always wrong, because a synthesiser has its
# own pace and it is not the one you had in your head. So once the audio exists,
# the measured durations replace the guess: every line after the first in a scene
# moves to where the previous one actually finished, and every scene is stretched
# to hold what is said over it.
#
#     python presentation/narrate.py retime
#
# It rewrites narration.js and the durations in scenes.js and lays the track out
# again. Nothing is re-synthesised.

BEAT = 700          # the pause between two lines in the same scene
TAIL = 1300         # how long the picture holds after the last word

# What each scene needs on its own, before anything is said over it.
FLOOR = [10000, 9500, 9500, 12500, 9000, 10000, 13500, 12500, 9500, 10000,
         13500, 5500, 15000, 15000, 11500, 10000, 11000, 8500]


def retime():
    spoken = {l["index"]: l["dur"] * 1000
              for l in json.loads((OUT / "manifest.json").read_text(encoding="utf-8"))["lines"]}
    said = lines()

    by_scene = {}
    for i, l in enumerate(said):
        by_scene.setdefault(l["scene"], []).append((i, l))

    moves, need = {}, {}
    for scene, items in by_scene.items():
        at = items[0][1]["at"]
        for i, _ in items:
            moves[i] = int(round(at / 100.0)) * 100
            at = moves[i] + spoken[i] + BEAT
        need[scene] = at - BEAT + TAIL

    # Each line is rewritten by matching its own text, which is unique, so a line
    # that has not moved is left exactly as it was.
    src = (FILM / "narration.js").read_text(encoding="utf-8")
    for i, l in enumerate(said):
        src = re.sub(
            r"\{ scene: %d, at:\s*%d, text: \"%s\" \}" % (l["scene"], l["at"], re.escape(l["text"])),
            '{ scene: %d, at: %d, text: "%s" }' % (l["scene"], moves[i], l["text"]),
            src, count=1)
    (FILM / "narration.js").write_text(src, encoding="utf-8")

    scenes = (FILM / "scenes.js").read_text(encoding="utf-8")
    ids = re.findall(r'id: "([^"]+)", dur:', scenes)
    cur = [int(d) for d in re.findall(r"dur:\s*(\d+)", scenes)]
    for i, name in enumerate(ids):
        want = max(FLOOR[i], int(round(need.get(i, 0) / 500.0)) * 500)
        if want != cur[i]:
            scenes = re.sub(r'(id: "%s", dur: )%d' % (re.escape(name), cur[i]),
                            r"\g<1>%d" % want, scenes, count=1)
        print("%2d  %-24s %6d -> %6d" % (i, name, cur[i], want))
    (FILM / "scenes.js").write_text(scenes, encoding="utf-8")

    starts, total = scene_starts(scenes)
    out = [{"index": i, "scene": l["scene"], "at": moves[i],
            "abs": starts[l["scene"]] + moves[i], "file": "%03d.wav" % i,
            "dur": round(spoken[i] / 1000.0, 3), "text": l["text"]}
           for i, l in enumerate(said)]
    (OUT / "manifest.json").write_text(
        json.dumps({"total": total, "lines": out}, indent=2), encoding="utf-8")
    mix(out, total)
    print("\nfilm runs %d:%02d" % (total // 60000, (total // 1000) % 60))


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "retime":
        retime()
    else:
        main()
