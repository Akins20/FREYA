package bench

import "time"

// Data-processing benchmarks.
//
// The capability under test is arithmetic over data she has to parse herself:
// read a CSV or a list, compute a sum / average / max / min / count, filter or
// sort the rows, and land the *computed* result in a file. A chatbot can recite
// a plausible number; only an agent that actually parsed and reduced the data
// arrives at the exact value.
//
// Every check therefore anchors on a figure that is only reachable by computing
// it — a total, an average, a range, a group subtotal — and each such anchor is
// chosen so it is NOT a substring of any raw input value. Copying the file
// wholesale, echoing a cell, or guessing all fail: the sum comes out wrong the
// moment a row is double-counted, dropped, or left unfiltered, and a wrong sum
// misses the needle. Extremes (max/min) that happen to equal a raw cell ride
// alongside a computed anchor, never alone.

func init() {
	Register(
		// --- Difficulty 2: sum a column and average it ----------------------
		// 120+340+55+210+75 = 800; average 800/5 = 160. Neither figure appears
		// among the rows, so both can only come from the arithmetic.
		Benchmark{
			Name:       "sum-and-average-sales",
			Category:   "data-processing",
			Difficulty: 2,
			Setup: WriteFile("sales.csv",
				"date,amount\n"+
					"2026-06-01,120\n"+
					"2026-06-02,340\n"+
					"2026-06-03,55\n"+
					"2026-06-04,210\n"+
					"2026-06-05,75\n"),
			Prompt: "sales.csv has a date and an amount on every row. Add up all the amounts and " +
				"work out the average sale, then write both — the total and the average — to " +
				"summary.txt, each clearly labelled.",
			Check: All(
				Drove(2),
				// 800 = total, 160 = average; neither is any single row's amount.
				FileHas("summary.txt", "800", "160"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 2: average, maximum and minimum of a list -----------
		// (40+60+50+70+45+65)/6 = 330/6 = 55. The average 55 is in no line;
		// max 70 and min 40 are the genuine extremes, riding with it.
		Benchmark{
			Name:       "min-max-average-readings",
			Category:   "data-processing",
			Difficulty: 2,
			Setup: WriteFile("readings.txt",
				"40\n60\n50\n70\n45\n65\n"),
			Prompt: "readings.txt has one sensor reading per line. Work out the average reading, the " +
				"highest, and the lowest, and write all three — clearly labelled — to stats.txt.",
			Check: All(
				Drove(2),
				// 55 is the computed average (in no line); 70 and 40 are the true
				// max and min, so all three together prove she scanned the list.
				FileHas("stats.txt", "55", "70", "40"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: filter rows over a threshold, then total them ----
		// Keeping only amount > 100 leaves 150, 200, 120, 300 → sum 770. Copying
		// the whole file into big_orders.csv still passes the row needles, but the
		// full sum is 995, so total.txt's 770 is the thing a no-filter answer
		// cannot produce.
		Benchmark{
			Name:       "filter-transactions-over-100",
			Category:   "data-processing",
			Difficulty: 3,
			Setup: WriteFile("transactions.csv",
				"id,amount\n"+
					"1,50\n"+
					"2,150\n"+
					"3,200\n"+
					"4,80\n"+
					"5,120\n"+
					"6,95\n"+
					"7,300\n"),
			Prompt: "transactions.csv lists transactions with an amount column. Pull out just the " +
				"transactions over 100 into a new file called big_orders.csv, then write their " +
				"combined total to total.txt.",
			Check: All(
				Drove(2),
				// The four qualifying amounts must all be in the filtered file.
				FileHas("big_orders.csv", "150", "200", "120", "300"),
				// 770 = 150+200+120+300; leaving the sub-100 rows in makes it 995.
				FileHas("total.txt", "770"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: sort, then derive a median and a range -----------
		// Sorted: 60,72,77,81,88,90,95. Median (4th of 7) = 81; range 95-60 = 35.
		// The range 35 is in no line, so it is the anchor a sort-free guess misses;
		// the seven names prove the leaderboard was actually produced.
		Benchmark{
			Name:       "rank-scores-leaderboard",
			Category:   "data-processing",
			Difficulty: 3,
			Setup: WriteFile("scores.csv",
				"player,score\n"+
					"Alice,88\n"+
					"Bob,72\n"+
					"Carol,95\n"+
					"Dave,60\n"+
					"Eve,81\n"+
					"Frank,77\n"+
					"Grace,90\n"),
			Prompt: "scores.csv lists seven players and their scores. Write leaderboard.txt listing " +
				"every player ranked from the highest score to the lowest, and at the bottom note " +
				"the median score and the score range (the highest minus the lowest).",
			Check: All(
				Drove(2),
				FileHas("leaderboard.txt",
					"Alice", "Bob", "Carol", "Dave", "Eve", "Frank", "Grace"),
				// 81 = median, 35 = range; 35 appears in no row.
				FileHas("leaderboard.txt", "81", "35"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: group by region and subtotal into a spreadsheet --
		// North 200+300+100=600, South 150+250=400, East 120+80=200, grand 1200.
		// The grand total 1200 requires every region counted; drop East and it
		// falls to 1000, so 1200 pins that all three groups were summed.
		Benchmark{
			Name:       "region-sales-totals-xlsx",
			Category:   "data-processing",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: WriteFile("sales.csv",
				"date,region,amount\n"+
					"2026-01-05,North,200\n"+
					"2026-01-06,South,150\n"+
					"2026-01-07,North,300\n"+
					"2026-01-08,East,120\n"+
					"2026-01-09,South,250\n"+
					"2026-01-10,East,80\n"+
					"2026-01-11,North,100\n"),
			Prompt: "sales.csv has a region and an amount on every row. Build a spreadsheet called " +
				"region_totals.xlsx with one row per region showing that region's total sales, " +
				"and a final row giving the grand total across all regions.",
			Check: All(
				Drove(2),
				// 600 = North, 400 = South (neither is any raw amount), and
				// 1200 = grand total across all three regions.
				GlobHas("workspace/*.xlsx",
					"North", "South", "East", "600", "400", "1200"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: compute a per-row column, total it, find the top -
		// Revenue = qty*price: 60, 75, 100, 80, 120 (total 435). None of these
		// products of the two columns appears among the raw qty/price values, so
		// the whole revenue column, the total, and the top seller are all products
		// of arithmetic she had to perform.
		Benchmark{
			Name:       "order-revenue-xlsx",
			Category:   "data-processing",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: WriteFile("orders.csv",
				"product,qty,price\n"+
					"Widget,3,20\n"+
					"Gadget,5,15\n"+
					"Gizmo,2,50\n"+
					"Doohickey,10,8\n"+
					"Sprocket,4,30\n"),
			Prompt: "orders.csv has a product, a quantity and a unit price per line. Make a spreadsheet " +
				"called order_revenue.xlsx with a revenue column for each product (quantity times " +
				"price), a final row with the total revenue across all products, and note which " +
				"product brought in the most revenue.",
			Check: All(
				Drove(2),
				// The five computed revenues plus the 435 total, none of which is a
				// raw cell, and Sprocket (120) as the highest earner.
				GlobHas("workspace/*.xlsx",
					"Widget", "Gadget", "Gizmo", "Doohickey", "Sprocket",
					"60", "75", "100", "80", "120", "435"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 5: combine three files, aggregate, rank, average ----
		// Per person across the quarter: Alice 300+250+200=750, Bob 200+400+120=720,
		// Carol 150+100+350=600. Grand total 2070; average per person 690. Every
		// one of those five figures is absent from the raw rows, so each is proof
		// of a correct combine-and-sum — a dropped file or a mis-attributed row
		// throws them all off.
		Benchmark{
			Name:       "combine-quarterly-sales-report",
			Category:   "data-processing",
			Difficulty: 5,
			Timeout:    5 * time.Minute,
			Setup: Setups(
				WriteFile("q1/jan.csv", "salesperson,amount\nAlice,300\nBob,200\nCarol,150\n"),
				WriteFile("q1/feb.csv", "salesperson,amount\nAlice,250\nBob,400\nCarol,100\n"),
				WriteFile("q1/mar.csv", "salesperson,amount\nAlice,200\nBob,120\nCarol,350\n"),
			),
			Prompt: "The q1 folder has three monthly sales files — jan.csv, feb.csv and mar.csv — each " +
				"listing salespeople and their sales for that month. Combine all three, work out each " +
				"salesperson's total for the quarter, and write report.txt ranking them from the " +
				"highest total to the lowest, followed by the grand total for the quarter and the " +
				"average total per salesperson.",
			Check: All(
				Drove(3),
				FileHas("report.txt", "Alice", "Bob", "Carol"),
				// 750/720/600 = per-person totals, 2070 = grand total, 690 = average;
				// none of the five is any monthly figure in the source files.
				FileHas("report.txt", "750", "720", "600", "2070", "690"),
				FinishedCleanly(),
			),
		},
	)
}
