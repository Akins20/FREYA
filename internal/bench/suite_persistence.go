package bench

import "time"

// Persistence benchmarks.
//
// The capability under test is stamina, not skill: can she carry a long chain of
// dependent steps all the way to the end without stopping early, hand-waving a
// middle step, or signing off with "I'll get started" before the work is done.
// A chatbot narrates the plan; an agent leaves every artifact the plan called for
// on disk. So each task here is a pipeline of five to eight steps where step N
// reads the *named file* step N-1 was told to write, and the Check asserts that
// every one of those intermediate files exists with the right content — not just
// the final answer. Skip a step and the file it should have produced is missing,
// so its FileHas fails; fake a step and the value it should have carried forward
// is wrong two links down the chain, so the final computed anchor fails.
//
// The anchors are chosen the same way as everywhere else in the suite: each
// intermediate and final figure is only reachable by actually running the step
// that produces it, and is not a substring of any raw input value, so copying a
// file wholesale or guessing a plausible number cannot pass. FinishedCleanly()
// rides on every task because the whole point of the category is the failure mode
// where she trails off mid-pipeline instead of driving it to the end.

func init() {
	Register(
		// --- Difficulty 2: a strict arithmetic relay of five named files -----
		// 7 → double = 14 → +10 = 24 → *3 = 72. Every link is a separate file she
		// must write before the next can read it, so a missing file fails its own
		// FileHas and the wrong number never reaches answer.txt. The operations are
		// stated in the prompt, but the enforcement is that each intermediate file
		// has to actually exist — a chatbot that computes 72 in its head and skips
		// the files fails four of the five checks.
		Benchmark{
			Name:       "numeric-relay-chain",
			Category:   "persistence",
			Difficulty: 2,
			Prompt: "I want you to do this as a relay, writing each result to its own file before moving " +
				"on to the next step — don't shortcut it. Step one: write the number 7 to step1.txt. " +
				"Step two: read step1.txt, double that number, and write the result to step2.txt. " +
				"Step three: read step2.txt, add 10, and write that to step3.txt. Step four: read " +
				"step3.txt, multiply by 3, and write it to step4.txt. Last, read step4.txt and write " +
				"answer.txt containing the final number and a line confirming all four steps ran.",
			Check: All(
				Drove(4),
				FileHas("step1.txt", "7"),
				FileHas("step2.txt", "14"),
				FileHas("step3.txt", "24"),
				FileHas("step4.txt", "72"),
				FileHas("answer.txt", "72"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: an inventory pipeline where every stage transforms --
		// Start 10/20/15/5. Restock +10 → 20/30/25/15. Ship -8 → 12/22/17/7.
		// Final remaining total 58. Skip the restock and shipping the originals
		// gives 18; skip the shipping and it is 90 — so 58 is only reachable when
		// both transform steps ran and each wrote its file for the next to read.
		Benchmark{
			Name:       "inventory-restock-pipeline",
			Category:   "persistence",
			Difficulty: 3,
			Setup: WriteFile("inventory.csv",
				"item,qty\n"+
					"apples,10\n"+
					"bananas,20\n"+
					"cherries,15\n"+
					"dates,5\n"),
			Prompt: "Work through this in order, saving each stage to the file I name. First read " +
				"inventory.csv and apply a restock that adds 10 units to every item's quantity, " +
				"writing the updated table to restocked.csv. Then read restocked.csv and apply a " +
				"bulk order that ships out 8 units of each item, writing the result to shipped.csv. " +
				"Finally read shipped.csv, add up the quantities that remain, and write that grand " +
				"total to report.txt.",
			Check: All(
				Drove(4),
				// 30 and 25 exist only after the +10 restock (originals are 15 and 5).
				FileHas("restocked.csv", "apples", "bananas", "cherries", "dates", "30", "25"),
				// 12/22/17 exist only after shipping 8 off the restocked figures.
				FileHas("shipped.csv", "apples", "bananas", "cherries", "dates", "12", "22", "17"),
				// 58 = 12+22+17+7; any skipped stage lands on 18 or 90 instead.
				FileHas("report.txt", "58"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: a log-triage chain that must not stop halfway ------
		// Four ERROR lines, three WARN lines, seven combined. The two counts each
		// live in their own file, and triage.txt has to read both back and add
		// them, so the 7 is proof the chain ran end to end rather than the model
		// answering "there are some errors" and quitting.
		Benchmark{
			Name:       "log-triage-pipeline",
			Category:   "persistence",
			Difficulty: 3,
			Setup: WriteFile("app.log",
				"INFO  server started\n"+
					"ERROR db connection failed\n"+
					"WARN  slow query 1200ms\n"+
					"INFO  request handled\n"+
					"ERROR timeout on upstream\n"+
					"ERROR disk write failed\n"+
					"WARN  cache miss\n"+
					"INFO  request handled\n"+
					"ERROR auth rejected\n"+
					"WARN  retry scheduled\n"),
			Prompt: "Triage app.log for me step by step, and save each step to the file I name. " +
				"First pull out just the ERROR lines into errors.txt. Then count the lines in " +
				"errors.txt and write only that number to error_count.txt. Next pull out just the " +
				"WARN lines into warnings.txt, and count those into warning_count.txt the same way. " +
				"Finally read both count files and write triage.txt stating how many errors, how " +
				"many warnings, and the combined total of the two.",
			Check: All(
				Drove(5),
				FileHas("errors.txt",
					"db connection failed", "timeout on upstream", "disk write failed", "auth rejected"),
				FileHas("error_count.txt", "4"),
				FileHas("warnings.txt", "slow query", "cache miss", "retry scheduled"),
				FileHas("warning_count.txt", "3"),
				// 4 errors, 3 warnings, 7 combined — the sum only appears if both
				// count files were produced and read back.
				FileHas("triage.txt", "4", "3", "7"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: clean → group → spreadsheet, ending in an xlsx -----
		// The raw file carries a stray TOTAL,,9999 summary row. Drop it and the
		// region subtotals are West 640, East 400, North 220 for a grand total of
		// 1260; leave it in and the grand total balloons past 11000, so 1260 in the
		// final spreadsheet is what proves the clean step at the top of the chain
		// actually happened.
		Benchmark{
			Name:       "sales-clean-to-xlsx-pipeline",
			Category:   "persistence",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: WriteFile("raw.csv",
				"region,product,amount\n"+
					"West,Alpha,220\n"+
					"East,Beta,140\n"+
					"West,Gamma,310\n"+
					"North,Alpha,130\n"+
					"East,Gamma,260\n"+
					"North,Beta,90\n"+
					"West,Alpha,110\n"+
					"TOTAL,,9999\n"),
			Prompt: "raw.csv ends with a stray 'TOTAL' summary row that got left in by mistake — it " +
				"isn't a real sale. Build this up in stages, each saved to the file I name. First " +
				"clean the data by dropping that bogus TOTAL row and write the real rows to " +
				"cleaned.csv. Then read cleaned.csv, work out each region's total sales, and write " +
				"region_totals.csv with one row per region. Finally turn region_totals.csv into a " +
				"spreadsheet called region_report.xlsx with a row per region and a final grand-total " +
				"row across all regions.",
			Check: All(
				Drove(3),
				FileHas("cleaned.csv",
					"West", "East", "North", "Alpha", "Beta", "Gamma",
					"220", "310", "110", "140", "260", "130", "90"),
				// 640 and 400 are computed subtotals present in no raw row.
				FileHas("region_totals.csv", "West", "East", "North", "640", "400", "220"),
				// 1260 = 640+400+220; leaving the 9999 junk row in blows this up.
				GlobHas("workspace/*.xlsx", "West", "East", "North", "640", "400", "1260"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: gather → tabulate → total → assemble a docx --------
		// Three status files feed one facts table, whose budgets sum to 125000, and
		// that total plus the flagged-delayed project have to land in the final Word
		// document. Miss a source file and the total is wrong; skip the docx and the
		// whole thing was for nothing — the last step is the one an early sign-off
		// abandons.
		Benchmark{
			Name:       "status-report-assembly-pipeline",
			Category:   "persistence",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: Setups(
				WriteFile("projects/alpha.txt", "Project Alpha\nStatus: on track\nBudget: 40000\nOwner: Sarah\n"),
				WriteFile("projects/beta.txt", "Project Beta\nStatus: delayed\nBudget: 25000\nOwner: Tom\n"),
				WriteFile("projects/gamma.txt", "Project Gamma\nStatus: on track\nBudget: 60000\nOwner: Priya\n"),
			),
			Prompt: "The projects folder has one status file per project. Pull this together in stages, " +
				"saving each to the file I name. First read all three files and write facts.csv with a " +
				"row per project holding its name, status, budget and owner. Then read facts.csv, add " +
				"up the budgets, and write the combined budget to budget_total.txt. Finally assemble a " +
				"Word document called status-report.docx that lists every project with its status and " +
				"owner, states the total budget across all of them, and calls out which project is " +
				"running delayed.",
			Check: All(
				Drove(4),
				FileHas("facts.csv",
					"Alpha", "Beta", "Gamma", "Sarah", "Tom", "Priya",
					"40000", "25000", "60000", "on track", "delayed"),
				// 40000+25000+60000 = 125000, reachable only by reading all three files.
				FileHas("budget_total.txt", "125000"),
				GlobHas("workspace/*.docx", "Alpha", "Beta", "Gamma", "125000", "delayed"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 5: a full monthly close, six named artifacts deep -----
		// Four weekly files fold into one ledger, then category totals (travel 710,
		// meals 145, office 455), then a grand total 1310, then the single largest
		// line item (Flights, 300), then a spreadsheet, then a summary that ties it
		// together. Every figure is only reachable with every prior step done, and
		// there are six files that all have to be on disk at the end — the deepest
		// test in the suite of whether she persists to the finish instead of
		// declaring victory at step three.
		Benchmark{
			Name:       "monthly-close-pipeline",
			Category:   "persistence",
			Difficulty: 5,
			Timeout:    8 * time.Minute,
			Setup: Setups(
				WriteFile("ledger/week1.csv", "item,category,amount\nFlights,travel,300\nLunch,meals,45\nPaper,office,20\n"),
				WriteFile("ledger/week2.csv", "item,category,amount\nHotel,travel,220\nDinner,meals,60\nInk,office,35\n"),
				WriteFile("ledger/week3.csv", "item,category,amount\nTaxi,travel,80\nCoffee,meals,25\nChairs,office,150\n"),
				WriteFile("ledger/week4.csv", "item,category,amount\nTrain,travel,110\nSnacks,meals,15\nMonitor,office,250\n"),
			),
			Prompt: "The ledger folder has four weekly expense files. Run the monthly close as a full " +
				"pipeline, and save each step to the file I name so nothing gets lost. First combine " +
				"all four weeks into all_expenses.csv with a single header and every row beneath it. " +
				"Then read all_expenses.csv and write category_totals.csv giving the total spent in " +
				"each category. Then read category_totals.csv and write the grand total across all " +
				"categories to grand_total.txt. Next scan all_expenses.csv for the single largest " +
				"expense and write its item, category and amount to largest.txt. Then turn " +
				"category_totals.csv into a spreadsheet called close.xlsx with a row per category and " +
				"a grand-total row. Finally write summary.txt stating the grand total, which category " +
				"cost the most, and what the single largest expense was.",
			Check: All(
				Drove(6),
				// One item from each of the four weeks proves the combine covered them all.
				FileHas("all_expenses.csv", "Flights", "Dinner", "Coffee", "Monitor"),
				// travel 710, meals 145, office 455 — none of which is a raw line amount.
				FileHas("category_totals.csv", "travel", "meals", "office", "710", "145", "455"),
				// 1310 = 710+145+455; a dropped week or category throws it off.
				FileHas("grand_total.txt", "1310"),
				// Flights at 300 is the largest single line across all four weeks.
				FileHas("largest.txt", "Flights", "300"),
				GlobHas("workspace/*.xlsx", "travel", "meals", "office", "710", "145", "455", "1310"),
				// The summary can only be right if every step above ran: grand total,
				// biggest category, and the largest single expense.
				FileHas("summary.txt", "1310", "travel", "Flights"),
				FinishedCleanly(),
			),
		},
	)
}
