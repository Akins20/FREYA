#!/usr/bin/env python3
"""Run Freya against a sample of real Web Bench tasks.

READ tasks only, and that is a deliberate limit rather than a convenience. The
write half of Web Bench (CREATE, UPDATE, DELETE, FILE_MANIPULATION — 1,006 of
2,647 tasks) asks an agent to register accounts, build wishlists, change profile
settings and delete records on 449 real third-party websites. No benchmark number
justifies doing that to live services under someone else's name, so it is not run
and the resulting figure is stated as read-only.

That also means this number is NOT comparable to the published Web Bench
headline, which is an all-tasks figure.
"""
import csv, json, random, shutil, subprocess, sys, tempfile, time, os, re

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = "/run/media/akins/Akins Drive1/Development/JARVIS"
BIN = os.path.join(REPO, "bin", "freya")
N = int(sys.argv[1]) if len(sys.argv) > 1 else 20
TIMEOUT = 300

rows = [r for r in csv.DictReader(open(os.path.join(HERE, "webbench.csv")))
        if r["Category"] == "READ"]
rng = random.Random(20260810)
# One task per site at most, so the sample spans the long tail rather than
# hammering whichever site happens to have the most entries.
seen, pool = set(), []
for r in rng.sample(rows, len(rows)):
    host = r["Starting URL"].split("/")[2]
    if host in seen:
        continue
    seen.add(host)
    pool.append(r)
sample = pool[:N]

BLOCK = re.compile(r"(?i)captcha|are you a robot|verify you are human|access denied|"
                   r"blocked|cloudflare|unusual traffic|bot detect|forbidden|403")
GAVE_UP = re.compile(r"(?i)^(i (?:couldn't|could not|was unable|can't|cannot)|"
                     r"unfortunately[, ]|sorry[, ])")

out = open(os.path.join(HERE, "wb-results.jsonl"), "w")
print(f"{len(sample)} Web Bench READ tasks, one per site", flush=True)

for i, t in enumerate(sample, 1):
    prompt = f"{t['Task']}\n\nStart at {t['Starting URL']}"
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
               (e.stderr or b"").decode("utf8", "replace")) + "\n[TIMED OUT]"
    dur = time.time() - t0

    lines = [l for l in raw.split("\n")
             if not l.startswith(("  →", "  ✓", "  ✗", "  💭", "  context:", "  tools:"))
             and "daemon is running" not in l]
    reply = "\n".join(lines).strip()
    tools = next((l.split(":", 1)[1].strip() for l in raw.split("\n")
                  if l.startswith("  tools:")), "")

    rec = {
        "id": t["ID"], "site": t["Starting URL"].split("/")[2], "q": t["Task"],
        "url": t["Starting URL"], "reply": reply[-4000:], "tools": tools,
        "seconds": round(dur, 1),
        "reached": "browser_open" in tools or "browser_read" in tools,
        "blocked": bool(BLOCK.search(reply)),
        "gave_up": bool(GAVE_UP.search(reply)) or "[TIMED OUT]" in raw,
    }
    # Read the cost out before the directory goes. Isolating memory per task is
    # what makes the benchmark honest; deleting the directory afterwards is what
    # made its cost invisible, so every figure for this run had to be estimated
    # from a sample. Both can be true at once — take the number, then delete.
    spent = 0.0
    try:
        with open(os.path.join(env["FREYA_DATA_DIR"], "telemetry.jsonl")) as tf:
            for tl in tf:
                try:
                    te = json.loads(tl)
                except Exception:
                    continue
                if te.get("kind") == "model":
                    spent += te.get("cost", 0) or 0
    except OSError:
        pass
    rec["cost"] = round(spent, 4)
    shutil.rmtree(env["FREYA_DATA_DIR"], ignore_errors=True)
    out.write(json.dumps(rec) + "\n")
    out.flush()
    flag = "BLOCKED" if rec["blocked"] else ("gave-up" if rec["gave_up"] else "answered")
    print(f"  [{i:2d}/{len(sample)}] {rec['site'][:26]:26} {flag:9} {dur:5.0f}s", flush=True)

out.close()
print("done", flush=True)
