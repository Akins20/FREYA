package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/voice"
)

// Confirming a risky action out loud.
//
// # Why this exists
//
// She is used by voice, with no terminal. Routine work runs on its own, but a
// genuinely destructive action — deleting data, touching a system path, reaching
// for root — should still be checked, and the only channel to check on is the
// one the user is actually using: speech. So instead of denying such an action
// because nobody is at a keyboard, she asks about it aloud and listens for the
// answer, exactly as a person would. "This will delete forty files. Go ahead?"
//
// A silence, or anything that is not a clear yes, is treated as no. Erring
// toward not-doing is correct here: the whole reason she is asking is that the
// action is hard to undo.

// voiceConfirm builds a spoken confirmation function bound to a voice session.
func voiceConfirm(vs *voiceState) guard.ConfirmFunc {
	return func(ctx context.Context, action guard.Action, a guard.Assessment) bool {
		if vs == nil || vs.session == nil {
			return false // no voice to ask with; the safe default holds
		}

		question := "Quick check before I do this. " + spokenEffect(action, a) +
			" Should I go ahead?"
		_ = vs.session.Speak(ctx, question)

		for attempt := 0; attempt < 2; attempt++ {
			answer := recordAnswer(ctx, vs)
			if decided, yes := parseYesNo(answer); decided {
				if yes {
					_ = vs.session.Speak(ctx, "Okay, doing it.")
				} else {
					_ = vs.session.Speak(ctx, "Okay, I'll leave it.")
				}
				return yes
			}
			if attempt == 0 {
				_ = vs.session.Speak(ctx, "Sorry — was that a yes or a no?")
			}
		}

		_ = vs.session.Speak(ctx, "I didn't catch a clear yes, so I'm leaving it for now.")
		return false
	}
}

// recordAnswer captures one spoken reply, immediately — no key press, because
// she has just asked and is listening for the answer.
func recordAnswer(ctx context.Context, vs *voiceState) string {
	dir := os.Getenv("TMPDIR")
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, fmt.Sprintf("freya-answer-%d.ogg", time.Now().UnixNano()))
	defer os.Remove(path)

	// The push-to-talk recorder starts at once and stops on the pause — the right
	// shape for a short yes/no, and it does not depend on the onset detection
	// that is unreliable on this hardware.
	if err := (voice.PushToTalkRecorder{}).Record(ctx, path); err != nil {
		return ""
	}
	if !voice.HasSpeechEnergy(ctx, path) {
		return ""
	}
	answer, err := vs.session.Recognizer.Transcribe(ctx, path)
	if err != nil {
		return ""
	}
	return answer
}

// spokenEffect turns a guard assessment into a plain sentence a person can act
// on, favouring the concrete preview over the internal rule name.
func spokenEffect(action guard.Action, a guard.Assessment) string {
	if p := strings.TrimSpace(a.Preview); p != "" {
		return "This will " + humanisePreview(p) + "."
	}
	if r := strings.TrimSpace(action.Reason); r != "" {
		return strings.ToUpper(r[:1]) + r[1:] + "."
	}
	return "This is a " + a.Risk.String() + "-risk action."
}

// humanisePreview softens a terse preview ("delete /a/b (3 files)") into
// something that reads aloud without sounding like a log line.
func humanisePreview(p string) string {
	p = strings.TrimPrefix(p, "would ")
	// Lead with the verb in lower case; the sentence already starts "This will".
	if len(p) > 0 {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	return p
}

// affirmative and negative words, checked as whole words against the answer.
var (
	yesWords = []string{"yes", "yeah", "yep", "yup", "sure", "okay", "ok", "go",
		"proceed", "do", "confirm", "affirmative", "please", "correct", "right", "fine"}
	noWords = []string{"no", "nope", "nah", "don't", "dont", "stop", "cancel",
		"wait", "negative", "hold", "leave", "skip", "abort"}
)

// parseYesNo interprets a spoken answer. The first return says whether the answer
// was decisive at all; the second is the decision when it was.
//
// Negatives are checked first, because "no, don't do that" contains no yes but a
// clear no, and "no" must win over an incidental "okay" elsewhere in the phrase.
func parseYesNo(answer string) (decided, yes bool) {
	words := strings.FieldsFunc(strings.ToLower(answer), func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	})
	for _, w := range words {
		for _, n := range noWords {
			if w == n {
				return true, false
			}
		}
	}
	for _, w := range words {
		for _, y := range yesWords {
			if w == y {
				return true, true
			}
		}
	}
	return false, false
}
