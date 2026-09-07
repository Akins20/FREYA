# gemini-3.8-flash against gemini-3.5-flash-lite

Run 7 September 2026. The whole 70-benchmark suite, twice, same machine, same
flags (`-parallel 2`, one run each), a few hours apart. The only differences
between the two runs were the model and the tool-round ceiling.

## The numbers

| | gemini-3.5-flash-lite | gemini-3.8-flash |
|---|---|---|
| weighted pass-rate | **70%** | **20%** |
| benchmarks passed | 52/70 | 15/70 |
| ran out of rounds | 0/70 | 0/70 |
| runs that thrashed | 0/70 | 0/70 |
| wasted rounds | 52 | 15 |
| failed tool calls | 53 | 15 |
| suite duration | 18m40s | 30m06s |
| `maxToolRounds` | 60 | 120 |

Per category, passes out of total:

| category | 3.5-flash-lite | 3.8-flash |
|---|---|---|
| browser | 3/3 | 0/3 |
| browser-edge | 9/10 | 1/10 |
| conversion | 3/7 | 0/7 |
| data-processing | 5/7 | 0/7 |
| doc-generation | 7/7 | 5/7 |
| file-ops | 7/8 | 3/8 |
| memory | 4/8 | 1/8 |
| orchestration | 3/6 | **4/6** |
| persistence | 4/7 | 0/7 |
| terminal | 7/7 | 1/7 |

Orchestration was the only category that improved.

## What actually went wrong

The dominant failure was **"only 0 tool calls — did not drive the work"**, 28 of
them, plus five more that never reached a shell when the task required one. Note
that 3.8-flash logged *fewer* failed tool calls than the baseline, not more: it
was not failing at the work, it was not attempting it.

It is **not a wire-format problem**. 3.8-flash calls tools perfectly well. Asked
to read a CSV and sum a column it consulted a skill and read the file, same as
the older model.

The difference is what it does with a task. Asked to find the top three scores in
a CSV and write them to a file:

- **gemini-3.5-flash-lite** consulted the `shell` skill, tried
  `sort -t, -k2 -nr scores.csv | head -n 3 > top.txt && cat top.txt`, had it
  refused by the guard (unattended session, `&&` with a state-changing segment),
  adapted, used `file_write`, and **created the file**.
- **gemini-3.8-flash** read the CSV, listed the folder, ran `ls -la ..` with the
  reason "Check if git repo exists or environment details", read a path that did
  not exist, and **never wrote the file**.

It explores where the older model acts. That is the whole 50-point gap.

## What this means, and does not mean

**It is not a statement about the models in general.** It is a statement about
this agent. Her system prompt is 4,558 words of procedure — finish the work, do
not narrate, consult the skill first, anything you name you owe — tuned over
months against flash-lite, with each rule added because a measured failure
demanded it. A different model reads that same text differently.

So a newer model is not a free upgrade here. Moving up means re-tuning the prompt
and re-running this suite, not changing a string. Anyone tempted by the version
number should run `./bin/bench -parallel 2` first; it costs half an hour.

**One run each.** No pass-rate averaging, so small category differences are
noise. A fifty-point total gap across seventy benchmarks is not.

## What was kept from the attempt

- `maxToolRounds` doubled to 120. Unrelated to the model, and headroom rather
  than a fix — 0/70 hit the cap at 60 on either model.
- Pricing for `gemini-3.8-flash` and `gemini-3.7-flash` in
  `internal/telemetry/telemetry.go`. Both were falling through to the flash-lite
  default rate, so anyone who does set `FREYA_MODEL` to one would have had every
  cost understated by 2x to 5x.


---

# Addendum: the cached formula value, same day

The 70% baseline above had a dominant failure shape — thirteen of eighteen
failures were `a file matched workspace/*.xlsx but none contained all of: …`.
She was building the spreadsheet correctly and it read back with a blank where
the total belonged.

The cause was one line of reasoning in `internal/docs/rich.go`: a formula cell
was written as `<f>SUM(B2:B5)</f>` and nothing else, because "Excel and
LibreOffice recalculate on open, and a stale cached value shown before
recalculation is worse than none". True of a file edited over time; false of one
written here in a single pass from data already in hand.

Fixed by computing the value and writing `<f>SUM(B2:B5)</f><v>440</v>`, which is
what Excel itself writes. Same suite, same model, same flags:

| | before | after |
|---|---|---|
| weighted pass-rate | 70% | **80%** |
| benchmarks passed | 52/70 | **58/70** |
| suite duration | 18m40s | 15m35s |

| category | before | after |
|---|---|---|
| conversion | 3/7 | **6/7** |
| data-processing | 5/7 | **6/7** |
| persistence | 4/7 | **6/7** |
| orchestration | 3/6 | **4/6** |
| terminal | 7/7 | 6/7 |

Ten points from one bug. The terminal slip is a single run and within noise; the
four gains are not.

Worth noting against the model comparison above: this is what a real fix looks
like next to a version bump. The model change cost fifty points and half an hour
more runtime. Reading the failure text cost an afternoon and gained ten.
