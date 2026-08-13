package main

import (
	"strings"
	"unicode"
)

// What of her mid-action talk is fit to say out loud.
//
// # What was happening
//
// OnInterim carries the text a model produces alongside its tool calls, and both
// voice paths spoke it verbatim so that voice mode had no dead air. The intent is
// in the comment that was there: "on it, opening your portal" before she opens
// it. That is a good thing to say.
//
// What the model actually produces is its working-out. Measured on one real
// request — rearrange my Downloads and rename some files — the user was read
// this, aloud:
//
//	Wait, let's check if there are other images or files. Let's list all files
//	in ~/Downloads again or check if there's any other un-named or UUID-named
//	file.
//	...
//	Let's use file_move to rename 1000842027.jpg to Lobby_Event_Photo.jpg, and
//	remove the stale files.
//	Now let's remove the duplicate UUID PDF and the stale .crdownload files
//	using file_delete.
//
// Thirty lines of it, naming tools, arguing with itself, addressing nobody. The
// thinking window already has the right treatment for this — dim, marked with a
// bubble, unmistakably not speech — and interim had none of it while being the
// half that got spoken.
//
// # Why this counts rather than judges
//
// Asking a model whether its own line is worth saying is another model call in
// the middle of a turn, and it would be answering about text it just wrote. The
// difference is visible without understanding any of it: narration is one short
// sentence addressed to a person, deliberation is long, multi-line, and full of
// the names of tools. So it is measured, and the doubtful case is silence —
// nothing here is lost by not saying it, because whatever mattered arrives in the
// reply a moment later.
const (
	// narrationMax is where a spoken aside stops being an aside. "On it, opening
	// your portal" is 34 characters; the shortest deliberation line measured in
	// that Downloads run was 96 and most ran past 200.
	narrationMax = 90
)

// deliberationMarks are the shapes that only appear when a model is reasoning at
// itself rather than talking to someone.
var deliberationMarks = []string{
	"let's ", "let me check", "wait,", "wait.", "first,", "next,", "actually,",
	"i need to", "i should", "i'll use", "we can", "hmm", "okay, so", "so i",
}

// speakableNarration returns the line to say, and whether to say anything.
//
// Silence is the default for everything it cannot vouch for. A spoken line that
// should not have been spoken cannot be taken back, and the cost of missing a
// real one is a few seconds of quiet before the answer.
func speakableNarration(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return "", false
	}
	// More than one line is a train of thought, not an aside.
	if strings.ContainsAny(s, "\n\r") {
		return "", false
	}
	if len([]rune(s)) > narrationMax {
		return "", false
	}
	// Backticks and underscored_names are how a model writes a tool name, and it
	// only writes one when it is talking about its own machinery.
	if strings.Contains(s, "`") || containsToolName(s) {
		return "", false
	}
	lower := strings.ToLower(s)
	for _, m := range deliberationMarks {
		if strings.Contains(lower, m) {
			return "", false
		}
	}
	// A list, a heading, or anything else formatted for a page rather than an ear.
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, "*") || strings.HasPrefix(s, "#") ||
		(len(s) > 1 && unicode.IsDigit(rune(s[0])) && (s[1] == '.' || s[1] == ')')) {
		return "", false
	}
	return s, true
}

// containsToolName spots a lower_snake_case word, which in her vocabulary is
// always the name of a tool and never ordinary speech.
func containsToolName(s string) bool {
	for _, word := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if !strings.Contains(word, "_") {
			continue
		}
		if strings.ToLower(word) == word {
			return true
		}
	}
	return false
}
