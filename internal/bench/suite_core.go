package bench

import "time"

// The founding benchmarks: one per category, each proving the harness can grade
// that kind of work. The larger suite is built on this shape.
//
// Every one demands driven work and checks an artifact, never a reply. The
// prompts are phrased as a person would phrase them, because that is what she
// has to handle.

func init() {
	Register(
		// --- file orchestration ---------------------------------------------
		Benchmark{
			Name:       "combine-three-notes",
			Category:   "file-ops",
			Difficulty: 2,
			Setup: Setups(
				WriteFile("notes/a.txt", "Project Apollo kicked off in March. Budget: 40000 dollars."),
				WriteFile("notes/b.txt", "Project Apollo hired three engineers in April."),
				WriteFile("notes/c.txt", "Project Apollo shipped its first release in June."),
			),
			Prompt: "In the notes folder there are three text files about Project Apollo. " +
				"Read all three and write a single file called summary.txt that pulls the " +
				"key facts from every one of them into a short paragraph.",
			Check: All(
				Drove(3),
				FileHas("workspace/summary.txt", "Apollo", "March", "April", "June"),
			),
		},

		// --- document generation --------------------------------------------
		Benchmark{
			Name:       "report-docx-with-table",
			Category:   "doc-generation",
			Difficulty: 3,
			Setup: WriteFile("sales.txt",
				"Q1 sales: 120 units. Q2 sales: 200 units. Q3 sales: 175 units. Q4 sales: 260 units."),
			Prompt: "Read sales.txt and produce a Word document called report.docx with a title, " +
				"a short paragraph summarising the year, and a table listing each quarter and its " +
				"unit sales.",
			Timeout: 5 * time.Minute,
			Check: All(
				GlobHas("workspace/*.docx", "120", "200", "175", "260"),
			),
		},

		// --- format conversion ----------------------------------------------
		Benchmark{
			Name:       "data-to-xlsx",
			Category:   "conversion",
			Difficulty: 3,
			Setup: WriteFile("scores.csv",
				"name,score\nAda,91\nGrace,88\nLinus,95\nMargaret,79\n"),
			Prompt: "Turn scores.csv into an Excel spreadsheet called scores.xlsx with the same " +
				"data, and add a row at the bottom with the average score.",
			Timeout: 5 * time.Minute,
			Check: All(
				GlobHas("workspace/*.xlsx", "Ada", "Grace", "Linus", "Margaret"),
				// The average of 91,88,95,79 is 88.25 — a computed value proves she
				// did the arithmetic, not just copied cells.
				GlobHas("workspace/*.xlsx", "88"),
			),
		},

		// --- terminal orchestration -----------------------------------------
		Benchmark{
			Name:       "make-and-run-script",
			Category:   "terminal",
			Difficulty: 3,
			Prompt: "Create a shell script called count.sh that prints the numbers 1 to 5 each on " +
				"their own line, make it executable, run it, and save its output to out.txt.",
			Check: All(
				Drove(2),
				FileHas("workspace/out.txt", "1", "2", "3", "4", "5"),
			),
		},

		// --- multi-step persistence: the anti-chatbot test ------------------
		Benchmark{
			Name:       "five-step-pipeline",
			Category:   "persistence",
			Difficulty: 4,
			Setup:      WriteFile("input.txt", "hello world"),
			Prompt: "Do all of these in order: 1) read input.txt, 2) create step1.txt containing " +
				"the text uppercased, 3) create step2.txt containing the text reversed, 4) create " +
				"step3.txt with the number of characters, 5) create done.txt containing the word " +
				"COMPLETE. Do every step; don't stop partway.",
			Timeout: 5 * time.Minute,
			Check: All(
				FileHas("workspace/step1.txt", "HELLO WORLD"),
				FileHas("workspace/step2.txt", "dlrow olleh"),
				FileHas("workspace/step3.txt", "11"),
				FileHas("workspace/done.txt", "COMPLETE"),
				FinishedCleanly(),
			),
		},

		// --- memory / recall ------------------------------------------------
		Benchmark{
			Name:       "recall-within-task",
			Category:   "memory",
			Difficulty: 2,
			Prompt: "My project deadline is the 14th and my client is Meridian Corp. Write those " +
				"two facts into a file called facts.txt, then in the same file add a line that says " +
				"how many days until the deadline if today were the 4th.",
			Check: All(
				FileHas("workspace/facts.txt", "14", "Meridian", "10"),
			),
		},
	)
}
