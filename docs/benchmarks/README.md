# Benchmarks against the real datasets

Run 10 August 2026, stopped early on cost.

## What was run

**WebVoyager** — the actual 643-task dataset (`MinorJerry/WebVoyager`), sampled
two tasks per site across all fifteen sites with a fixed seed. The bot-walled
sites were deliberately left in rather than swapped for friendly ones, so the
block rate would be measured instead of avoided.

**15 of the 30 tasks completed** before the run was stopped. Everything below is
that partial sample.

| | |
|---|---|
| reached the site | 15/15 |
| hit a bot wall | 0/15 |
| gave up or timed out | 0/15 |
| came back with a concrete answer | 15/15 |
| median duration | 64s |

Includes Amazon 2/2 and Booking 2/2, which was the opposite of the prediction
made before the run.

## What this is NOT

**It is not a WebVoyager success rate**, and it must not be compared to the
published leaderboard (Browser Use 89.1%, Skyvern 2.0 85.85%). Those are
correctness figures. This measures reaching the site and answering, which is a
weaker claim and a different one.

WebVoyager ships no gold answers; the official evaluation is GPT-4V as judge. An
LLM judge scores 0.54–0.65 AUROC on exactly this kind of question (arXiv
2606.09863), so quoting one here would be dressing up a coin flip. Instead one
task was verified by hand against the live page:

> *Check the storage options and prices for the latest iPad Pro models.*
> Her answer: 256GB $1,199 · 512GB $1,399 · 1TB $1,799 · 2TB $2,299, in 11-inch
> and 13-inch. Apple's page: identical, all four tiers, both sizes.

One verified answer is one verified answer. The other fourteen are unverified.

## A methodology bug worth recording

The first run was thrown away. Task 1 returned in six seconds without opening a
browser: the runs shared her live `FREYA_DATA_DIR`, so an earlier task's answer
was sitting in her working history and she recalled it instead of researching it.
Each task now gets a fresh data directory, which is what `internal/bench` already
does. A benchmark where task N can be answered from task N-1 measures nothing.

The fix created a second problem. Deleting those directories afterwards also
deleted their telemetry, so the cost of the run had to be estimated from a single
sampled task rather than measured. `wbrun.py` reads the cost out before deleting;
`wvrun.py` was already running and could not be fixed mid-flight.

## Web Bench

Dataset fetched (`Halluminate/WebBench`, 2,647 tasks over 449 sites) and a runner
written, but not run.

Only the 1,637 READ tasks were ever in scope. The 1,006 write tasks ask an agent
to register accounts, edit profiles and delete records on real third-party sites
under the user's name, which is not something to do for a benchmark number. Any
figure produced here would therefore be read-only and not comparable to the
published all-tasks headline of 66%.

## To finish this properly

Run the remaining 15 WebVoyager tasks, then verify all 30 answers by hand against
the live pages. Roughly $2 of model time and an hour of checking. Until then the
honest summary is: she reaches real sites reliably and is not stopped by the ones
expected to stop her, and her accuracy is unmeasured.
