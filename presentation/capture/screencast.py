#!/usr/bin/env python3
"""Record the film from a headless browser, without touching the screen.

    python presentation/capture/screencast.py [--seconds N] [--out picture.mp4]

# Why this exists

The first film was captured by recording the desktop, because grabbing the
browser window by its title returns black frames: Chrome hands the window to the
compositor rather than to its device context. Recording the desktop works, and it
costs the screen for the length of the film. Two takes were lost that way, one to
a click that brought another window to the front.

Headless Chrome renders the film correctly, WebAssembly included. It just has no
window to grab. The frames have to be asked for over the DevTools protocol, and
that protocol only speaks WebSocket, which the standard library does not.

So there is a WebSocket client in here. It is about eighty lines and it only has
to do one job well, which is why writing it was cheaper than taking on a
dependency for it.

# Why not virtual time

Chrome can fast-forward a page with --virtual-time-budget and screenshot the
result, which is genuinely frame-accurate. But each shot starts the page again
from zero, so rendering frame N costs N milliseconds of film. Over eight thousand
frames that is quadratic and hopeless. The screencast plays once, in real time,
and every frame it emits is a frame the page actually drew.

# Frames arrive when the picture changes

Screencast is event-driven: a still passage emits nothing. So the timestamps
matter as much as the images, and the frames are laid back onto a constant rate
afterwards using them, rather than being assumed to be evenly spaced.
"""

import argparse
import base64
import json
import os
import re
import shutil
import socket
import struct
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
FILM = ROOT / "film"


# --- the smallest WebSocket client that can do this job ---------------------

class WS:
    """RFC 6455, client side, text frames only.

    Everything this omits is something the DevTools protocol never sends:
    there are no continuation frames to reassemble because Chrome does not
    fragment, and no compression because it is not negotiated.
    """

    def __init__(self, url):
        m = re.match(r"ws://([^:/]+):(\d+)(/.*)", url)
        if not m:
            raise ValueError("not a ws:// url: %s" % url)
        host, port, path = m.group(1), int(m.group(2)), m.group(3)
        self.sock = socket.create_connection((host, port), timeout=30)
        key = base64.b64encode(os.urandom(16)).decode()
        self.sock.sendall((
            "GET %s HTTP/1.1\r\n"
            "Host: %s:%d\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            "Sec-WebSocket-Key: %s\r\n"
            "Sec-WebSocket-Version: 13\r\n\r\n" % (path, host, port, key)).encode())
        self.f = self.sock.makefile("rb")
        status = self.f.readline()
        if b"101" not in status:
            raise RuntimeError("handshake refused: %s" % status)
        while self.f.readline().strip():
            pass
        self.next_id = 0

    def send(self, method, **params):
        self.next_id += 1
        self._frame(json.dumps({"id": self.next_id, "method": method, "params": params}))
        return self.next_id

    def call(self, method, **params):
        """Send and wait for the answer, discarding events that arrive meanwhile.

        Needed because these are not independent: the screencast has to start
        after the viewport is pinned, and firing both without waiting captured a
        first take at 1920x993, which is the size the headless window happened to
        settle on before the override took effect.
        """
        want = self.send(method, **params)
        while True:
            msg = self.recv()
            if msg.get("id") == want:
                if "error" in msg:
                    raise RuntimeError("%s: %s" % (method, msg["error"]))
                return msg.get("result", {})

    def _frame(self, text, opcode=0x1):
        data = text.encode()
        head = bytearray([0x80 | opcode])
        mask = os.urandom(4)
        n = len(data)
        if n < 126:
            head.append(0x80 | n)
        elif n < 65536:
            head.append(0x80 | 126)
            head += struct.pack(">H", n)
        else:
            head.append(0x80 | 127)
            head += struct.pack(">Q", n)
        head += mask
        self.sock.sendall(bytes(head) + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def _exact(self, n):
        buf = self.f.read(n)
        if buf is None or len(buf) < n:
            raise ConnectionError("socket closed mid-frame")
        return buf

    def recv(self):
        """One message, as a dict. Answers pings and skips anything else."""
        while True:
            b0, b1 = self._exact(2)
            opcode = b0 & 0x0F
            n = b1 & 0x7F
            if n == 126:
                n = struct.unpack(">H", self._exact(2))[0]
            elif n == 127:
                n = struct.unpack(">Q", self._exact(8))[0]
            payload = self._exact(n) if n else b""
            if opcode == 0x9:                      # ping, answer it and carry on
                self._frame(payload.decode("utf-8", "replace"), opcode=0xA)
                continue
            if opcode == 0x8:                      # close
                raise ConnectionError("browser closed the connection")
            if opcode != 0x1:
                continue
            return json.loads(payload)

    def close(self):
        try:
            self._frame("", opcode=0x8)
        except OSError:
            pass
        self.sock.close()


# --- the browser ------------------------------------------------------------

def chrome_path():
    for p in (os.environ.get("CHROME"),
              r"C:\Program Files\Google\Chrome\Application\chrome.exe",
              r"C:\Program Files (x86)\Google\Chrome\Application\chrome.exe",
              os.path.expandvars(r"%LOCALAPPDATA%\Google\Chrome\Application\chrome.exe"),
              shutil.which("google-chrome"), shutil.which("chromium")):
        if p and Path(p).exists():
            return p
    sys.exit("no Chrome found; set CHROME to its path")


def film_seconds():
    """How long the film runs, read from the film rather than restated here."""
    js = (FILM / "film.js").read_text(encoding="utf-8")
    cover, reveal = re.search(r"COVER = (\d+), REVEAL = (\d+)", js).groups()
    gap = int(cover) + 20 + int(reveal) + 30
    beats = re.findall(r"dur:\s*(\d+)(,\s*keep:\s*true)?",
                       (FILM / "scenes.js").read_text(encoding="utf-8"))
    total = 0
    for i, (dur, _) in enumerate(beats):
        nxt = i + 1 < len(beats) and bool(beats[i + 1][1])
        total += int(dur) + (0 if nxt else gap)
    return total / 1000.0


def page_target(port, tries=40):
    for _ in range(tries):
        try:
            with urllib.request.urlopen("http://127.0.0.1:%d/json/list" % port, timeout=2) as r:
                for t in json.load(r):
                    if t.get("type") == "page" and t.get("webSocketDebuggerUrl"):
                        return t["webSocketDebuggerUrl"]
        except Exception:
            pass
        time.sleep(0.5)
    sys.exit("the browser never offered a page to attach to")


def record(url, seconds, frames_dir, port, width, height):
    frames_dir.mkdir(parents=True, exist_ok=True)
    for old in frames_dir.glob("*.png"):
        old.unlink()

    profile = HERE / "work" / "headless-profile"
    shutil.rmtree(profile, ignore_errors=True)
    proc = subprocess.Popen([
        chrome_path(), "--headless=new", "--disable-gpu", "--hide-scrollbars",
        "--mute-audio", "--no-first-run", "--no-default-browser-check",
        "--user-data-dir=%s" % profile,
        "--remote-debugging-port=%d" % port,
        "--window-size=%d,%d" % (width, height),
        url,
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    ws = WS(page_target(port))
    ws.call("Page.enable")
    # Pin the viewport and wait for it, or the frames come back at whatever size
    # the headless window settled on: the first take was 1920x993, with the film
    # letterboxed inside it.
    ws.call("Emulation.setDeviceMetricsOverride",
            width=width, height=height, deviceScaleFactor=1, mobile=False)
    # and reload, so the film lays itself out at the size it will be recorded at
    ws.call("Page.reload", ignoreCache=True)
    time.sleep(2.5)
    ws.call("Page.startScreencast", format="png", everyNthFrame=1,
            maxWidth=width, maxHeight=height)

    stamps, n, first = [], 0, None
    started = time.time()
    print("recording %.1fs of film, headless" % seconds)
    try:
        while time.time() - started < seconds + 2:
            msg = ws.recv()
            if msg.get("method") != "Page.screencastFrame":
                continue
            p = msg["params"]
            ws.send("Page.screencastFrameAck", sessionId=p["sessionId"])
            t = p["metadata"].get("timestamp")
            if t is None:
                continue
            if first is None:
                first = t
            at = t - first
            if at > seconds:
                break
            (frames_dir / ("%06d.png" % n)).write_bytes(base64.b64decode(p["data"]))
            stamps.append(at)
            n += 1
            if n % 300 == 0:
                print("  %5d frames, %6.1fs of film" % (n, at))
    except ConnectionError as e:
        print("stopped early: %s" % e)
    finally:
        try:
            ws.send("Page.stopScreencast")
            ws.close()
        except Exception:
            pass
        proc.terminate()
        shutil.rmtree(profile, ignore_errors=True)

    print("%d frames over %.1fs, %.1f a second on average" %
          (n, stamps[-1] if stamps else 0, n / max(stamps[-1], 0.001) if stamps else 0))
    return stamps


def to_video(frames_dir, stamps, out, fps, seconds):
    """Lay the frames back onto a constant rate using the times they arrived.

    Screencast only emits on change, so a still passage is one frame with a long
    life. The concat demuxer takes exactly that: a file and how long it is on
    screen for.
    """
    if not stamps:
        sys.exit("no frames were captured")
    lines = []
    for i, at in enumerate(stamps):
        nxt = stamps[i + 1] if i + 1 < len(stamps) else seconds
        lines.append("file '%06d.png'\nduration %.4f" % (i, max(nxt - at, 1.0 / 240)))
    lines.append("file '%06d.png'" % (len(stamps) - 1))   # concat needs the last one twice
    (frames_dir / "frames.txt").write_text("\n".join(lines), encoding="utf-8")

    done = subprocess.run([
        "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
        "-f", "concat", "-safe", "0", "-i", "frames.txt",
        "-vsync", "cfr", "-r", str(fps), "-t", "%.3f" % seconds,
        "-c:v", "libx264", "-preset", "medium", "-crf", "14",
        "-pix_fmt", "yuv420p", str(out),
    ], cwd=frames_dir, capture_output=True, text=True)
    if done.returncode != 0:
        sys.exit("ffmpeg failed:" + chr(10) + done.stderr[-1500:])
    print("wrote %s" % out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seconds", type=float, default=None)
    ap.add_argument("--out", default=str(HERE / "picture.mp4"))
    ap.add_argument("--port", type=int, default=9333)
    ap.add_argument("--serve", type=int, default=8802)
    ap.add_argument("--film", default="film")
    ap.add_argument("--fps", type=int, default=30)
    ap.add_argument("--width", type=int, default=1920)
    ap.add_argument("--height", type=int, default=1080)
    args = ap.parse_args()

    seconds = args.seconds if args.seconds else film_seconds()
    server = subprocess.Popen(
        [sys.executable, "-m", "http.server", str(args.serve), "--directory", str(ROOT)],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    time.sleep(1.5)
    try:
        url = "http://localhost:%d/%s/?mode=film" % (args.serve, args.film)
        frames = HERE / "work" / "frames"
        stamps = record(url, seconds, frames, args.port, args.width, args.height)
        to_video(frames, stamps, Path(args.out), args.fps, seconds)
    finally:
        server.terminate()


if __name__ == "__main__":
    main()
