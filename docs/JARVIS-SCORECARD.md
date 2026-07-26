# Freya vs JARVIS — capability scorecard

A checkpoint taken 24 Jul 2026, measured against the tree as built:
**106 tools · 8 playbooks · 9 watchers · ~32K LOC + 8.3K test LOC.**

Kept so we can come back to it: re-measure after each phase and see what actually
moved, rather than re-arguing from memory.

## Where she genuinely matches JARVIS

| JARVIS trait | Freya today | Evidence |
|---|---|---|
| Voice-first, no keyboard | Push-to-talk, spoken confirmations, spoken results | Voice has been the only interface in daily use |
| Persists across time | Tiered memory, BM25 recall, append-only archive | 600+ turns, months of facts |
| Acts in the real world | 106 tools — shell, files, browser, desktop, docs | Drove and submitted real coursework end-to-end |
| Proactive monitoring | 9 watchers + salience/dedup gating | Disk, battery, git, deadlines |
| **Wakes herself to continue** | Self-tasks: set → poll → fire once → run → report | Journal: `⏰ following up` → ran → spoke |
| **Ambient awareness of the user** | 60s window sampling + vision depth on browser/folder | Read the actual page content, not just the tab title |
| Deadline anticipation | Escalating buckets 15m→1h→3h→12h→1d→3d | `[critical] due in 9 minutes` |
| Unprompted re-engagement | Lull review of conversation + state + activity | "Before you head out — that's due in 20 minutes" |
| Personality with spine | Persona-as-data, unconditional anti-sycophancy | Pushes back, does not flatter |
| Delegates to a specialist | `claude_delegate` hands heavy engineering to Claude Code | The "run the numbers" pattern |

## Where she falls short — ranked by impact

1. **Competence under pressure — the real gap.** She guessed 20 CSS selectors in a
   row and burned 25 rounds until a circuit breaker physically stopped her. Nearly
   every reliability win so far is a guardrail compensating for a weak substrate,
   not intelligence. *Diagnosis: architectural, not model.* The fumbles traced to
   shadow-DOM-blind reads, iframe-blind interaction, synthetic clicks that silently
   no-op, tab-name lookups that errored instead of resolving, and a working
   directory that differed between file and shell tools. The software was not
   strong enough to support her.
2. **Single-threaded.** `pttBusy` serialises everything: one turn at a time. She
   cannot watch a download, drive a page and talk at once; self-tasks queue behind
   voice turns. This is a design gap, not a model limit.
3. **No streaming, so no interruption.** Think → act → speak. The user cannot cut
   in mid-action with "no, stop, do it this way."
4. **Always-on listening is unreliable.** Wake word off; Ctrl+Space instead. The
   hardware's mic noise floor beat continuous detection.
5. **She does not learn from her own failures.** Every fix so far was a human
   editing her code and prompts. No path from "I failed this way three times" to an
   updated playbook.
6. **Perception is a snapshot, not a stream.** Vision fires on window change only.

## What she does better than JARVIS

**Transparency.** JARVIS is a black box that works because it is fiction. Freya
shows her reasoning (`💭`), journals every tool call, records exact token cost per
call, and has 8.3K lines of tests pinning behaviours that previously broke. When
she failed, the reason was readable from her own thoughts. On real hardware that
auditability is what makes her fixable rather than mysterious.

## Baselines to measure against

Taken 24 Jul 2026, before any of the reliability work, from her own telemetry and
archive. Re-measure these after each phase rather than re-arguing from memory.

| Metric | Baseline |
|---|---|
| Events that failed | 397 / 1662 (**24%**) |
| Real tool failures | ~230 — **229 of them browser interaction** |
| Replies that died at the round cap | 11 / 101 (**11%**) |
| Worst thrash run | **19 consecutive** `browser_click_real` calls |
| **Prompt-cache hit rate** | **70.5%** (31.2M cached of 44.3M input) — the canary; a drop means tier ordering broke |
| Spend to date | $4.63 over 701 model calls |

## Chosen direction

Attack **1** and **2**. Both are ours to fix: build a substrate strong enough that
competence is the default rather than something guardrails have to enforce, and a
concurrency model that lets her hold more than one thread of work at a time.
