package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akins/jarvis/internal/agent"
	"github.com/akins/jarvis/internal/config"
	"github.com/akins/jarvis/internal/llm"
	"github.com/akins/jarvis/internal/memory"
	"github.com/akins/jarvis/internal/schedule"
	"github.com/akins/jarvis/internal/voice"
)

// voiceState holds the spoken-mode machinery for a REPL session.
type voiceState struct {
	// mu guards style, which voice_adjust can change from inside a tool goroutine
	// while another goroutine is speaking.
	mu       sync.Mutex
	session  *voice.Session
	verifier *voice.MFCCVerifier
	policy   voice.Policy
	enabled  bool
	dataDir  string
	style    voice.Style
	listener *voice.Listener
	ackStyle voice.AckStyle
	// speaker owns the audio device. Every spoken line goes through it, so two
	// things can never play at once and a background report can never talk over a
	// conversation. Nil only when voice is unavailable.
	speaker *voice.Speaker
	// indicator is the screen light, if there is a display. Nil is safe.
	indicator *indicator
}

// Style implements skills.VoiceController.
//
// Guarded because the pair below is genuinely concurrent: SetStyle is reachable
// from the voice_adjust skill, which runs inside a tool goroutine, while Style
// and the synthesiser are read from whichever goroutine is speaking. She can
// retune her own voice mid-sentence, which is the point — but the read and the
// write have to not overlap.
func (v *voiceState) Style() voice.Style {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.style
}

// SetStyle applies new delivery settings to the live synthesiser and persists
// them, so a change Freya makes mid-conversation takes effect on her next
// sentence rather than after a restart.
func (v *voiceState) SetStyle(s voice.Style) error {
	v.mu.Lock()
	v.style = s
	v.mu.Unlock()
	voice.Restyle(v.session.Synth, s)
	return voice.SaveStyle(v.dataDir, s)
}

// setupVoice builds the voice stack, reporting clearly when a piece is missing
// rather than failing at the moment someone tries to speak.
func setupVoice(cfg *config.Config, provider llm.Provider) (*voiceState, error) {
	recorder, err := voice.NewRecorder()
	if err != nil {
		return nil, err
	}
	recognizer, err := voice.NewRecognizer(cfg.STT, provider, cfg.WhisperModel)
	if err != nil {
		return nil, err
	}

	style := voice.LoadStyle(cfg.DataDir)
	if cfg.VoiceName != "" {
		style.Voice = cfg.VoiceName
	}

	// Gemini synthesis is only offered when the active provider can do it.
	speech, _ := provider.(llm.SpeechSynthesizer)
	synth, err := voice.NewSynthesizerWith(voice.SynthOptions{
		Preference: cfg.TTS,
		Style:      style,
		PiperModel: cfg.PiperModel,
		Speech:     speech,
	})
	if err != nil {
		return nil, err
	}

	// The speaker is inserted between everything that talks and the synthesiser,
	// and the Session speaks through it too — so there is exactly one path to the
	// audio device and no caller can bypass the arbitration by holding the
	// session directly.
	speaker := voice.NewSpeaker(synth)

	return &voiceState{
		style:    style,
		ackStyle: voice.ParseAckStyle(cfg.WakeAck),
		speaker:  speaker,
		session: &voice.Session{
			Recorder:   recorder,
			Recognizer: recognizer,
			Synth:      voice.AtPriority(speaker, voice.Reply),
			Mic:        mic,
			TempDir:    os.TempDir(),
		},
		verifier: voice.NewMFCCVerifier(cfg.DataDir),
		policy:   voice.ParsePolicy(cfg.VoicePolicy),
		dataDir:  cfg.DataDir,
	}, nil
}

// startListening turns on continuous wake-word listening.
func (v *voiceState) startListening(ctx context.Context, arg string, a *agent.Agent) error {
	if v.listener != nil && v.listener.Listening() {
		fmt.Printf("  %s\n", v.listener.Describe())
		return nil
	}

	timeout := voice.DefaultInactivityTimeout
	indefinite := false
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "forever", "indefinite", "always":
		indefinite = true
	case "":
	default:
		if d, err := time.ParseDuration(arg); err == nil {
			timeout = d
		}
	}

	// Wake listening gets its own recorder, tuned short. The session recorder is
	// sized for a full spoken request and would let a noisy room stretch every
	// wake cycle to its ceiling; a wake word needs neither the length nor the
	// long trailing pause. Sox is the recorder in all but the fallback case, so
	// this is a safe narrowing rather than a new dependency.
	wakeRecorder := v.session.Recorder
	if _, ok := wakeRecorder.(voice.SoxRecorder); ok {
		wakeRecorder = voice.WakeTuned()
	}

	v.listener = &voice.Listener{
		Recorder:          wakeRecorder,
		Recognizer:        v.session.Recognizer,
		Mic:               mic,
		Busy:              func() bool { return v.speaker != nil && v.speaker.Speaking() },
		InactivityTimeout: timeout,
		Indefinite:        indefinite,
		OnHeard: func(text string, woke bool) {
			if woke {
				return // the wake handler reports it
			}
			fmt.Printf("%s  (heard, not addressed: %.60s)%s\n", cDim, text, cReset)
		},
		OnTrouble: func(err error, consecutive int) {
			fmt.Fprintf(os.Stderr,
				"%s  cannot record: %v (%d attempts in a row — she is listening but hearing nothing)%s\n",
				cYellow, err, consecutive, cReset)
		},
		OnStop: func(reason string) {
			// The light must follow the listener even when it stops itself on
			// the inactivity timeout, which is the case a caller cannot see.
			v.indicator.idle()
			fmt.Printf("\n%s  %s%s\n%s❯%s ", cYellow, reason, cReset, cCyan, cReset)
		},
		OnWake: func(w voice.Wake) {
			// First thing, before any processing: the point of the sound is to
			// land while the user is still talking, so they know she caught it.
			voice.Acknowledge(v.ackStyle, func(s string) error {
				return v.session.Speak(ctx, s)
			})
			fmt.Printf("\n%s  ▸ %s%s\n", cCyan, w.Transcript, cReset)

			command := w.Command
			if command == "" {
				// Addressed with nothing after it: wait for the request, which
				// is how people speak when they pause after a name.
				if v.ackStyle == voice.AckChime {
					// The chime alone does not invite a reply; say so.
					_ = v.session.Speak(ctx, "Yes?")
				}
				follow, err := v.session.Listen(ctx)
				if err != nil || strings.TrimSpace(follow) == "" {
					return
				}
				command = follow
				fmt.Printf("%s  ▸ %s%s\n", cCyan, command, cReset)
			}

			// "Freya, stop" is about the work, not new work. It ends what is
			// running rather than queueing behind it.
			if isStopInstruction(command) {
				msg := stopEverything()
				fmt.Printf("%s  ⏹ %s%s\n", cYellow, msg, cReset)
				v.speak(ctx, voice.Urgent, msg)
				return
			}

			// Registered so a later press of the talk key can stop it. The wake
			// loop cannot itself hear anything while this runs — it is recording
			// nothing during the callback — so Ctrl+Space is the way out.
			turnCtx, endTurn := beginTurn(ctx, "working on: "+clipLine(command, 60))
			defer endTurn()

			done := v.indicator.working()
			res, err := a.Ask(turnCtx, command)
			done()
			if err != nil {
				if turnCtx.Err() != nil {
					return // interrupted deliberately; the interrupter already spoke
				}
				// A falling two-tone, so a failure is audible from across the
				// room rather than only visible on a terminal nobody is reading.
				_ = voice.ChimeError()
				fmt.Printf("%s  %v%s\n", cRed, err, cReset)
				return
			}
			fmt.Printf("\n%s\n\n%s❯%s ", res.Reply, cCyan, cReset)
			_ = v.session.Speak(ctx, res.Reply)
			// The closing chime goes after the speech, where it marks the end of
			// the exchange. Before it, it would sound like an error.
			_ = voice.ChimeDone()
		},
	}

	if err := v.listener.Start(ctx); err != nil {
		return err
	}
	v.indicator.listening()
	limit := timeout.String()
	if indefinite {
		limit = "no timeout"
	}
	fmt.Printf("  listening for \"Freya\" or \"hey Freya\" — stops after %s\n", limit)
	fmt.Printf("%s  note: while this is on, speech near the mic is transcribed,\n"+
		"  whether or not it was meant for her. /voice deaf turns it off.%s\n", cDim, cReset)
	return nil
}

func (v *voiceState) describe() string {
	base := fmt.Sprintf("recorder %s · ears %s · voice %s · policy %s · %s",
		v.session.Recorder.Name(), v.session.Recognizer.Name(),
		v.session.Synth.Name(), v.policy, v.verifier.Describe())
	if v.listener != nil && v.listener.Listening() {
		base += "\n  " + v.listener.Describe()
	}
	return base
}

// voiceCommand handles the /voice family. Returns true if voice mode should
// run a spoken turn immediately after.
func voiceCommand(ctx context.Context, rest string, v *voiceState, a *agent.Agent) (speak bool, err error) {
	sub, arg, _ := strings.Cut(rest, " ")
	arg = strings.TrimSpace(arg)

	switch sub {
	case "":
		fmt.Printf("  %s\n", v.describe())
		fmt.Printf("  voice mode is %s\n", onOff(v.enabled))
		return false, nil

	case "on":
		v.enabled = true
		fmt.Printf("  voice mode on — press Enter to speak, /voice off to stop\n")
		return false, nil

	case "off":
		v.enabled = false
		v.session.Interrupt()
		fmt.Println("  voice mode off")
		return false, nil

	case "say":
		if arg == "" {
			return false, fmt.Errorf("nothing to say")
		}
		return false, v.session.Speak(ctx, arg)

	case "enroll", "enrol":
		return false, v.enroll(ctx, arg)

	case "test":
		return false, v.test(ctx)

	case "listen":
		return false, v.startListening(ctx, arg, a)

	case "ack":
		if arg == "" {
			fmt.Printf("  wake acknowledgement: %s\n", v.ackStyle)
			fmt.Println("  options: chime, speak, both, silent")
			return false, nil
		}
		v.ackStyle = voice.ParseAckStyle(arg)
		fmt.Printf("  wake acknowledgement: %s\n", v.ackStyle)
		// Play it so the choice is audible rather than described.
		voice.Acknowledge(v.ackStyle, func(s string) error {
			return v.session.Speak(ctx, s)
		})
		return false, nil

	case "chime":
		return false, voice.Chime()

	case "deaf", "unlisten":
		if v.listener != nil {
			v.listener.Stop()
		}
		v.indicator.idle()
		fmt.Println("  stopped listening")
		return false, nil

	case "style":
		if arg == "" {
			fmt.Printf("  %s\n", v.Style().Describe())
			return false, nil
		}
		return false, v.setStyleField(arg)

	case "pace", "speed":
		return false, v.setStyleField("pace " + arg)

	case "pitch":
		return false, v.setStyleField("pitch " + arg)

	case "tone":
		return false, v.setStyleField("tone " + arg)

	case "voices":
		fmt.Printf("  %space:%s  %s\n", cDim, cReset, strings.Join(voice.PaceNames(), ", "))
		fmt.Printf("  %spitch:%s %s\n", cDim, cReset, strings.Join(voice.PitchNames(), ", "))
		fmt.Printf("  %stone:%s  %s\n", cDim, cReset, strings.Join(voice.ToneNames(), ", "))
		fmt.Printf("  %svoices:%s\n", cDim, cReset)
		for _, gv := range voice.GeminiVoices {
			fmt.Printf("    %-16s %s\n", gv.Name, gv.Character)
		}
		return false, nil

	case "forget":
		if err := v.verifier.Forget(); err != nil {
			return false, err
		}
		fmt.Println("  voiceprint deleted")
		return false, nil

	case "policy":
		if arg == "" {
			fmt.Printf("  policy is %s\n", v.policy)
			return false, nil
		}
		v.policy = voice.ParsePolicy(arg)
		if v.policy == voice.PolicyEnforce {
			fmt.Printf("%s  warning: speaker verification here is not accurate enough to\n"+
				"  lock others out reliably. Run /voice test with a second person\n"+
				"  before trusting enforce.%s\n", cYellow, cReset)
		}
		fmt.Printf("  policy set to %s\n", v.policy)
		return false, nil

	case "threshold":
		if arg == "" {
			fmt.Printf("  %s\n", v.verifier.Describe())
			return false, nil
		}
		t, parseErr := strconv.ParseFloat(arg, 64)
		if parseErr != nil {
			return false, fmt.Errorf("threshold must be a number between 0 and 1")
		}
		if err := v.verifier.SetThreshold(t); err != nil {
			return false, err
		}
		fmt.Printf("  threshold set to %.2f\n", t)
		return false, nil

	default:
		return false, fmt.Errorf("unknown voice command %q — try on, off, enroll, test, "+
			"policy, threshold, forget, say", sub)
	}
}

// enroll records several phrases and builds a voiceprint.
func (v *voiceState) enroll(ctx context.Context, name string) error {
	if name == "" {
		name = "owner"
	}
	dir := filepath.Join(v.dataDir, "enrolment")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	fmt.Printf("\n  Enrolling %q. You'll read %d phrases; recording stops when you\n"+
		"  stop speaking. Use your normal voice, at your normal distance.\n\n",
		name, len(voice.EnrollmentPhrases))

	var paths []string
	for i, phrase := range voice.EnrollmentPhrases {
		fmt.Printf("  %d/%d  \"%s\"\n", i+1, len(voice.EnrollmentPhrases), phrase)
		fmt.Printf("  %spress Enter, then read it aloud%s ", cDim, cReset)
		fmt.Scanln()

		path := filepath.Join(dir, fmt.Sprintf("%s-%d.ogg", name, i))
		if err := v.session.Recorder.Record(ctx, path); err != nil {
			fmt.Printf("  %sskipped: %v%s\n", cYellow, err, cReset)
			continue
		}
		fmt.Printf("  %s✓ captured%s\n\n", cDim, cReset)
		paths = append(paths, path)
	}

	profile, err := v.verifier.Enroll(ctx, name, paths)
	if err != nil {
		return err
	}
	fmt.Printf("  enrolled: %s\n", v.verifier.Describe())
	if profile.SelfSimilarity < 0.85 {
		fmt.Printf("%s  the samples vary a lot; a quieter room would give a tighter profile%s\n",
			cYellow, cReset)
	}
	fmt.Printf("%s  Now run /voice test — ideally with someone else speaking too — to see\n"+
		"  whether the scores actually separate before relying on this.%s\n", cDim, cReset)
	return nil
}

// test records one utterance and reports how it scores, so the threshold can
// be tuned against real measurements instead of guesswork.
func (v *voiceState) test(ctx context.Context) error {
	if !v.verifier.Enrolled() {
		return fmt.Errorf("no voiceprint enrolled — run /voice enroll first")
	}
	fmt.Printf("  %spress Enter, then say anything%s ", cDim, cReset)
	fmt.Scanln()

	path := filepath.Join(os.TempDir(), fmt.Sprintf("freya-test-%d.ogg", time.Now().UnixNano()))
	defer os.Remove(path)

	if err := v.session.Recorder.Record(ctx, path); err != nil {
		return err
	}
	report, err := v.verifier.Diagnose(ctx, path)
	if err != nil {
		return err
	}

	verdict := cRed + "REJECT" + cReset
	if report.Accepted {
		verdict = cCyan + "ACCEPT" + cReset
	}
	fmt.Printf("  score %.3f vs threshold %.2f -> %s\n",
		report.Score, report.Threshold, verdict)
	fmt.Printf("  %sper-sample cosine: %s%s\n", cDim, formatScores(report.PerSample), cReset)
	return nil
}

// gate applies the verification policy to a recording. It returns false when
// the turn should be abandoned.
func (v *voiceState) gate(ctx context.Context, audioPath string) bool {
	if v.policy == voice.PolicyOff || !v.verifier.Enrolled() {
		return true
	}
	ok, score, err := v.verifier.Verify(ctx, audioPath)
	if err != nil {
		// A verification failure must not silently block the owner.
		fmt.Printf("%s  voice check failed (%v) — continuing%s\n", cYellow, err, cReset)
		return true
	}
	if ok {
		return true
	}
	if v.policy == voice.PolicyWarn {
		fmt.Printf("%s  unrecognised voice (%.3f) — answering anyway, policy is warn%s\n",
			cYellow, score, cReset)
		return true
	}
	fmt.Printf("%s  unrecognised voice (%.3f) — ignoring%s\n", cRed, score, cReset)
	return false
}

// setStyleField parses "<field> <value>" and applies it.
func (v *voiceState) setStyleField(arg string) error {
	field, value, _ := strings.Cut(strings.TrimSpace(arg), " ")
	value = strings.TrimSpace(value)
	style := v.Style()

	switch strings.ToLower(field) {
	case "pace", "speed":
		if _, ok := voice.Paces[strings.ToLower(value)]; !ok {
			return fmt.Errorf("pace must be one of: %s", strings.Join(voice.PaceNames(), ", "))
		}
		style.Pace = strings.ToLower(value)
	case "pitch":
		if _, ok := voice.Pitches[strings.ToLower(value)]; !ok {
			return fmt.Errorf("pitch must be one of: %s", strings.Join(voice.PitchNames(), ", "))
		}
		style.Pitch = strings.ToLower(value)
	case "tone":
		if unknown := style.SetTone(strings.Split(value, ",")); len(unknown) > 0 {
			fmt.Printf("%s  passing through unlisted tones: %s%s\n",
				cDim, strings.Join(unknown, ", "), cReset)
		}
	case "voice":
		style.Voice = value
	case "custom":
		style.Custom = value
	case "reset":
		style = voice.DefaultStyle()
	default:
		return fmt.Errorf("unknown style field %q — try pace, pitch, tone, voice, custom, reset", field)
	}

	if err := v.SetStyle(style); err != nil {
		return err
	}
	fmt.Printf("  %s\n", style.Describe())
	return nil
}

func formatScores(scores []float64) string {
	parts := make([]string, 0, len(scores))
	for _, s := range scores {
		parts = append(parts, fmt.Sprintf("%.3f", s))
	}
	return strings.Join(parts, " ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// pushToTalk runs one global-hotkey exchange: a chime to confirm the key was
// heard, record until the speaker pauses, transcribe, think, and speak.
//
// # Why its own recorder
//
// It uses the push-to-talk recorder, not the session's. The session recorder
// waits for a loud onset before it captures, which on this hardware never fires
// reliably — the failure that sank wake-word listening. The key press has
// already announced that speech is coming, so the recorder starts at once and
// stops on the pause instead of hunting for an onset that will not come.
//
// # Concurrency
//
// bgBusy serialises the quiet-moment follow-up against a live conversation.
//
// Scheduled self-tasks used to share it; they are Jobs now, bounded by the work
// manager's own pool. What is left here is the one piece of background work that
// is NOT a job: a reflection with no tools that decides whether to say anything
// at all, and which must not run twice at once or over the top of a turn.
//
// It replaces a single flag that used to cover both speaking and working. That
// flag also swallowed every attempt to interrupt her, because a press during a
// long task looked identical to a press during a recording. Recording is now
// guarded by the microphone lock and the conversation by the turn handle, which
// leaves this doing only what it should: keeping background work out of the way.
var bgBusy atomic.Bool

// lastUserTurnTime returns when the user last spoke, ignoring the assistant's own
// turns (including any follow-up it makes), so a lull is measured from real input
// and a follow-up cannot reset its own clock.
func lastUserTurnTime(turns []memory.Turn) time.Time {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "user" {
			return turns[i].Timestamp
		}
	}
	return time.Time{}
}

// startSelfTask hands one due self-task to the background manager.
//
// It used to run inline, holding a flag that blocked every other piece of
// background work and stood down whenever the user was talking — so "check back
// in ten minutes" quietly meant "check back once you stop typing". As a Job it
// runs on its own branched conversation and reports when it is done, which is
// what a follow-up was always supposed to be.
func startSelfTask(t schedule.Task) {
	label := t.Note
	if label == "" {
		label = t.Prompt
	}
	if jobs == nil {
		return
	}
	if _, err := jobs.Start(t.Prompt, "your earlier request"); err != nil {
		fmt.Fprintf(os.Stderr, "self-task %q not started: %v\n", t.ID, err)
		return
	}
	fmt.Printf("%s  ⏰ following up: %s%s\n", cDim, label, cReset)
}

// deepenActivity screenshots the focused window and asks the vision model for a
// one-line read of what is actually in it — the browser page, the folder's files —
// beyond the title. Best-effort: it returns "" on any failure, so the tracker
// simply falls back to the title.
func deepenActivity(seer llm.VisionAnalyzer, kind string) string {
	var prompt string
	switch kind {
	case "browser":
		prompt = "This is a screenshot of a web browser. In ONE short line (under 15 words), " +
			"what page is this — the site and what they are viewing? No preamble, no markdown."
	case "files":
		prompt = "This is a screenshot of a file manager. In ONE short line (under 15 words), " +
			"which folder is open and any notable files or subfolders? No preamble, no markdown."
	default:
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	path := filepath.Join(os.TempDir(), fmt.Sprintf("freya-act-%d.png", time.Now().UnixNano()))
	if err := exec.CommandContext(ctx, "scrot", "-u", "-o", path).Run(); err != nil {
		return ""
	}
	defer os.Remove(path)
	png, err := os.ReadFile(path)
	if err != nil || len(png) == 0 {
		return ""
	}
	out, err := seer.AnalyzeImage(ctx, prompt, [][]byte{png}, []string{"image/png"})
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(out)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}
	if len(line) > 140 {
		line = line[:140] + "…"
	}
	return line
}

func pushToTalk(ctx context.Context, a *agent.Agent, v *voiceState) {
	// Only the microphone is exclusive. While she is thinking and running tools —
	// nearly all of a long task — the mic is idle, so a press CAN be heard, and
	// that is the whole point: it is the only way to stop her.
	if !takeMic() {
		return // already recording; two recorders on one device produce nothing
	}
	released := false
	releaseNow := func() {
		if !released {
			released = true
			releaseMic()
		}
	}
	defer releaseNow()

	// Acknowledge immediately, before anything slow, so the press feels heard.
	_ = voice.Chime()
	done := v.indicator.working()
	defer done()

	dir := os.Getenv("TMPDIR")
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, fmt.Sprintf("freya-ptt-%d.ogg", time.Now().UnixNano()))
	defer os.Remove(path)

	rec := voice.PushToTalkRecorder{}
	if err := rec.Record(ctx, path); err != nil {
		_ = voice.ChimeError()
		fmt.Fprintf(os.Stderr, "push-to-talk record: %v\n", err)
		return
	}

	// A press with no speech behind it — a mis-hit key, a pocket — records
	// near-silence, and the transcriber's wake-word hint would turn that into a
	// phantom "Freya". The energy gate stops it here, quietly: no chime, no
	// hallucinated turn in memory.
	if !voice.HasSpeechEnergy(ctx, path) {
		return
	}

	transcript, err := v.session.Recognizer.Transcribe(ctx, path)
	if err != nil {
		_ = voice.ChimeError()
		fmt.Fprintf(os.Stderr, "push-to-talk transcribe: %v\n", err)
		return
	}
	if strings.TrimSpace(transcript) == "" {
		return // pressed but said nothing; no chime, no fuss
	}
	fmt.Printf("%s  ▸ %s%s\n", cCyan, transcript, cReset)

	// The microphone is free from here, so she can be interrupted again while she
	// works on what was just said.
	releaseNow()

	// "Stop" is an instruction about the work, not a new piece of work. It ends
	// what is running and starts nothing.
	if isStopInstruction(transcript) {
		msg := stopEverything()
		fmt.Printf("%s  ⏹ %s%s\n", cYellow, msg, cReset)
		// Urgent: it cuts through whatever she was mid-sentence on, which is the
		// point — the user just asked her to stop and is waiting to hear it landed.
		v.speak(context.Background(), voice.Urgent, msg)
		return
	}

	// Anything else spoken mid-task supersedes it, the way talking over someone
	// does. beginTurn cancels whatever was in flight.
	turnCtx, endTurn := beginTurn(ctx, "working on: "+clipLine(transcript, 60))
	defer endTurn()

	res, err := a.Ask(turnCtx, transcript)
	if err != nil {
		if turnCtx.Err() != nil {
			return // interrupted on purpose; the interrupter already spoke
		}
		_ = voice.ChimeError()
		_ = v.session.Speak(ctx, "Something went wrong with that.")
		return
	}
	fmt.Printf("\n%s\n\n", res.Reply)
	_ = v.session.Speak(ctx, res.Reply)
	_ = voice.ChimeDone()
}

// clipLine shortens an utterance for describing what is being worked on.
func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// spokenTurn runs one push-to-talk exchange: record, verify the speaker,
// transcribe, think, and speak the answer.
func spokenTurn(ctx context.Context, a *agent.Agent, cfg *config.Config, v *voiceState) error {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("freya-turn-%d.ogg", time.Now().UnixNano()))
	defer os.Remove(path)

	fmt.Printf("%s  listening…%s\n", cDim, cReset)
	recStart := time.Now()
	if err := v.session.Recorder.Record(ctx, path); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	// Verify before transcribing: no point spending an API call on a voice
	// that is about to be turned away.
	if !v.gate(ctx, path) {
		return nil
	}

	sttStart := time.Now()
	transcript, err := v.session.Recognizer.Transcribe(ctx, path)
	if err != nil {
		return fmt.Errorf("transcribe: %w", err)
	}
	if transcript == "" {
		fmt.Printf("%s  didn't catch anything%s\n", cDim, cReset)
		return nil
	}
	sttTime := time.Since(sttStart)

	fmt.Printf("%s  you:%s %s\n", cCyan, cReset, transcript)

	done := v.indicator.working()
	res, err := a.Ask(ctx, transcript)
	done()
	if err != nil {
		_ = voice.ChimeError()
		_ = v.session.Speak(ctx, "Something went wrong handling that.")
		return err
	}

	fmt.Printf("\n%s\n\n", res.Reply)
	if cfg.Verbose {
		fmt.Printf("%s  record %.1fs · transcribe %.1fs · %d round(s)%s\n",
			cDim, sttStart.Sub(recStart).Seconds(), sttTime.Seconds(), res.Rounds, cReset)
	}
	speakErr := v.session.Speak(ctx, res.Reply)
	_ = voice.ChimeDone()
	return speakErr
}

// startWakeListening arms the wake word and points the status light at it.
//
// # Why the daemon defaults to no timeout and a session does not
//
// The inactivity timeout exists so that listening, once switched on, does not
// stay on because somebody forgot. That reasoning does not apply to a process
// whose entire purpose is to be there when you speak to it: a daemon that goes
// deaf after two quiet hours is a daemon that is deaf every morning.
//
// The cost is real and worth stating plainly rather than burying. There is no
// local wake-word model here, so while this is on, speech near the microphone
// is recorded and sent for transcription whether or not it was meant for her.
// FREYA_WAKE=off turns it off; FREYA_WAKE=2h restores a timeout.
func startWakeListening(ctx context.Context, a *agent.Agent, v *voiceState, ind *indicator) error {
	if v == nil {
		return fmt.Errorf("voice is not configured")
	}
	v.indicator = ind

	arg := strings.ToLower(strings.TrimSpace(os.Getenv("FREYA_WAKE")))
	switch arg {
	case "off", "no", "false":
		return fmt.Errorf("disabled by FREYA_WAKE=off")
	case "", "on", "forever", "always", "indefinite":
		arg = "forever"
	}

	if err := v.startListening(ctx, arg, a); err != nil {
		return err
	}
	ind.listening()
	return nil
}

// speak says something at a given priority, and reports whether it was actually
// said.
//
// Every spoken line in the process goes through here. Calling the session
// directly is what allowed two utterances to overlap: each synthesiser keeps one
// handle for Stop to kill, and concurrent callers overwrote it, so the audio
// doubled up and only the last one could be interrupted.
func (v *voiceState) speak(ctx context.Context, pri voice.Priority, text string) bool {
	if v == nil || v.speaker == nil {
		return false
	}
	if v.session.OnSpeaking != nil {
		v.session.OnSpeaking(text)
	}
	said, _ := v.speaker.Say(ctx, pri, text)
	return said
}

// wakeDisabled reports whether the user has turned always-on listening off.
//
// Separate from "it failed to start", which is what the banner used to conflate:
// a microphone that records the room continuously should be on because somebody
// said so, not because nothing prevented it.
func wakeDisabled(cfg *config.Config) bool {
	switch strings.ToLower(strings.TrimSpace(cfg.Wake)) {
	case "off", "no", "false", "0", "deaf":
		return true
	}
	return false
}
