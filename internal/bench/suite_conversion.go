package bench

import "time"

// Format-conversion benchmarks.
//
// The capability under test is turning data from one format into another
// *faithfully* — and, where the ask implies arithmetic, computing the derived
// figures rather than transcribing cells. So every check reads the produced
// artifact back through the same extractor a reader would (FileHas/GlobHas pull
// text out of xlsx/pdf/docx) and asserts on the values that can only be there
// if she did the work: a total, an average, a per-row product.
//
// The datasets are chosen so no computed value is a substring of any source
// figure — the total introduces a digit the inputs never contain, so the needle
// cannot slip in by transcription. A conversion that drops rows, mislabels a
// column, or writes the total without adding fails its check; a plausible reply
// with no artifact fails too.

func init() {
	Register(
		// --- Difficulty 2: csv -> xlsx with a computed total row -------------
		Benchmark{
			Name:       "csv-to-xlsx-with-total",
			Category:   "conversion",
			Difficulty: 2,
			Setup: WriteFile("regions.csv",
				"region,units\n"+
					"North,120\n"+
					"South,90\n"+
					"East,150\n"+
					"West,80\n"),
			Prompt: "Turn regions.csv into an Excel spreadsheet called regions.xlsx with the same " +
				"rows, then add a Total row at the bottom that sums the units column.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// 120+90+150+80 = 440. The inputs contain no digit 4, so 440 can
				// only appear if she actually added the column.
				GlobHas("workspace/*.xlsx",
					"North", "South", "East", "West",
					"120", "90", "150", "80", "440"),
			),
		},

		// --- Difficulty 3: markdown -> pdf, structure preserved --------------
		Benchmark{
			Name:       "markdown-to-pdf-notes",
			Category:   "conversion",
			Difficulty: 3,
			Setup: WriteFile("release-notes.md",
				"# Release Notes v2.0\n\n"+
					"## Highlights\n"+
					"- Faster startup time\n"+
					"- New dark theme\n"+
					"- Offline sync support\n\n"+
					"## Bug Fixes\n"+
					"- Fixed a crash on export\n"+
					"- Patched a memory leak in the cache\n"),
			Prompt: "Convert release-notes.md into a PDF called release-notes.pdf, keeping the " +
				"headings and the bullet points so it reads the same as the markdown.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// Single-word needles survive PDF line-wrapping; together they prove
				// every heading and bullet made it across, not just the title.
				GlobHas("workspace/*.pdf",
					"Release", "Highlights", "startup", "dark",
					"Offline", "sync", "Fixes", "export", "leak"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: csv -> xlsx with total AND average rows -----------
		Benchmark{
			Name:       "csv-to-xlsx-total-and-average",
			Category:   "conversion",
			Difficulty: 3,
			Setup: WriteFile("scores.csv",
				"name,score\n"+
					"Ada,90\n"+
					"Grace,80\n"+
					"Linus,100\n"+
					"Margaret,70\n"),
			Prompt: "Convert scores.csv into an Excel spreadsheet called scores.xlsx with the same " +
				"data, then add a Total row and an Average row at the bottom of the score column.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// Total 90+80+100+70 = 340; average = 85. The inputs contain no 3, 4
				// or 5, so both figures betray real arithmetic rather than a copy.
				GlobHas("workspace/*.xlsx",
					"Ada", "Grace", "Linus", "Margaret",
					"90", "80", "100", "70", "340", "85"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: csv -> docx table with a derived column + total ---
		Benchmark{
			Name:       "csv-to-docx-revenue-table",
			Category:   "conversion",
			Difficulty: 4,
			Setup: WriteFile("products.csv",
				"product,price,units\n"+
					"Notebook,4,30\n"+
					"Pen,2,50\n"+
					"Backpack,25,8\n"+
					"Folder,3,45\n"),
			Prompt: "Turn products.csv into a Word document called sales-table.docx that lays the " +
				"data out as a table: a column for the product, its price, the units sold, and a " +
				"Revenue column equal to price times units. Finish with a Total row giving the " +
				"total revenue across all products.",
			Timeout: 5 * time.Minute,
			Check: All(
				Drove(2),
				// Line revenue 4*30=120, 2*50=100, 25*8=200, 3*45=135; total 555.
				// None of these numbers appear among the source figures, so each is
				// proof she multiplied per row and then summed.
				GlobHas("workspace/*.docx",
					"Notebook", "Pen", "Backpack", "Folder",
					"120", "100", "200", "135", "555"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: markdown table -> xlsx with line + grand totals ---
		Benchmark{
			Name:       "markdown-table-to-xlsx",
			Category:   "conversion",
			Difficulty: 4,
			Setup: WriteFile("orders.md",
				"# Orders\n\n"+
					"| Product  | Price | Qty |\n"+
					"|----------|-------|-----|\n"+
					"| Widget   | 12    | 5   |\n"+
					"| Gadget   | 25    | 8   |\n"+
					"| Sprocket | 15    | 6   |\n"+
					"| Bolt     | 40    | 7   |\n"),
			Prompt: "Pull the table out of orders.md and convert it into an Excel spreadsheet called " +
				"orders.xlsx with a Product, Price, Qty and Line Total column, where the line total " +
				"is price times quantity. Add a final row with the grand total of all the line totals.",
			Timeout: 6 * time.Minute,
			Check: All(
				Drove(2),
				// Line totals 12*5=60, 25*8=200, 15*6=90, 40*7=280; grand total 630.
				// The prices survive the extraction; the line totals and grand total
				// only exist if she read the markdown table and did the maths.
				GlobHas("workspace/*.xlsx",
					"Widget", "Gadget", "Sprocket", "Bolt",
					"12", "25", "15", "40", "60", "200", "90", "280", "630"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 5: one source, converted into TWO formats ------------
		Benchmark{
			Name:       "one-source-to-xlsx-and-docx",
			Category:   "conversion",
			Difficulty: 5,
			Setup: WriteFile("sales-by-rep.md",
				"# Sales by Rep\n\n"+
					"| Rep   | Deals |\n"+
					"|-------|-------|\n"+
					"| Alice | 12    |\n"+
					"| Bob   | 18    |\n"+
					"| Carol | 10    |\n"+
					"| Dave  | 16    |\n"),
			Prompt: "Take the sales table in sales-by-rep.md and convert it two ways. First an Excel " +
				"spreadsheet called sales.xlsx with the same rows plus a Total row and an Average " +
				"row for the deals column. Second a Word document called sales.docx showing the data " +
				"as a table with a Total row. I need both files.",
			Timeout: 7 * time.Minute,
			Check: All(
				Drove(3),
				// Total 12+18+10+16 = 56; average = 14. The deal counts contain no 4
				// and no 5, so the average forces the digit 4 and the total the 5 —
				// neither can be transcribed. Both artifacts must carry them: getting
				// one format right does not excuse the other.
				GlobHas("workspace/*.xlsx",
					"Alice", "Bob", "Carol", "Dave",
					"12", "18", "10", "16", "56", "14"),
				GlobHas("workspace/*.docx",
					"Alice", "Bob", "Carol", "Dave",
					"12", "18", "10", "16", "56"),
				FinishedCleanly(),
			),
		},
	)
}
