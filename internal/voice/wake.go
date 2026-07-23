package voice

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Wake-word listening.
//
// # What this actually costs
//
// There is no local wake-word model here. Detecting "hey Freya" on-device
// properly needs a trained keyword spotter — Porcupine, openWakeWord — which
// means a dependency and a model download, and the MFCC work already in this
// package was measured and found too weak for a job of this kind.
//
// So the gate is energy plus silence: sox records only while someone is
// actually speaking, and each utterance is transcribed and checked for the wake
// word. That is honest but has a real consequence worth stating plainly —
// **while listening is on, speech near the microphone is sent for
// transcription, whether or not it was addressed to Freya**. It is off by
// default for that reason, and it stops itself after a period of quiet rather
// than running forever because somebody forgot.
//
// # Why the matching is fuzzy
//
// Speech recognition does not reliably render an invented name. Measured
// variants of "Freya" include Freyja, Fraya, Freyer, Frey a and friar. Matching
// the exact spelling would produce an assistant that ignores you most of the
// time, which is worse than occasionally waking when it should not.

// wakeVariants are the renderings a recogniser plausibly produces for the name.
var wakeVariants = []string{
	"freya", "freyja", "fraya", "frayer", "freyer", "freia", "frey a",
	"friar", "fryer", "freyah", "frida",
}

// WakeHint is the vocabulary hint handed to the transcriber so it spells the
// name rather than guessing at it.
//
// # A hint, not a template
//
// The first version of this described "someone addressing an assistant named
// Freya, possibly followed by a request", and that clause was a catastrophe: it
// handed the model a fill-in-the-blank form, and given silence or noise the
// model filled it in — "Freya, play music", "Freya, set an alarm for 7 AM" —
// complete fabricated commands that each fired a false wake and wrote to memory.
// A hint must help the model spell a word it actually hears. It must never
// describe the shape of an utterance, because the model will happily produce
// that shape from nothing.
//
// So this names the word and its spellings, and does exactly one more thing:
// insists on silence for silence. Paired with the energy gate that keeps
// non-speech from arriving here at all, that is enough.
func WakeHint() string {
	return "The audio may contain the name Freya (also spelled Freyja or Fraya). " +
		"Transcribe only speech that is actually present; if there is no clear " +
		"speech, output nothing."
}

// wakePrefixes are the forms of address that precede the name.
var wakePrefixes = []string{"hey", "hi", "hello", "ok", "okay", "yo"}

// DefaultInactivityTimeout stops listening after a stretch of nothing.
//
// Listening indefinitely is a decision someone makes once and then forgets they
// made; a timeout means the quiet default reasserts itself.
const DefaultInactivityTimeout = 2 * time.Hour

// maxUtterance bounds a single captured segment.
const maxUtterance = 30 * time.Second

// Wake is a detected address, with whatever followed it.
type Wake struct {
	// Phrase is what triggered detection.
	Phrase string
	// Command is the rest of the utterance, if the user kept talking.
	Command string
	// Transcript is everything that was heard.
	Transcript string
	Heard      time.Time
}

// Listener watches for the wake word continuously.
type Listener struct {
	Recorder   Recorder
	Recognizer Recognizer
	// InactivityTimeout stops listening after this long with no wake. Zero uses
	// the default.
	InactivityTimeout time.Duration
	// Indefinite disables the timeout entirely.
	Indefinite bool
	// TempDir holds captured segments.
	TempDir string

	// OnWake fires when the wake word is heard.
	OnWake func(Wake)
	// OnHeard fires for every transcript, wake word or not, for tracing.
	OnHeard func(text string, woke bool)
	// OnStop explains why listening ended.
	OnStop func(reason string)
	// OnTrouble reports a fault that is not stopping the loop but means nothing
	// is being heard.
	//
	// Without this the failure mode is silence that looks exactly like nobody
	// speaking: a recorder that cannot write its output fails identically every
	// time, and the backoff below turns that into an assistant which is
	// attentively deaf and says nothing about it.
	OnTrouble func(err error, consecutive int)

	mu        sync.Mutex
	listening bool
	lastWake  time.Time
	heard     int
	woken     int
	cancel    context.CancelFunc
}

// Listening reports whether the loop is running.
func (l *Listener) Listening() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listening
}

// Stats reports how much has been heard and how often it woke.
func (l *Listener) Stats() (heard, woken int, since time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastWake.IsZero() {
		return l.heard, l.woken, 0
	}
	return l.heard, l.woken, time.Since(l.lastWake)
}

// Start begins listening. It returns immediately; the loop runs until Stop is
// called, the context ends, or the inactivity timeout elapses.
func (l *Listener) Start(ctx context.Context) error {
	l.mu.Lock()
	if l.listening {
		l.mu.Unlock()
		return fmt.Errorf("already listening")
	}
	if l.Recorder == nil || l.Recognizer == nil {
		l.mu.Unlock()
		return fmt.Errorf("listening needs a recorder and a recogniser")
	}
	l.listening = true
	l.lastWake = time.Now()
	loopCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.mu.Unlock()

	go l.loop(loopCtx)
	return nil
}

// Stop ends listening.
func (l *Listener) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.listening = false
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *Listener) loop(ctx context.Context) {
	timeout := l.InactivityTimeout
	if timeout == 0 {
		timeout = DefaultInactivityTimeout
	}

	dir := l.TempDir
	if dir == "" {
		dir = os.TempDir()
	}

	// Consecutive recorder failures. Reset by any successful capture, so a
	// genuinely intermittent device never trips the report.
	failures := 0

	defer func() {
		l.mu.Lock()
		l.listening = false
		l.mu.Unlock()
	}()

	for {
		if ctx.Err() != nil {
			l.stopped("stopped")
			return
		}

		if !l.Indefinite {
			l.mu.Lock()
			idle := time.Since(l.lastWake)
			l.mu.Unlock()
			if idle > timeout {
				l.stopped(fmt.Sprintf("no wake word for %s — listening off",
					idle.Round(time.Minute)))
				return
			}
		}

		// The recorder blocks until someone speaks and stops when they stop,
		// so this loop costs nothing while the room is quiet.
		path := filepath.Join(dir, fmt.Sprintf("freya-wake-%d.ogg", time.Now().UnixNano()))
		recCtx, cancel := context.WithTimeout(ctx, maxUtterance+15*time.Second)
		err := l.Recorder.Record(recCtx, path)
		cancel()

		if err != nil {
			os.Remove(path)
			if ctx.Err() != nil {
				l.stopped("stopped")
				return
			}
			// A recorder failure is usually transient — a device briefly busy —
			// so back off rather than spinning. But "usually" is doing a lot of
			// work: a misconfigured sandbox or a missing device fails this way
			// every single time, and staying quiet about it is how listening
			// comes to mean nothing at all.
			failures++
			if l.OnTrouble != nil && (failures == 3 || failures%60 == 0) {
				l.OnTrouble(err, failures)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		failures = 0
		if !fileHasAudio(path) {
			os.Remove(path)
			continue
		}

		// The energy gate. A clip that is mostly quiet — the onset threshold
		// tripped by a single knock, or a room that drifts across the line — is
		// discarded here, before it can be handed to a transcriber that would
		// invent a sentence for it. This is the difference between a wake word
		// that answers only when spoken to and one that answers the silence.
		if !hasSpeechEnergy(ctx, path) {
			os.Remove(path)
			continue
		}

		transcript, err := l.Recognizer.Transcribe(ctx, path)
		os.Remove(path)
		if err != nil || strings.TrimSpace(transcript) == "" {
			continue
		}

		wake, woke := DetectWake(transcript)
		l.mu.Lock()
		l.heard++
		if woke {
			l.woken++
			l.lastWake = time.Now()
		}
		l.mu.Unlock()

		if l.OnHeard != nil {
			l.OnHeard(transcript, woke)
		}
		if woke && l.OnWake != nil {
			wake.Heard = time.Now()
			l.OnWake(wake)
		}
	}
}

func (l *Listener) stopped(reason string) {
	if l.OnStop != nil {
		l.OnStop(reason)
	}
}

// DetectWake looks for the wake word and returns whatever followed it.
//
// The name may appear anywhere in the utterance, not only at the start: people
// say "so, Freya, what's the disk at" as readily as "Freya, what's the disk at".
func DetectWake(transcript string) (Wake, bool) {
	// Split the original once and normalise each word only for comparison. An
	// earlier version stripped punctuation from the whole string and then built
	// the command from the stripped copy, which turned "what's my disk at" into
	// "what s my disk at" — the apostrophes were collateral damage from making
	// "Freya," match.
	words := strings.Fields(transcript)

	for i, raw := range words {
		if !isWakeName(normaliseWord(raw)) {
			continue
		}
		// A preceding "hey" is part of the address, not of the command.
		phraseStart := i
		if i > 0 && isWakePrefix(normaliseWord(words[i-1])) {
			phraseStart = i - 1
		}

		phrase := strings.ToLower(strings.Join(words[phraseStart:i+1], " "))
		phrase = strings.TrimRight(phrase, ",.!?;:")

		// The command keeps its original spelling and punctuation.
		command := strings.TrimSpace(strings.Join(words[i+1:], " "))
		command = strings.TrimLeft(command, " ,;:-—")

		return Wake{
			Phrase:     phrase,
			Command:    strings.TrimSpace(command),
			Transcript: strings.TrimSpace(transcript),
		}, true
	}
	return Wake{}, false
}

// normaliseWord reduces a word to its comparable form: lowercase, with
// surrounding punctuation removed but internal characters untouched.
func normaliseWord(w string) string {
	return strings.Trim(strings.ToLower(w), `,.!?;:"'()[]`)
}

func isWakeName(word string) bool {
	for _, v := range wakeVariants {
		if word == v {
			return true
		}
	}
	// A near-miss still counts, because the transcriber is not deterministic
	// and a wake word the user has to say three times is one that trains them
	// to stop using it. The tolerance is deliberately tight — one edit against
	// the canonical spellings — and gated on a leading "fr", so common words do
	// not slip through: "free", "fry", "afraid" are all more than one edit away
	// or fail the prefix.
	if len(word) >= 4 && strings.HasPrefix(word, "fr") {
		for _, v := range []string{"freya", "freyja", "fraya"} {
			if editDistanceWithin(word, v, 1) {
				return true
			}
		}
	}
	return false
}

// editDistanceWithin reports whether a and b are within max edits of each
// other, giving up as soon as that is exceeded.
//
// A bounded check rather than a full matrix: the words are short and the bound
// is one or two, so the moment a row's best possible score passes the bound
// there is no reason to finish computing it.
func editDistanceWithin(a, b string, max int) bool {
	la, lb := len(a), len(b)
	if la-lb > max || lb-la > max {
		return false
	}

	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur := make([]int, lb+1)
		cur[0] = i
		best := cur[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if cur[j] < best {
				best = cur[j]
			}
		}
		if best > max {
			return false
		}
		prev = cur
	}
	return prev[lb] <= max
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func isWakePrefix(word string) bool {
	for _, p := range wakePrefixes {
		if word == p {
			return true
		}
	}
	return false
}

// Describe summarises a listener's state.
func (l *Listener) Describe() string {
	heard, woken, since := l.Stats()
	if !l.Listening() {
		return "not listening"
	}
	timeout := l.InactivityTimeout
	if timeout == 0 {
		timeout = DefaultInactivityTimeout
	}
	limit := timeout.String()
	if l.Indefinite {
		limit = "no timeout"
	}
	return fmt.Sprintf("listening — %d utterances heard, %d woke, %s since the last wake (stops after %s)",
		heard, woken, since.Round(time.Second), limit)
}
