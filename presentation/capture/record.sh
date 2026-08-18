#!/usr/bin/env bash
# Record the film to a video file.
#
#   presentation/capture/record.sh [out.mp4]
#
# The page is played in film mode, which runs the scenes back to back on a fixed
# clock with the keyboard hints hidden. Nothing here drives the page: it plays
# itself, and this only points a recorder at it and then lays the narration on
# top.
#
# # Why a real browser window and not a headless screenshot loop
#
# Screenshotting frame by frame would mean stepping the clock by hand, and every
# transition in this film is time-based: the dissolve is a function of elapsed
# seconds, the grain walks with it, the dust drifts. Stepping that produces a film
# that is technically 60fps and looks wrong, because the motion no longer matches
# the timing the scenes were written against. Recording the window as it plays
# keeps them the same thing.
#
# Requires: ffmpeg, and a browser that can be told to open a window at a size.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$HERE")"
OUT="${1:-$HERE/freya.mp4}"
PORT="${PORT:-8712}"
W=1600
H=900
FPS=30

have() { command -v "$1" >/dev/null 2>&1; }

have ffmpeg || { echo "ffmpeg is not installed"; exit 1; }

# The film's length is the sum of the scene durations plus the dissolve between
# each pair, both read from the source rather than restated here.
DUR=$(python3 - "$ROOT" <<'PY'
import re, sys, pathlib
film = pathlib.Path(sys.argv[1]) / "film"
durs = [int(d) for d in re.findall(r"dur:\s*(\d+)", (film / "scenes.js").read_text(encoding="utf-8"))]
cover, reveal = re.search(r"COVER = (\d+), REVEAL = (\d+)", (film / "film.js").read_text(encoding="utf-8")).groups()
gap = int(cover) + 20 + int(reveal) + 30
print("%.2f" % ((sum(durs) + gap * (len(durs) - 1)) / 1000.0 + 1.0))
PY
)
echo "film runs ${DUR}s"

python3 -m http.server "$PORT" --directory "$ROOT" >/dev/null 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true' EXIT
sleep 1

URL="http://localhost:$PORT/film/?mode=film"
for b in google-chrome chromium chromium-browser brave-browser; do
  if have "$b"; then BROWSER="$b"; break; fi
done
: "${BROWSER:?no Chromium-family browser found}"

PROFILE="$(mktemp -d)"
"$BROWSER" \
  --user-data-dir="$PROFILE" \
  --no-first-run --no-default-browser-check \
  --window-size=$W,$H --window-position=0,0 \
  --app="$URL" >/dev/null 2>&1 &
CHROME=$!
trap 'kill $SERVER $CHROME 2>/dev/null || true; rm -rf "$PROFILE"' EXIT

# The wasm has to compile and the first scene has to start before recording does.
sleep 4

ffmpeg -y -f x11grab -framerate $FPS -video_size ${W}x${H} -i "${DISPLAY:-:0}+0,0" \
  -t "$DUR" -c:v libx264 -preset slow -crf 17 -pix_fmt yuv420p \
  "$HERE/picture.mp4"

VOICE="$ROOT/film/vo/voice.wav"
if [ -f "$VOICE" ]; then
  # The narration was already laid out on the film clock by narrate.py, so it
  # only has to be attached. -shortest guards against a trailing line running
  # past the last frame.
  ffmpeg -y -i "$HERE/picture.mp4" -i "$VOICE" \
    -c:v copy -c:a aac -b:a 192k -shortest "$OUT"
  rm -f "$HERE/picture.mp4"
  echo "wrote $OUT with narration"
else
  mv "$HERE/picture.mp4" "$OUT"
  echo "wrote $OUT without narration; run presentation/narrate.py first for the voice track"
fi
