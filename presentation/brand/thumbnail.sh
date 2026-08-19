#!/usr/bin/env bash
# Render the YouTube thumbnail to a PNG.
#
#   presentation/brand/thumbnail.sh
#
# Headless Chrome, because the page is static: there is no animation to capture,
# so none of the compositing problems that forced the film itself onto a screen
# recording apply here.
set -euo pipefail
cd "$(dirname "$0")"
CHROME="${CHROME:-/c/Program Files/Google/Chrome/Application/chrome.exe}"
PORT="${PORT:-8791}"
python -m http.server "$PORT" --directory .. >/dev/null 2>&1 &
SERVER=$!
trap 'kill $SERVER 2>/dev/null || true' EXIT
sleep 1
"$CHROME" --headless --disable-gpu --hide-scrollbars \
    --screenshot="$PWD/freya-thumbnail.png" --window-size=1280,720 \
    "http://localhost:$PORT/brand/thumbnail.html" >/dev/null 2>&1
echo "wrote freya-thumbnail.png"
