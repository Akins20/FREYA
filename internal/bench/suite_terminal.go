package bench

import "time"

// Terminal benchmarks.
//
// The capability under test is shell work that only a live terminal session can
// do: pipes, redirection, a script written and then run, commands chained so one
// stage feeds the next. run_shell executes a single binary with no shell around
// it — no `|`, no `>`, no `;` — so a task phrased as "grep these lines into a
// file and count them" cannot be satisfied by a lone exec. It needs the terminal
// tool, which is why every prompt below asks for the terminal explicitly and
// every check asserts UsedTool("terminal_run", …) alongside the artifact.
//
// Strictness lives in the artifacts, never in the reply. Each check anchors on a
// value that only the correct pipeline produces — a de-duplicated count, a summed
// revenue, an average of the true top rows, a week-wide failure tally — and each
// anchor is chosen so it is NOT a substring of any raw input line. A pipeline
// that drops a stage, forgets to redirect, or miscounts lands a different number
// and misses the needle; echoing the input wholesale never contains the computed
// figure at all.

func init() {
	Register(
		// --- Difficulty 2: grep a pattern through the shell, redirect the count -
		// 13 of the 20 addresses are on gmail.com. run_shell cannot redirect, so
		// the number has to be produced and written from inside a session; a wrong
		// or whole-file count (20) never yields the string "13".
		Benchmark{
			Name:       "count-gmail-contacts",
			Category:   "terminal",
			Difficulty: 2,
			Setup: WriteFile("contacts.txt",
				"alice@gmail.com\nbob@yahoo.com\ncarol@gmail.com\ndave@gmail.com\n"+
					"eve@outlook.com\nfrank@gmail.com\ngrace@gmail.com\nheidi@hotmail.com\n"+
					"ivan@gmail.com\njudy@gmail.com\nmallory@yahoo.com\nniaj@gmail.com\n"+
					"olivia@gmail.com\npeggy@outlook.com\nrupert@gmail.com\nsybil@gmail.com\n"+
					"trent@hotmail.com\nvictor@gmail.com\nwalter@yahoo.com\nzoe@gmail.com\n"),
			Prompt: "contacts.txt has one email address per line. Using the terminal, count how many " +
				"of them are Gmail addresses — the line contains gmail.com — and save just that " +
				"number to gmail_count.txt.",
			Check: All(
				UsedTool("terminal_run", 1),
				// 13 gmail addresses out of 20; a no-filter count gives 20.
				FileHas("gmail_count.txt", "13"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 2: sort | uniq pipeline, redirect the list and the count -
		// The 15 lines collapse to 6 distinct colours. The sorted set proves the
		// pipe ran; the count 6 (not the raw 15) is the anchor a no-dedupe answer
		// cannot produce.
		Benchmark{
			Name:       "dedupe-colour-tags",
			Category:   "terminal",
			Difficulty: 2,
			Setup: WriteFile("tags.txt",
				"red\nblue\nred\ngreen\nblue\nred\nyellow\ngreen\nred\npurple\n"+
					"blue\ngreen\norange\nred\nblue\n"),
			Prompt: "tags.txt lists colour tags, one per line, with lots of repeats. Using the terminal, " +
				"produce tags_sorted.txt containing each colour exactly once in alphabetical order, and " +
				"write the number of distinct colours to distinct_count.txt.",
			Check: All(
				UsedTool("terminal_run", 1),
				// All six survivors of the dedupe, in the file the pipe wrote.
				FileHas("tags_sorted.txt", "blue", "green", "orange", "purple", "red", "yellow"),
				// 6 distinct; failing to dedupe leaves 15.
				FileHas("distinct_count.txt", "6"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: grep matching lines into one file, count into another -
		// 11 ERROR and 6 WARN lines among 25. errors.log must hold the pulled-out
		// lines (their message fragments appear only on ERROR lines) and summary.txt
		// must carry both counts, neither of which is a raw log figure.
		Benchmark{
			Name:       "extract-and-count-log-errors",
			Category:   "terminal",
			Difficulty: 3,
			Setup: WriteFile("app.log",
				"INFO boot\nERROR disk full\nWARN low mem\nINFO ready\nERROR timeout\n"+
					"ERROR db lost\nWARN cache miss\nINFO serve\nERROR auth fail\nINFO ok\n"+
					"ERROR timeout\nWARN slow\nERROR disk full\nINFO retry\nERROR db lost\n"+
					"WARN deprecated\nINFO ok\nERROR auth fail\nERROR timeout\nWARN slow\n"+
					"INFO done\nERROR disk full\nWARN retrying\nINFO ok\nERROR timeout\n"),
			Prompt: "app.log is a server log. Using the terminal, pull every line that mentions ERROR " +
				"into errors.log, then write summary.txt stating how many ERROR lines and how many " +
				"WARN lines the log contained.",
			Check: All(
				UsedTool("terminal_run", 1),
				// The redirected file holds the ERROR-only messages.
				FileHas("errors.log", "disk full", "auth fail", "db lost"),
				// 11 ERROR, 6 WARN; both come only from counting, not from a cell.
				FileHas("summary.txt", "11", "6"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: write a script, run it, redirect its labelled output --
		// Revenue = qty*price summed: 20+20+18+32+18 = 108. No raw cell is 108, and
		// the "Total revenue" label proves the number came from the script she ran
		// rather than a guessed reply.
		Benchmark{
			Name:       "revenue-script",
			Category:   "terminal",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: WriteFile("sales.csv",
				"item,qty,price\npen,10,2\nnotebook,4,5\nmarker,6,3\nfolder,8,4\nstapler,2,9\n"),
			Prompt: "sales.csv lists items with a quantity and a unit price. Write a shell script called " +
				"revenue.sh that adds up the revenue for every item — quantity times price — and prints " +
				"a line reading 'Total revenue: <number>'. Run it in the terminal and save its output " +
				"to revenue.txt.",
			Check: All(
				UsedTool("terminal_run", 1),
				// 108 = 20+20+18+32+18, reachable only by running the arithmetic.
				FileHas("revenue.txt", "total revenue", "108"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: chain sort | head, then average the top rows ----------
		// Sorted by score: Cy 95, Gu 90, Ada 88 are the top three; their average is
		// (95+90+88)/3 = 91. The average is in no row, so a wrong top-three (or a
		// whole-file average) misses 91, and picking the wrong names drops one of
		// Cy / Gu / Ada.
		Benchmark{
			Name:       "top-three-scorers",
			Category:   "terminal",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: WriteFile("scores.csv",
				"name,score\nAda,88\nBo,72\nCy,95\nDi,60\nEz,81\nFi,77\nGu,90\n"),
			Prompt: "scores.csv has a name and a score on each row. Using the terminal, work out the " +
				"three highest scorers, and write top3.txt listing those three names with their scores " +
				"followed by their average score.",
			Check: All(
				UsedTool("terminal_run", 1),
				// The three genuine leaders plus 91, the average only they produce.
				FileHas("top3.txt", "Cy", "Gu", "Ada", "91"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 5: combine several files, then tally across all of them ---
		// Across the three days: 16 FAILED (4+7+5) and 14 succeeded (6+3+5) out of
		// 30 jobs. Both totals require concatenating every file — drop one day and
		// each figure shifts — so 16 and 14 together prove the whole week was read.
		Benchmark{
			Name:       "weekly-failure-tally",
			Category:   "terminal",
			Difficulty: 5,
			Timeout:    5 * time.Minute,
			Setup: Setups(
				WriteFile("logs/monday.log",
					"backup OK\nsync FAILED\nreport OK\ncleanup FAILED\nindex OK\n"+
						"mail OK\npurge FAILED\narchive OK\ndeploy FAILED\nhealth OK\n"),
				WriteFile("logs/tuesday.log",
					"backup FAILED\nsync FAILED\nreport FAILED\ncleanup OK\nindex FAILED\n"+
						"mail FAILED\npurge OK\narchive FAILED\ndeploy FAILED\nhealth OK\n"),
				WriteFile("logs/wednesday.log",
					"backup OK\nsync FAILED\nreport OK\ncleanup FAILED\nindex FAILED\n"+
						"mail OK\npurge FAILED\narchive OK\ndeploy FAILED\nhealth OK\n"),
			),
			Prompt: "The logs folder has three daily job logs — monday.log, tuesday.log and " +
				"wednesday.log — where each line ends in OK or FAILED. Using the terminal, combine all " +
				"three and write report.txt giving the total number of jobs that FAILED and the total " +
				"number that succeeded across the whole week.",
			Check: All(
				UsedTool("terminal_run", 1),
				// 16 failed and 14 succeeded across all three files.
				FileHas("report.txt", "16", "14"),
				FinishedCleanly(),
			),
		},
	)
}
