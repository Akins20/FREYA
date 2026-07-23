package bench

import "time"

// Orchestration benchmarks.
//
// The capability under test is the whole reason an agent exists: chaining
// several distinct capabilities into one end-to-end job. Not "read a file", not
// "add a column", but read *these* inputs, compute the figures they imply, and
// lay the result into one or more real artifacts — a spreadsheet AND a memo, a
// filtered file AND an alert, three monthly CSVs joined into a quarter review.
// A single capability firing in isolation is a tool call; an agent is the thing
// that sequences them without being told each step.
//
// So every check here inspects more than one link in the chain: the computed
// figure that proves she reduced the data, present in the *specific* artifact the
// task asked for, and where the task produces two deliverables, both must carry
// the right numbers — getting the spreadsheet right does not excuse a wrong memo.
// As in the sibling suites, every anchor figure (a total, an average, a joined
// per-row product) is chosen so it is not a substring of any raw input value:
// copying a cell, echoing a file, or skipping a step throws the anchor off and
// misses the needle.

func init() {
	Register(
		// --- Difficulty 2: list a folder, read every file, reduce to a summary --
		// 45+60+90 = 195; average 195/3 = 65. Neither figure is any single week's
		// spend, and 65 is not a substring of 195, so total and average each stand
		// on their own arithmetic. The chain is list → read three → compute → write.
		Benchmark{
			Name:       "weekly-spend-roundup",
			Category:   "orchestration",
			Difficulty: 2,
			Setup: Setups(
				WriteFile("receipts/week1.txt", "Week 1 supplies spend: 45"),
				WriteFile("receipts/week2.txt", "Week 2 supplies spend: 60"),
				WriteFile("receipts/week3.txt", "Week 3 supplies spend: 90"),
			),
			Prompt: "The receipts folder has one file per week with that week's supplies spend. " +
				"See what's in the folder, read every file, and write spend-summary.txt giving the " +
				"total spend across all three weeks and the average spend per week.",
			Check: All(
				Drove(2),
				// 195 = total, 65 = average; neither is any week's raw amount.
				FileHas("spend-summary.txt", "195", "65"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: one input, two deliverables (spreadsheet + memo) ------
		// Total 30+24+18+12 = 84; average 84/4 = 21; largest is Engineering. The
		// spreadsheet carries the rows plus the total; the memo must independently
		// state the total, the largest department and the average — a second
		// capability driven off the same computation, not a copy of the sheet.
		Benchmark{
			Name:       "headcount-memo-and-sheet",
			Category:   "orchestration",
			Difficulty: 3,
			Setup: WriteFile("headcount.csv",
				"department,headcount\n"+
					"Engineering,30\n"+
					"Sales,24\n"+
					"Support,18\n"+
					"Operations,12\n"),
			Prompt: "headcount.csv lists each department and its headcount. Build a spreadsheet called " +
				"headcount.xlsx with the same rows and a Total row summing the headcount, then also " +
				"write a short Word memo called headcount-memo.docx titled 'Headcount Summary' that " +
				"states the total headcount, which department is the largest, and the average " +
				"headcount per department.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(3),
				// 84 = total, absent from every raw cell, so the Total row proves the sum.
				GlobHas("workspace/*.xlsx",
					"Engineering", "Sales", "Support", "Operations",
					"30", "24", "18", "12", "84"),
				// The memo must carry the total (84), the largest dept, and the
				// average (21) — 21 is in no cell and is not a substring of 84.
				GlobHas("workspace/*.docx",
					"Headcount Summary", "Engineering", "84", "21"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: filter to a new file, then alert off the filtered set -
		// Hotter than 80% keeps web2(88), db1(92), db2(95): combined load 275, peak
		// 95. Copying every row into critical.csv still satisfies the host/value
		// needles, but the alert's 275 (the whole-file sum is 426) only appears if
		// the filter actually ran. Chain: read → filter → write file → sum → write.
		Benchmark{
			Name:       "filter-critical-servers",
			Category:   "orchestration",
			Difficulty: 3,
			Setup: WriteFile("servers.csv",
				"host,cpu\n"+
					"web1,45\n"+
					"web2,88\n"+
					"db1,92\n"+
					"cache1,30\n"+
					"web3,76\n"+
					"db2,95\n"),
			Prompt: "servers.csv has a host and its CPU load on every row. Pull the hosts running " +
				"hotter than 80% into a new file called critical.csv, then write alert.txt saying how " +
				"many hosts are over the line, their combined CPU load, and the single highest load.",
			Check: All(
				Drove(2),
				// The three over-threshold hosts and their loads land in the filtered file.
				FileHas("critical.csv", "web2", "db1", "db2", "88", "92", "95"),
				// 275 = 88+92+95 (whole-file sum is 426); 95 is the genuine peak.
				FileHas("alert.txt", "275", "95"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: join two files, compute per row, emit sheet + PDF -----
		// Revenue = price × units, matched by product across two files: 12×5=60,
		// 25×8=200, 15×6=90, 40×7=280; grand total 630, best seller Bolt (280).
		// None of the four products or the total is a raw price or unit count, so
		// each is proof the join and the multiplication happened. Both artifacts
		// must agree on the figures.
		Benchmark{
			Name:       "join-prices-revenue-report",
			Category:   "orchestration",
			Difficulty: 4,
			Setup: Setups(
				WriteFile("prices.csv",
					"product,price\n"+
						"Widget,12\n"+
						"Gadget,25\n"+
						"Sprocket,15\n"+
						"Bolt,40\n"),
				WriteFile("sales.csv",
					"product,units\n"+
						"Widget,5\n"+
						"Gadget,8\n"+
						"Sprocket,6\n"+
						"Bolt,7\n"),
			),
			Prompt: "There are two files: prices.csv lists each product's unit price, and sales.csv " +
				"lists how many units of each product sold. Match them up by product, work out the " +
				"revenue for each (price times units sold), and build a spreadsheet called revenue.xlsx " +
				"with a revenue column and a grand-total row. Then write a one-page PDF called " +
				"revenue-report.pdf titled 'Revenue Report' that gives the grand total and names the " +
				"best-selling product by revenue.",
			Timeout: 6 * time.Minute,
			Check: All(
				Drove(3),
				// The four line revenues and the 630 total, none of them a raw cell.
				GlobHas("workspace/*.xlsx",
					"Widget", "Gadget", "Sprocket", "Bolt",
					"60", "200", "90", "280", "630"),
				// The report independently carries the total and names Bolt (280) as top.
				GlobHas("workspace/*.pdf",
					"Revenue Report", "630", "Bolt", "280"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: aggregate a folder of logs, report, then tidy up ------
		// Errors 4+7+1+9+6 = 27; worst day thu (9), quietest wed (1). The report
		// anchors on the total 27 (in no file) with the peak 9 riding alongside;
		// then the busiest log is renamed, so worst-day.txt must exist and still
		// hold "errors: 9". Chain: list → read five → compute → write → rename.
		Benchmark{
			Name:       "weekly-error-report-cleanup",
			Category:   "orchestration",
			Difficulty: 4,
			Setup: Setups(
				WriteFile("logs/mon.txt", "errors: 4"),
				WriteFile("logs/tue.txt", "errors: 7"),
				WriteFile("logs/wed.txt", "errors: 1"),
				WriteFile("logs/thu.txt", "errors: 9"),
				WriteFile("logs/fri.txt", "errors: 6"),
			),
			Prompt: "The logs folder has one file per weekday, each recording that day's error count. " +
				"List the folder, read every log, and write weekly-report.txt with the total errors " +
				"for the week, the worst day and its count, and the quietest day. Then rename the worst " +
				"day's log file to worst-day.txt.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(3),
				// 27 = week total (in no file); 9 is the peak, thu the worst day and
				// wed the quietest — together they prove the whole week was scanned.
				FileHas("weekly-report.txt", "27", "thu", "wed", "9"),
				// The rename actually happened: the busiest log now carries this name.
				FileHas("worst-day.txt", "errors", "9"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 5: three monthly files → sheet + review doc + callout ----
		// Per line across the quarter: Hardware 300+250+200=750, Software
		// 200+400+120=720, Services 150+100+350=600; grand total 2070, average per
		// line 690, top line Hardware. Every one of those five figures is absent
		// from the raw monthly rows, so each proves a correct combine-and-sum. The
		// three deliverables — spreadsheet, executive doc, callout file — must all
		// agree, and a dropped month or mis-attributed line throws them all off.
		Benchmark{
			Name:       "q3-business-review-pipeline",
			Category:   "orchestration",
			Difficulty: 5,
			Setup: Setups(
				WriteFile("q3/jul.csv", "line,revenue\nHardware,300\nSoftware,200\nServices,150\n"),
				WriteFile("q3/aug.csv", "line,revenue\nHardware,250\nSoftware,400\nServices,100\n"),
				WriteFile("q3/sep.csv", "line,revenue\nHardware,200\nSoftware,120\nServices,350\n"),
			),
			Prompt: "The q3 folder has three monthly files — jul.csv, aug.csv and sep.csv — each listing " +
				"revenue by product line for that month. Combine all three into each line's total for " +
				"the quarter. Build a spreadsheet called quarter-totals.xlsx with one row per line and " +
				"a grand-total row. Then write an executive Word document called q3-review.docx titled " +
				"'Q3 Business Review' giving the grand total, the top-performing line, and the average " +
				"revenue per line. Finally, note the winning line and its quarter total in top-performer.txt.",
			Timeout: 7 * time.Minute,
			Check: All(
				Drove(4),
				// Per-line quarter totals (750/720/600) and the 2070 grand total, none
				// of which is any monthly figure in the source files.
				GlobHas("workspace/*.xlsx",
					"Hardware", "Software", "Services",
					"750", "720", "600", "2070"),
				// The review carries the grand total (2070) and the average per line
				// (690, in no cell and not a substring of 2070) and names the top line.
				GlobHas("workspace/*.docx",
					"Q3 Business Review", "Hardware", "2070", "690"),
				// The callout independently names Hardware and its 750 quarter total.
				FileHas("top-performer.txt", "Hardware", "750"),
				FinishedCleanly(),
			),
		},
	)
}
