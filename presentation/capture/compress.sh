#!/usr/bin/env bash
# Re-encode the film to a size limit, and measure what that cost.
#
#   presentation/capture/compress.sh [megabytes]
#
# # Why it starts from the raw grab
#
# The finished mp4 is already a lossy encode. Squeezing that again stacks one
# generation of loss on another, and the artefacts of the first pass become
# detail the second pass has to spend bits preserving. record.ps1 keeps the raw
# screen grab for exactly this reason, so every re-encode starts from the same
# near-lossless source.
#
# # Why two passes
#
# A size limit is a bitrate target, and a single pass has to guess how to spend
# the budget before it has seen the film. Two passes let it spend where the
# picture is difficult, which here means the dissolves, and coast through four
# minutes of near-static windows.
#
# # What it costs, in a number rather than an adjective
#
# The result is measured against the raw grab with VMAF, which is a perceptual
# score rather than a pixel difference. Above 95 is generally taken as
# indistinguishable in normal viewing, and above 93 as very hard to tell apart on
# a side-by-side. The score is printed rather than described, so the trade is
# visible instead of asserted.

set -euo pipefail
cd "$(dirname "$0")"

TARGET_MIB="${1:-46}"
RAW="work/raw.mkv"
VOICE="../film/vo/voice.wav"
OUT="freya-${TARGET_MIB}mb.mp4"

# The trim point and the running time come from the same places record.ps1 read
# them from, so this cannot drift away from the take it is re-encoding.
# Where the film actually starts in the raw grab: the frame after the slate
# flash ends. Override with SS= if a different take is being re-encoded.
SS="${SS:-10.066}"
DUR=$(python -c "
import re, pathlib
film = pathlib.Path('../film')
js = (film / 'film.js').read_text(encoding='utf-8')
cover, reveal = re.search(r'COVER = (\d+), REVEAL = (\d+)', js).groups()
gap = int(cover) + 20 + int(reveal) + 30
beats = re.findall(r'dur:\s*(\d+)(,\s*keep:\s*true)?', (film / 'scenes.js').read_text(encoding='utf-8'))
t = 0
for i, (d, _) in enumerate(beats):
    nxt = i + 1 < len(beats) and bool(beats[i + 1][1])
    t += int(d) + (0 if nxt else gap)
print('%.2f' % (t / 1000.0))
")

AUDIO_KBPS=80
VIDEO_KBPS=$(python -c "print(int($TARGET_MIB * 1048576 * 8 / $DUR / 1000) - $AUDIO_KBPS)")
echo "film runs ${DUR}s, target ${TARGET_MIB} MiB, video budget ${VIDEO_KBPS} kbps"

[ -f "$RAW" ] || { echo "no raw grab at $RAW. Run record.ps1 first."; exit 1; }

# aq-mode 3 puts bits into dark flat areas, which is most of this film and where
# banding would show first. A longer lookahead helps the two passes agree about
# where the dissolves are.
#
# Inputs come first and output options after, always. Putting the voice track
# after the encoding flags is what broke the first attempt: ffmpeg read -b:v as
# an option belonging to the wav.
ENCODE=(-c:v libx264 -preset slow -b:v "${VIDEO_KBPS}k"
        -x264-params "aq-mode=3:rc-lookahead=60:ref=5"
        -pix_fmt yuv420p -g 60 -keyint_min 60)

if [ -f work/x264-0.log ] && [ work/x264-0.log -nt "$RAW" ]; then
    echo "pass 1 already done, reusing work/x264-0.log"
else
    echo "pass 1 of 2"
    ffmpeg -hide_banner -loglevel error -y -ss "$SS" -i "$RAW" -t "$DUR"         "${ENCODE[@]}" -pass 1 -passlogfile work/x264 -an -f null -
fi

echo "pass 2 of 2"
ffmpeg -hide_banner -loglevel error -y -ss "$SS" -i "$RAW" -i "$VOICE" -t "$DUR"     -map 0:v:0 -map 1:a:0 "${ENCODE[@]}" -pass 2 -passlogfile work/x264     -c:a aac -b:a "${AUDIO_KBPS}k" -ac 1 -shortest -movflags +faststart "$OUT"

SIZE=$(python -c "import os; print('%.1f' % (os.path.getsize('$OUT') / 1048576))")
echo "wrote $OUT at ${SIZE} MiB"

# What the size cost, as a number rather than an adjective. VMAF is perceptual:
# above 95 is generally taken as indistinguishable in ordinary viewing.
echo "measuring against the raw grab"
ffmpeg -hide_banner -nostats -ss "$SS" -i "$RAW" -i "$OUT" -t "$DUR"     -lavfi "[0:v]setpts=PTS-STARTPTS[ref];[1:v]setpts=PTS-STARTPTS[dis];[dis][ref]libvmaf=n_threads=4"     -f null - 2>&1 | grep -i "VMAF score"
