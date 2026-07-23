package bench

import "time"

// Advanced file-ops benchmarks: the kind of dreary, exacting file surgery a
// person actually hands an assistant — reorganise a dumping-ground folder,
// rename by a rule, strip duplicates, fold many files into one, split one into
// many, and edit a config in place without disturbing the rest.
//
// Every check here anchors on something a plausible-but-wrong answer could not
// contain: a computed sum that is only reachable after a correct de-dup, a
// region total that is wrong if the split leaked rows, an untouched config line
// that vanishes if she rewrote the file instead of editing it. Presence-only
// checks are avoided precisely because they pass on a no-op.

func init() {
	Register(
		// --- rename by a rule ------------------------------------------------
		// The rule is a transform (uppercase the client, prefix INV-), so the
		// new names can only be produced by applying it, not by copying.
		Benchmark{
			Name:       "rename-invoices-by-rule",
			Category:   "file-ops",
			Difficulty: 2,
			Setup: Setups(
				WriteFile("invoices/invoice_acme.txt", "Invoice for Acme. Amount due 500. MARK_ACME\n"),
				WriteFile("invoices/invoice_globex.txt", "Invoice for Globex. Amount due 750. MARK_GLOBEX\n"),
				WriteFile("invoices/invoice_initech.txt", "Invoice for Initech. Amount due 300. MARK_INITECH\n"),
			),
			Prompt: "The invoices folder has files named invoice_<client>.txt. Rename every one " +
				"so the client name is in capitals and it's prefixed with INV- — for example " +
				"invoice_acme.txt should become INV-ACME.txt. Leave the file contents unchanged.",
			Check: All(
				Drove(2),
				FileHas("workspace/invoices/INV-ACME.txt", "MARK_ACME"),
				FileHas("workspace/invoices/INV-GLOBEX.txt", "MARK_GLOBEX"),
				FileHas("workspace/invoices/INV-INITECH.txt", "MARK_INITECH"),
			),
		},

		// --- reorganise a folder by file type --------------------------------
		// Each file carries a unique marker, and the glob pins the exact
		// destination subfolder, so a file left unmoved (or filed under the
		// wrong type) fails its check.
		Benchmark{
			Name:       "reorganise-inbox-by-type",
			Category:   "file-ops",
			Difficulty: 3,
			Setup: Setups(
				WriteFile("inbox/roadmap.md", "# Roadmap\nShips in Q4. MARK_ROADMAP\n"),
				WriteFile("inbox/notes.md", "Standup notes for Monday. MARK_NOTES\n"),
				WriteFile("inbox/sales.csv", "region,amount\nWest,500,MARK_SALES\n"),
				WriteFile("inbox/signups.csv", "email,date\na@b.com,2026-01-01,MARK_SIGNUPS\n"),
				WriteFile("inbox/error.log", "ERROR db timeout MARK_ERRLOG\n"),
				WriteFile("inbox/access.log", "GET /home 200 MARK_ACCLOG\n"),
			),
			Prompt: "The inbox folder is a dumping ground of loose files. Inside inbox, make three " +
				"subfolders named docs, data, and logs, then move every .md file into docs, every " +
				".csv file into data, and every .log file into logs.",
			Check: All(
				Drove(2),
				GlobHas("workspace/inbox/docs/*.md", "MARK_ROADMAP"),
				GlobHas("workspace/inbox/docs/*.md", "MARK_NOTES"),
				GlobHas("workspace/inbox/data/*.csv", "MARK_SALES"),
				GlobHas("workspace/inbox/data/*.csv", "MARK_SIGNUPS"),
				GlobHas("workspace/inbox/logs/*.log", "MARK_ERRLOG"),
				GlobHas("workspace/inbox/logs/*.log", "MARK_ACCLOG"),
			),
		},

		// --- dedupe --------------------------------------------------------
		// 12 lines, 8 distinct. The distinct addresses are still all present
		// even if she leaves the duplicates in, so the count.txt value is the
		// only thing that distinguishes a real de-dup from a copy — and it must
		// be computed, not copied.
		Benchmark{
			Name:       "dedupe-email-list",
			Category:   "file-ops",
			Difficulty: 3,
			Setup: WriteFile("emails.txt",
				"alice@x.com\nbob@y.com\nalice@x.com\ncarol@z.com\ndave@w.com\nbob@y.com\n"+
					"eve@v.com\nfrank@u.com\ncarol@z.com\ngrace@t.com\nheidi@s.com\nalice@x.com\n"),
			Prompt: "emails.txt has a pile of email addresses, one per line, with duplicates. Write " +
				"the de-duplicated list to unique.txt — each address exactly once, keeping the order " +
				"of first appearance — and write just the number of unique addresses to count.txt.",
			Check: All(
				Drove(2),
				FileHas("workspace/unique.txt",
					"alice@x.com", "bob@y.com", "carol@z.com", "dave@w.com",
					"eve@v.com", "frank@u.com", "grace@t.com", "heidi@s.com"),
				// 8 distinct addresses; a kept-the-duplicates answer writes 12.
				FileHas("workspace/count.txt", "8"),
			),
		},

		// --- merge many files into one -------------------------------------
		// Three CSV parts sharing one header, folded into one file with a
		// computed grand total. 10+5+8+12+3+7 = 45.
		Benchmark{
			Name:       "merge-csv-parts",
			Category:   "file-ops",
			Difficulty: 3,
			Setup: Setups(
				WriteFile("parts/part1.csv", "item,qty\napple,10\nbanana,5\n"),
				WriteFile("parts/part2.csv", "item,qty\ncherry,8\ndate,12\n"),
				WriteFile("parts/part3.csv", "item,qty\nfig,3\ngrape,7\n"),
			),
			Prompt: "The parts folder has several CSV files that all share the same 'item,qty' header. " +
				"Merge them into one file called all.csv with the header appearing only once at the top, " +
				"every data row from all the files beneath it, and a final row that reads TOTAL followed " +
				"by the sum of every quantity.",
			Check: All(
				Drove(2),
				FileHas("workspace/all.csv", "apple", "banana", "cherry", "date", "fig", "grape"),
				// The sum, 45, proves she added the quantities rather than
				// guessing; no single quantity equals it.
				FileHas("workspace/all.csv", "TOTAL", "45"),
			),
		},

		// --- split a file into many ----------------------------------------
		// One orders file split into a file per region, each with its own
		// region subtotal. If rows leak across regions, or she copies the whole
		// file into each, the per-region total comes out wrong:
		// North 100+150=250, South 200+80=280, West 300+120=420.
		Benchmark{
			Name:       "split-orders-by-region",
			Category:   "file-ops",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: WriteFile("orders.csv",
				"id,region,amount\n1,North,100\n2,South,200\n3,North,150\n"+
					"4,West,300\n5,South,80\n6,West,120\n"),
			Prompt: "Split orders.csv into one file per region — north.csv, south.csv and west.csv — " +
				"each containing only that region's orders. At the bottom of each file add a TOTAL row " +
				"with the sum of just that region's amounts.",
			Check: All(
				Drove(2),
				FileHas("workspace/north.csv", "100", "150", "250"),
				FileHas("workspace/south.csv", "200", "80", "280"),
				FileHas("workspace/west.csv", "300", "120", "420"),
				FinishedCleanly(),
			),
		},

		// --- surgical edit preserving the rest -----------------------------
		// Two values change; everything else must survive verbatim. If she
		// rewrote the file from memory instead of editing in place, the
		// untouched lines drift and the preservation needles fail.
		Benchmark{
			Name:       "surgical-config-edit",
			Category:   "file-ops",
			Difficulty: 3,
			Setup: WriteFile("config.ini",
				"[server]\nhost = localhost\nport = 8080\ntimeout = 30\nmax_connections = 100\n\n"+
					"[logging]\nlevel = info\nfile = /var/log/app.log\nrotate = daily\n\n"+
					"[cache]\nenabled = true\nttl = 3600\n"),
			Prompt: "In config.ini, change the port to 9090 and change the logging level to debug. " +
				"Don't touch anything else in the file.",
			Check: All(
				Drove(1),
				// The two edits landed.
				FileHas("workspace/config.ini", "9090", "debug"),
				// Everything untouched is still exactly there.
				FileHas("workspace/config.ini",
					"host = localhost", "timeout = 30", "max_connections = 100",
					"file = /var/log/app.log", "rotate = daily", "enabled = true", "ttl = 3600"),
			),
		},

		// --- capstone: dedupe + reorganise + split + aggregate -------------
		// The dump has exact-duplicate receipts (r3=r1, r6=r2). Correct work is
		// the only path to the per-category totals: unique receipts are
		// travel 320+60=380, meals 45+30=75, office 85, grand total 540. Keeping
		// the duplicates gives travel 700 and a total of 905 — both miss.
		Benchmark{
			Name:       "dedupe-and-file-receipts",
			Category:   "file-ops",
			Difficulty: 5,
			Timeout:    5 * time.Minute,
			Setup: Setups(
				WriteFile("dump/r1.txt", "category: travel\nmerchant: Delta\namount: 320\nID: RCPT_A\n"),
				WriteFile("dump/r2.txt", "category: meals\nmerchant: Chipotle\namount: 45\nID: RCPT_B\n"),
				WriteFile("dump/r3.txt", "category: travel\nmerchant: Delta\namount: 320\nID: RCPT_A\n"),
				WriteFile("dump/r4.txt", "category: travel\nmerchant: Uber\namount: 60\nID: RCPT_C\n"),
				WriteFile("dump/r5.txt", "category: meals\nmerchant: Sweetgreen\namount: 30\nID: RCPT_D\n"),
				WriteFile("dump/r6.txt", "category: meals\nmerchant: Chipotle\namount: 45\nID: RCPT_B\n"),
				WriteFile("dump/r7.txt", "category: office\nmerchant: Staples\namount: 85\nID: RCPT_E\n"),
			),
			Prompt: "The dump folder has receipt files and some are exact duplicates of each other. " +
				"Remove the duplicates, then sort the unique receipts into subfolders named travel, " +
				"meals and office by their category line. Finally write summary.txt giving the total " +
				"amount spent in each category and the grand total across all of them.",
			Check: All(
				Drove(3),
				GlobHas("workspace/travel/*.txt", "RCPT_A"),
				GlobHas("workspace/travel/*.txt", "RCPT_C"),
				GlobHas("workspace/meals/*.txt", "RCPT_B"),
				GlobHas("workspace/meals/*.txt", "RCPT_D"),
				GlobHas("workspace/office/*.txt", "RCPT_E"),
				// Per-category and grand totals, only reachable after a correct
				// de-dup and sum: travel 380, meals 75, office 85, total 540.
				FileHas("workspace/summary.txt", "380", "75", "85", "540"),
				FinishedCleanly(),
			),
		},
	)
}
