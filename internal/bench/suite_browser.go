package bench

import (
	"fmt"
	"time"
)

// Browser benchmarks run against the local fake portal, so they are
// deterministic and check the server's own record of what she reached — not her
// reply. They are built at run time rather than registered in init, because each
// must close over the live server's address and state.
//
// These are the benchmarks the whole revamp was for: the class she failed on a
// real portal — lazy content, pagination, dynamic clicks — reproduced where it
// can be graded.

// BrowserBenchmarks builds the portal benchmarks against a running server.
func BrowserBenchmarks(ts *TestServer) []Benchmark {
	// portalPassword is what /quiz/N puts on the page; reaching the page is the
	// only way to know it, so an extract check proves she got there.
	quizPassword := func(unit int) string { return fmt.Sprintf("APOLLO-%d", unit) }

	openedQuiz := func(unit int) func(*World) (bool, string) {
		return func(w *World) (bool, string) {
			got := ts.State.OpenedQuiz.Load()
			if int(got) == unit {
				return true, fmt.Sprintf("reached the Unit %d quiz page", unit)
			}
			if got == 0 {
				return false, "never reached any quiz page — did not work through to the target"
			}
			return false, fmt.Sprintf("reached Unit %d, but the target was Unit %d", got, unit)
		}
	}

	return []Benchmark{
		{
			Name:       "portal-find-across-pagination",
			Category:   "browser",
			Difficulty: 5,
			Timeout:    5 * time.Minute,
			Prompt: fmt.Sprintf("Open %s/portal in the browser. It's a work-to-do list that loads "+
				"its items a moment after the page and is split across several pages. Find the "+
				"'Self-Quiz Unit 5 for BUS 1102 Basic Accounting' — it is NOT on the first page, so "+
				"you'll need to work the pagination — open it, and tell me the password shown on the "+
				"quiz page.", ts.URL),
			Check: func(w *World) (bool, string) {
				if ok, why := openedQuiz(5)(w); !ok {
					return false, why
				}
				// Belt and braces: the password only exists on the quiz page, so
				// finding it in her reply confirms she actually read it.
				return FinishedCleanly()(w)
			},
		},
		{
			Name:       "portal-wait-for-content",
			Category:   "browser",
			Difficulty: 3,
			Timeout:    4 * time.Minute,
			Prompt: fmt.Sprintf("Open %s/portal and tell me the exact title of the FIRST work item in "+
				"the list. The list loads a moment after the page, so make sure you're reading the "+
				"real content, not the loading placeholders.", ts.URL),
			Check: func(w *World) (bool, string) {
				// The first real item is Unit 1; a too-early read would say
				// "loading" or the skeleton glyphs.
				return ReplyHas("Unit 1")(w)
			},
		},
		{
			Name:       "portal-extract-quiz-password",
			Category:   "browser",
			Difficulty: 4,
			Timeout:    5 * time.Minute,
			Prompt: fmt.Sprintf("Go to %s/quiz/5 in the browser and read me back, exactly, the "+
				"password printed on that page.", ts.URL),
			Check: func(w *World) (bool, string) {
				return ReplyHas(quizPassword(5))(w)
			},
		},
	}
}

// ReplyHas passes when her final reply contains the needle (case-insensitive).
// Used where the proof is something only visible on the page she had to reach.
func ReplyHas(needle string) func(*World) (bool, string) {
	return func(w *World) (bool, string) {
		if containsFold(w.Reply, needle) {
			return true, "reply contains " + needle
		}
		return false, "reply does not contain " + needle + " — did not read the page"
	}
}
