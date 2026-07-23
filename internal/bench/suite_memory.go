package bench

import "time"

// Memory benchmarks.
//
// The capability under test is holding facts and using them: she is handed a
// set of facts — stated in the prompt or sitting in a file she has to read —
// and must (a) commit them to a file verbatim, (b) compute something from them
// that was never stated, and (c) carry an earlier fact forward to a later step
// of the same task without it being repeated. A chatbot can echo the facts back
// in a reply; only an agent that actually stored them and did the arithmetic
// lands the derived figure on disk.
//
// Every check anchors on a value that is reachable ONLY by remembering the right
// fact and computing with it — a difference, a subtotal, a remainder, a reserve —
// and each such anchor is chosen so it is NOT a substring of any raw fact. The
// stated facts ride alongside as needles to prove they were written down, but the
// pass/fail hinges on the computed anchor: forget a fact, re-read the wrong one,
// or skip the arithmetic and it comes out wrong and misses. FinishedCleanly()
// rides on the multi-step tasks, where the tempting failure is to store the facts
// and then trail off before using them.

func init() {
	Register(
		// --- Difficulty 2: record two facts, derive the one between them ------
		// 48210 - 47890 = 320. The two odometer readings are the facts to store;
		// 320 is the miles driven, a figure that appears in neither reading, so it
		// can only come from subtracting the one she has to remember from the other.
		Benchmark{
			Name:       "odometer-trip-miles",
			Category:   "memory",
			Difficulty: 2,
			Prompt: "I just filled up the tank. The odometer reads 48210 miles, and at my last fill-up " +
				"it read 47890. Save both readings to trip.txt, and add a line for how many miles I " +
				"drove between the two fill-ups.",
			Check: All(
				Drove(1),
				// 320 = 48210-47890; it is a substring of neither reading, so a reply
				// that stored the numbers without subtracting them cannot produce it.
				FileHas("trip.txt", "48210", "47890", "320"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 2: store a small inventory of facts, then total it ----
		// 3+5+4 = 12. The three category counts are the facts; 12 is the total,
		// which is not any single count, so it proves she added the list she stored.
		Benchmark{
			Name:       "board-game-collection-count",
			Category:   "memory",
			Difficulty: 2,
			Prompt: "Help me catalogue my board games. I own 3 strategy games, 5 party games and 4 card " +
				"games. Write collection.txt listing those three counts, and add a line with the total " +
				"number of games I own across all categories.",
			Check: All(
				Drove(1),
				// 12 = 3+5+4, present in no single count; the category words prove the
				// stored facts survived into the file rather than a bare number.
				FileHas("collection.txt", "strategy", "party", "card", "3", "5", "4", "12"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: hold a fact from step one, spend against it later --
		// 8 guests and a 200 budget are established up front; the per-head costs come
		// later and must be applied to that remembered head count: food 12*8=96,
		// drinks 5*8=40, total 136, leaving 200-136 = 64. The 8 is never repeated in
		// the costing step, so 136 and 64 only come out right if she carried it.
		Benchmark{
			Name:       "dinner-party-budget-recall",
			Category:   "memory",
			Difficulty: 3,
			Prompt: "I'm hosting a dinner party for 8 people on a 200 dollar budget — save those two " +
				"details to party.txt. Now, I want to spend 12 dollars a head on food and 5 a head on " +
				"drinks: work out the total cost for everyone, add it to party.txt, and note how much " +
				"of my budget is left over.",
			Check: All(
				Drove(1),
				// 136 = 12*8 + 5*8 and 64 = 200-136; both depend on recalling the 8
				// guests from the first sentence, and neither is a substring of any fact.
				FileHas("party.txt", "8", "200", "136", "64"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 3: read facts from a file, apply a rule she must recall
		// Dana works 42 hours at 28/hr with time-and-a-half past 40. Regular pay
		// 40*28 = 1120; overtime 2 hours * (28*1.5=42) = 84; gross 1120+84 = 1204.
		// Paying all 42 hours flat gives 1176, so the 84 and 1204 together prove the
		// overtime rule from the file was actually remembered and applied.
		Benchmark{
			Name:       "weekly-paystub-overtime",
			Category:   "memory",
			Difficulty: 3,
			Setup: WriteFile("employee.txt",
				"Name: Dana Reyes\n"+
					"Hourly rate: 28\n"+
					"Hours worked this week: 42\n"+
					"Overtime rule: hours over 40 are paid at 1.5x\n"),
			Prompt: "employee.txt has Dana's pay details for the week. Work out her gross pay — regular " +
				"hours at her normal rate, plus time-and-a-half on every hour past 40 — and write " +
				"paystub.txt with her name, the regular and overtime amounts broken out, and the gross " +
				"total.",
			Check: All(
				Drove(2),
				// 1120 regular, 84 overtime, 1204 gross — none is a substring of the
				// raw rate/hours, so each is proof of the correct computation.
				FileHas("paystub.txt", "Dana", "1120", "84", "1204"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: gather scattered facts, subtract, aggregate to docx
		// Each goal file gives a target and an amount saved; the remaining need is
		// target-saved: vacation 1150, laptop 450, emergency 1800, and the total
		// still needed is 3400. Every one of those figures is absent from the raw
		// files, so all four ride on reading all three goals and subtracting each.
		Benchmark{
			Name:       "savings-goals-remaining-docx",
			Category:   "memory",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Setup: Setups(
				WriteFile("goals/vacation.txt", "Goal: Vacation\nTarget: 3000\nSaved: 1850\n"),
				WriteFile("goals/laptop.txt", "Goal: Laptop\nTarget: 1400\nSaved: 950\n"),
				WriteFile("goals/emergency.txt", "Goal: Emergency Fund\nTarget: 5000\nSaved: 3200\n"),
			),
			Prompt: "The goals folder has one file per savings goal, each with a target and how much I've " +
				"saved so far. Pull them all together into a Word document called savings.docx that " +
				"lists each goal with how much more I still need to reach its target, and finish with " +
				"the total amount still to save across all of them.",
			Check: All(
				Drove(3),
				// 1150/450/1800 are per-goal shortfalls, 3400 the grand shortfall;
				// none appears in any source file, so all four require the subtractions.
				GlobHas("workspace/*.docx",
					"Vacation", "Laptop", "Emergency", "1150", "450", "1800", "3400"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 4: store facts, compute a derived column into a sheet -
		// Four trips with miles at 0.65/mile: 32,47,28,55 → 162 miles → 162*0.65 =
		// 105.3 total reimbursement. The total is a substring of no fact, so it is
		// reachable only by remembering every trip and applying the rate to the sum.
		Benchmark{
			Name:       "mileage-reimbursement-xlsx",
			Category:   "memory",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Prompt: "Set up a spreadsheet called mileage.xlsx logging my client visits this week: Monday " +
				"32 miles to Acme, Tuesday 47 to Globex, Wednesday 28 to Initech, Thursday 55 to " +
				"Umbrella. At 65 cents a mile, give each trip a reimbursement column and add a final " +
				"row with the total reimbursement for the week.",
			Check: All(
				Drove(1),
				// The four clients and their miles must all be logged, and 105.3 is the
				// total reimbursement (162 miles * 0.65) — wrong the moment a trip is lost.
				GlobHas("workspace/*.xlsx",
					"Acme", "Globex", "Initech", "Umbrella",
					"32", "47", "28", "55", "105.3"),
				FinishedCleanly(),
			),
		},

		// --- Difficulty 5: carry a rule from the top of the task to the bottom
		// The rent (1800) and the 15% maintenance rule are set at the start and must
		// be recalled twice: to compute the reserve 0.15*1800 = 270, and again in the
		// final document. Actual costs 240+90+130 = 460 exceed the reserve, a 190
		// shortfall, so the reserve was NOT enough. 270, 460 and 190 each appear in
		// no raw fact, and every one depends on a fact stated only once, far earlier.
		Benchmark{
			Name:       "rental-reserve-reconcile",
			Category:   "memory",
			Difficulty: 5,
			Timeout:    5 * time.Minute,
			Prompt: "I'm reconciling my rental property this month. Remember two things: the monthly rent " +
				"is 1800, and I set aside 15% of the rent for maintenance. First save the rent and that " +
				"15% rule to property.txt. This month's maintenance costs were plumbing 240, cleaning " +
				"90 and insurance 130 — work out my maintenance reserve for the month, add up the actual " +
				"costs, and record in property.txt the reserve amount, the total costs, the difference " +
				"between them, and whether the reserve covered the costs. Then produce a one-page " +
				"summary as report.docx repeating the rent, the reserve, the total costs and whether I " +
				"was covered.",
			Check: All(
				Drove(2),
				// 270 = 15% of 1800, 460 = 240+90+130, 190 = 460-270 shortfall; all
				// three hinge on the rent and the rule stated once at the very start.
				FileHas("property.txt", "1800", "270", "460", "190"),
				// The document has to restate the recalled figures, not just the file.
				GlobHas("workspace/*.docx", "1800", "270", "460"),
				FinishedCleanly(),
			),
		},
	)
}
