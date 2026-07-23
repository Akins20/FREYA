package bench

import "time"

// Document-generation benchmarks.
//
// The capability under test is not "can she type prose" but "can she read
// figures out of an input, do the arithmetic, and lay the result out as a real
// document with structure" — a title, sections, and a table whose cells hold the
// right numbers. So every check reads the generated docx/pdf back through the
// same extractor a human reader would (FileHas/GlobHas pull text out of the
// binary formats) and asserts on the *figures*, including a total or average
// that only appears if she computed it. A plausible report with wrong numbers
// fails; a report that merely echoes the input without the computed row fails
// too. The needles are chosen so no one number is a substring of another, so a
// match means the value is genuinely present.

func init() {
	Register(
		// --- Difficulty 2: single input, title + section + table + total -----
		Benchmark{
			Name:       "expense-report-docx",
			Category:   "doc-generation",
			Difficulty: 2,
			Setup: WriteFile("expenses.txt",
				"Q3 team expenses:\n"+
					"Travel: 210\n"+
					"Lodging: 340\n"+
					"Meals: 95\n"+
					"Supplies: 60\n"),
			Prompt: "Read expenses.txt and make me a Word document called expense-report.docx " +
				"titled 'Q3 Team Expenses'. Give it a short intro paragraph, then a table with a " +
				"row for each expense category and its amount, and finish the table with a Total " +
				"row that adds them all up.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// 210+340+95+60 = 705 — the Total row is the proof she added, not copied.
				GlobHas("workspace/*.docx",
					"Q3 Team Expenses", "Travel", "Lodging", "Meals", "Supplies",
					"210", "340", "95", "60", "705"),
			),
		},

		// --- Difficulty 3: PDF report with total AND average -----------------
		Benchmark{
			Name:       "signups-report-pdf",
			Category:   "doc-generation",
			Difficulty: 3,
			Setup: WriteFile("signups.txt",
				"New signups by month this quarter:\n"+
					"Jan: 100\n"+
					"Feb: 140\n"+
					"Mar: 90\n"),
			Prompt: "Read signups.txt and produce a PDF report called signups-report.pdf titled " +
				"'Q1 Signups'. Open with a one-paragraph overview, then a table listing each month " +
				"and its signups, with a Total row and an Average row at the bottom.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// Total 100+140+90 = 330; average = 110. Both differ from every row
				// value, so neither can slip in by accident from a copied cell.
				GlobHas("workspace/*.pdf",
					"Q1 Signups", "Jan", "Feb", "Mar",
					"100", "140", "90", "330", "110"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: figures pulled from three separate files ----------
		Benchmark{
			Name:       "regional-revenue-docx",
			Category:   "doc-generation",
			Difficulty: 3,
			Setup: Setups(
				WriteFile("north.txt", "North region revenue this month: 240"),
				WriteFile("south.txt", "South region revenue this month: 300"),
				WriteFile("east.txt", "East region revenue this month: 180"),
			),
			Prompt: "There are three region files — north.txt, south.txt and east.txt — each " +
				"holding that region's revenue for the month. Read all three and write a Word " +
				"document called revenue-summary.docx titled 'Regional Revenue Summary' with a " +
				"short opening section, a table of region and revenue, and a Total row.",
			Timeout: 5 * time.Minute,
			Check: All(
				// Must read all three files to have all three figures; total = 720.
				Drove(3),
				GlobHas("workspace/*.docx",
					"Regional Revenue Summary", "North", "South", "East",
					"240", "300", "180", "720"),
			),
		},

		// --- Difficulty 4: per-row computed column plus grand total ----------
		Benchmark{
			Name:       "inventory-value-report",
			Category:   "doc-generation",
			Difficulty: 4,
			Setup: WriteFile("inventory.csv",
				"item,unit_price,qty\n"+
					"Widget,12,5\n"+
					"Gadget,25,8\n"+
					"Sprocket,15,6\n"),
			Prompt: "Read inventory.csv — it has an item, a unit price and a quantity per line. " +
				"Make a Word document called inventory-report.docx titled 'Warehouse Inventory'. " +
				"Add a short section saying what it covers, then a table with a column for the " +
				"item, its unit price, quantity, and the line value (unit price times quantity), " +
				"and a final row with the grand total value across all items.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// Line values 12*5=60, 25*8=200, 15*6=90; grand total 350. The three
				// products and the total cannot appear unless she multiplied and summed.
				GlobHas("workspace/*.docx",
					"Warehouse Inventory", "Widget", "Gadget", "Sprocket",
					"12", "25", "15", "60", "200", "90", "350"),
			),
		},

		// --- Difficulty 5: same report rendered to BOTH docx and pdf ---------
		Benchmark{
			Name:       "sprint-review-both-formats",
			Category:   "doc-generation",
			Difficulty: 5,
			Setup: WriteFile("tickets.csv",
				"team,tickets_closed\n"+
					"Alpha,14\n"+
					"Bravo,22\n"+
					"Charlie,9\n"),
			Prompt: "Read tickets.csv, which lists each team and how many support tickets they " +
				"closed this sprint. Produce a report titled 'Sprint Support Review' with an " +
				"overview paragraph and a table of team and tickets closed, plus a Total row and " +
				"an Average row. Save it BOTH as sprint-review.docx and sprint-review.pdf so I " +
				"can share whichever I need.",
			Timeout: 6 * time.Minute,
			Check: All(
				Drove(3),
				// Total 14+22+9 = 45; average = 15. Both formats must carry the same
				// correct figures — one artifact being right does not excuse the other.
				GlobHas("workspace/*.docx",
					"Sprint Support Review", "Alpha", "Bravo", "Charlie",
					"14", "22", "9", "45", "15"),
				GlobHas("workspace/*.pdf",
					"Sprint Support Review", "Alpha", "Bravo", "Charlie",
					"14", "22", "9", "45", "15"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 5: two input columns, derived column, executive doc --
		Benchmark{
			Name:       "profit-loss-report",
			Category:   "doc-generation",
			Difficulty: 5,
			Setup: WriteFile("pnl.csv",
				"month,revenue,cost\n"+
					"Jan,500,320\n"+
					"Feb,400,250\n"+
					"Mar,300,210\n"),
			Prompt: "Read pnl.csv — each line has a month, that month's revenue and its cost. " +
				"Produce a Word document called profit-report.docx titled 'Q1 Profit and Loss'. " +
				"Open with a short executive summary section, then a table giving each month's " +
				"revenue, cost, and profit (revenue minus cost), and a final row with the total " +
				"profit for the quarter.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// Profits 500-320=180, 400-250=150, 300-210=90; total profit 420. Every
				// profit cell and the total is a subtraction/sum she had to perform.
				GlobHas("workspace/*.docx",
					"Profit", "Jan", "Feb", "Mar",
					"500", "400", "300", "320", "250", "210",
					"180", "150", "90", "420"),
				FinishedCleanly(),
			),
		},
	)
}
