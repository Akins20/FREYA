#!/usr/bin/env python3
"""Run Freya against a stratified sample of real WebVoyager tasks.

Deliberately samples every site, including the ones that fight automation, so
the bot-wall rate is measured rather than avoided by picking friendly sites.
Three outcomes are recorded separately because they mean different things:

  reached   the browser actually loaded the site (objective)
  answered  she came back with something concrete rather than "I couldn't"
  blocked   a bot wall, captcha or hard refusal was hit

Correctness is judged afterwards, separately, and is the weakest of the numbers.
"""
import json, random, shutil, subprocess, sys, tempfile, time, os, re

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = "/run/media/akins/Akins Drive1/Development/JARVIS"
BIN = os.path.join(REPO, "bin", "freya")
PER_SITE = int(sys.argv[1]) if len(sys.argv) > 1 else 2
TIMEOUT = 300

tasks = [json.loads(l) for l in open(os.path.join(HERE, "wv.jsonl"))]
by_site = {}
for t in tasks:
    by_site.setdefault(t["web_name"], []).append(t)

rng = random.Random(20260810)  # fixed, so the sample is reproducible
sample = []
for site in sorted(by_site):
    sample += rng.sample(by_site[site], min(PER_SITE, len(by_site[site])))

BLOCK = re.compile(r"(?i)captcha|are you a robot|verify you are human|access denied|"
                   r"blocked|cloudflare|unusual traffic|bot detect|forbidden|403")
GAVE_UP = re.compile(r"(?i)^(i (?:couldn't|could not|was unable|can't|cannot)|"
                     r"unfortunately[, ]|sorry[, ])")

out = open(os.path.join(HERE, "wv-results.jsonl"), "w")
print(f"{len(sample)} tasks, {PER_SITE} per site", flush=True)

for i, t in enumerate(sample, 1):
    prompt = f"{t['ques']}\n\nStart at {t['web']}"
    env = dict(os.environ)
    # A fresh memory directory per task. Sharing her live one let an earlier
    # task's answer sit in the working history, and the second Allrecipes task
    # came back in six seconds without opening a browser — recalled, not
    # researched. Her own bench harness isolates the data dir for exactly this
    # reason; a benchmark where task N can be answered from task N-1 measures
    # nothing.
    env["FREYA_DATA_DIR"] = tempfile.mkdtemp(prefix="wvbench-")
    env["FREYA_WAKE"] = "off"
    t0 = time.time()
    try:
        p = subprocess.run([BIN, "-yes", "-v", "-ask", prompt], env=env,
                           cwd=REPO, capture_output=True, text=True, timeout=TIMEOUT)
        raw = p.stdout + p.stderr
    except subprocess.TimeoutExpired as e:
        raw = ((e.stdout or b"").decode("utf8", "replace") +
               (e.stderr or b"").decode("utf8", "replace"))
        raw += "\n[TIMED OUT]"
    dur = time.time() - t0

    # The reply is what is left after the trace lines the -v flag prints.
    lines = [l for l in raw.split("\n")
             if not l.startswith(("  →", "  ✓", "  ✗", "  💭", "  context:", "  tools:"))
             and "daemon is running" not in l]
    reply = "\n".join(lines).strip()
    tools = ""
    for l in raw.split("\n"):
        if l.startswith("  tools:"):
            tools = l.split(":", 1)[1].strip()

    rec = {
        "id": t["id"], "site": t["web_name"], "q": t["ques"], "url": t["web"],
        "reply": reply[-4000:], "tools": tools, "seconds": round(dur, 1),
        "reached": "browser_open" in tools or "browser_read" in tools,
        "blocked": bool(BLOCK.search(reply)),
        "gave_up": bool(GAVE_UP.search(reply)) or "[TIMED OUT]" in raw,
    }
    shutil.rmtree(env["FREYA_DATA_DIR"], ignore_errors=True)
    out.write(json.dumps(rec) + "\n")
    out.flush()
    flag = "BLOCKED" if rec["blocked"] else ("gave-up" if rec["gave_up"] else "answered")
    print(f"  [{i:2d}/{len(sample)}] {t['web_name']:22} {flag:9} {dur:5.0f}s", flush=True)

out.close()
print("done", flush=True)
