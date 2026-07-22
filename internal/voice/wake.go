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
	return false
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
