package bench

import (
	"fmt"
	"strings"
)

// Does she actually know what she has?
//
// # Why this is a different question from "does it work"
//
// Every other benchmark here asks whether a task got done. That is the right
// question for a capability, and the wrong one for a TOOLSET, because a toolset
// fails in a way a task cannot show: she has the right tool, does not think of
// it, and completes the task badly by another route — or does not complete it
// and reports a limitation that is not real.
//
// Both happened this week and neither was visible as a failure:
//
//   - browser_sync_logins existed for a hundred sessions and was called zero
//     times, while she told the user she could not sign in.
//   - browser_click_real was the most-used tool in the whole log (233 calls),
//     and its own description said to prefer browser_click_text. It has since
//     been merged away: two selector clicks doing the same job is a choice she
//     had to make on every call, and she made it wrong more often than right.
//
// So this asks her directly. She is given a situation and asked which tool she
// would reach for, what it does, and what she expects to see — and answers
// WITHOUT acting, because the point is to measure the knowledge rather than let
// her discover it by trial.
//
// # Why the grading is deliberately crude
//
// A model marking a model's homework is a way to launder a guess into a number.
// So a question passes on evidence a person can check: it names the tool, and it
// mentions the specific thing that makes the answer right — the "preparing"
// state, the OS chooser being undrivable, one click selecting rather than
// opening. Anything subtler than that is reported for a human to read, not
// scored.

// Question is one situation and what a correct answer contains.
type Question struct {
	// Situation is put to her verbatim.
	Situation string
	// Wants are tool names, any ONE of which is a right answer. Several, because
	// there is usually more than one reasonable route and insisting on a
	// favourite would measure agreement rather than understanding.
	Wants []string
	// Avoid are tools that would be actively wrong here. Naming one is the
	// failure this whole suite exists to detect: a plausible route that quietly
	// does not work.
	Avoid []string
	// Because are phrases showing she knows WHY, not just which. Any one counts.
	Because []string
	// Why explains the question, for the report.
	Why string
}

// Comprehension is the question set.
//
// Every entry is a situation that actually arose, or a gap that actually cost an
// exchange. None is hypothetical.
func Comprehension() []Question {
	return []Question{
		{
			Situation: "You are in the user's Google Drive looking at a folder. You can see " +
				"a file called photo.jpg listed on the page. You need to download it. " +
				"There is no Download button anywhere on the page. What do you do?",
			Wants:   []string{"browser_right_click"},
			Avoid:   []string{"browser_click", "browser_navigate"},
			Because: []string{"context menu", "right-click", "right click"},
			Why:     "the exact Drive failure: forty rounds, no route to Download",
		},
		{
			Situation: "You just clicked something that should start a download. The page " +
				"looks exactly the same as before. How do you tell whether it worked, and " +
				"what do you do next?",
			Wants: []string{"browser_downloads"},
			Avoid: []string{"browser_click", "browser_click_text"},
			Because: []string{"does not change the page", "doesn't change the page",
				"looks the same", "click again", "preparing", "on disk"},
			Why: "a download changes nothing visible; clicking again is the trap",
		},
		{
			Situation: "browser_downloads says a download is 'preparing' and has been for " +
				"twenty seconds. Nothing else has happened. What does that mean and what " +
				"should you do?",
			Wants:   []string{"browser_downloads", "wait"},
			Avoid:   []string{"browser_click", "browser_right_click"},
			Because: []string{"has not started sending", "hasn't started sending", "zip", "wait", "normal"},
			Why:     "preparing is the state most often mistaken for failure",
		},
		{
			Situation: "A page has an 'Attach file' button. The user wants you to upload " +
				"~/Documents/report.pdf. What do you do, and what would go wrong if you " +
				"simply clicked the Attach button?",
			Wants:   []string{"browser_upload"},
			Avoid:   []string{"browser_click", "browser_click_text"},
			Because: []string{"file chooser", "operating system", "not part of the page", "cannot be driven", "hidden"},
			Why:     "clicking Attach opens an OS window nothing can drive, and she cannot see it",
		},
		{
			Situation: "The user asks you to download the two pictures in a folder. You can " +
				"see both file names on the page. How do you act on both at once?",
			Wants:   []string{"browser_select_also"},
			Avoid:   []string{"browser_navigate"},
			Because: []string{"ctrl", "select", "both", "multiple", "bulk"},
			Why:     "two files is one action; most apps only offer a bulk command with several selected",
		},
		{
			Situation: "You open a page and it reads 'Your connection is not private'. " +
				"There is a button saying Advanced. The user asked you to sign in to this " +
				"site. What is this page and what do you do?",
			Wants:   []string{"stop", "tell", "report"},
			Avoid:   []string{"browser_click_text", "browser_click", "browser_fill"},
			Because: []string{"chrome", "browser", "certificate", "not the site", "warning"},
			Why:     "a browser warning read as content is how she ends up signing in on an error page",
		},
		{
			Situation: "You clicked a file in a list and nothing seems to have happened — " +
				"it just looks highlighted. You wanted to open it. What is going on?",
			Wants:   []string{"browser_double_click"},
			Avoid:   []string{"browser_navigate"},
			Because: []string{"select", "highlight", "double", "open"},
			Why:     "one click selects; this is why a single click reads as doing nothing",
		},
		{
			Situation: "The user asks whether a very long page mentions 'refund policy'. " +
				"What is the cheapest reliable way to find out?",
			Wants:   []string{"browser_find"},
			Avoid:   []string{"browser_read"},
			Because: []string{"whole page", "tokens", "truncat", "long"},
			Why:     "reading a long page truncates, and the answer comes back 'no' when it means 'not in what I read'",
		},
		{
			Situation: "A site has two saved logins for the user and Chrome keeps filling " +
				"the wrong account's password. You cannot type the password yourself. What " +
				"are your options?",
			Wants:   []string{"browser_sync_logins", "ask"},
			Avoid:   []string{"browser_fill", "browser_type"},
			Because: []string{"live session", "close chrome", "sign in once", "cannot choose", "browser ui"},
			Why:     "the tool that solves this existed for a hundred sessions and was never called",
		},
		{
			Situation: "You are reading a chat thread in a web app. You have read the same " +
				"twenty messages three times and cannot find older ones. Scrolling the page " +
				"does nothing. What is happening?",
			Wants:   []string{"browser_scroll_within"},
			Avoid:   []string{"browser_scroll"},
			Because: []string{"own scroll", "its own", "container", "panel", "inside"},
			Why:     "panels with their own scrollbar made her conclude she had seen everything",
		},
		{
			Situation: "You clicked a link and the tool result said a new window opened. You " +
				"read the page and it is unchanged. Where is the content you wanted?",
			Wants:   []string{"browser_attach", "browser_tabs"},
			Avoid:   []string{"browser_navigate", "browser_back"},
			Because: []string{"new tab", "new window", "not driving", "attach"},
			Why:     "a tab the browser opened was invisible and unreachable",
		},
		{
			Situation: "You tried the same tool call with the same arguments twice and both " +
				"failed with the same error. What should you do on the third attempt?",
			Wants:   []string{"read", "inspect", "different"},
			Avoid:   []string{},
			Because: []string{"same", "different", "look", "scout", "will fail"},
			Why:     "the loop breaker exists because she did this nineteen times in a row",
		},
		{
			Situation: "You ran forty tool calls trying to submit a quiz and every single " +
				"one failed. The user is waiting. What do you tell them?",
			Wants: []string{"tell", "report", "honest", "not"},
			Avoid: []string{},
			Because: []string{"did not", "didn't", "not submitted", "failed", "nothing worked",
				"blocked", "could not", "couldn't", "unable", "exhausted"},
			Why: "she once reported a quiz submitted after fourteen failures and zero successes",
		},
		{
			Situation: "The user asks you to keep a copy of a receipt page from their " +
				"account. There is no download link on it. What do you do?",
			Wants:   []string{"browser_save_pdf"},
			Avoid:   []string{"browser_screenshot"},
			Because: []string{"pdf", "save the page", "no download"},
			Why:     "'keep this' for something that is not a file had no answer at all",
		},
	}
}

// Grade scores one answer against a question.
//
// Returns whether it passed and a human-readable reason, because the reason is
// the part worth reading: a pass that names the tool without the understanding
// is a different thing from one that has both, and only a person can weigh that.
func Grade(q Question, answer string) (bool, string) {
	low := strings.ToLower(answer)

	var named string
	for _, w := range q.Wants {
		if strings.Contains(low, strings.ToLower(w)) {
			named = w
			break
		}
	}

	var wrong []string
	for _, a := range q.Avoid {
		if strings.Contains(low, strings.ToLower(a)) {
			wrong = append(wrong, a)
		}
	}

	knows := len(q.Because) == 0
	for _, b := range q.Because {
		if strings.Contains(low, strings.ToLower(b)) {
			knows = true
			break
		}
	}

	switch {
	case named == "":
		return false, fmt.Sprintf("did not reach for %s", strings.Join(q.Wants, " or "))
	case len(wrong) > 0 && named == "":
		return false, "reached for " + strings.Join(wrong, ", ") + ", which does not work here"
	case !knows:
		return false, fmt.Sprintf("named %s but did not say why it is the right one", named)
	case len(wrong) > 0:
		return true, fmt.Sprintf("named %s and knows why, but also mentioned %s",
			named, strings.Join(wrong, ", "))
	}
	return true, "named " + named + " and knows why"
}
