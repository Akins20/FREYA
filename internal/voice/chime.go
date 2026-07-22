package voice

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Acknowledging the wake word.
//
// # Why a tone rather than speech
//
// The purpose is to close a feedback loop in the moment: you said her name, did
// she hear it? Speech is the wrong instrument for that. It takes a round trip
// to synthesise, so the answer arrives a second or two after the question, and
// it talks over you if you were still finishing your sentence. A tone is
// immediate, costs nothing, and does not compete with a human voice.
//
// Speech remains available, because sometimes a spoken "yes?" is what you want
// — particularly if you are across the room and not looking at the machine.
//
// The tone is generated rather than shipped: a hundred lines of arithmetic
// beats a binary asset that has to live somewhere and be found at runtime.

// AckStyle is how a detected wake word is acknowledged.
type AckStyle string

const (
	// AckChime plays a short tone. Immediate, and the default.
	AckChime AckStyle = "chime"
	// AckSpeak says a word aloud.
	AckSpeak AckStyle = "speak"
	// AckBoth chimes and then speaks.
	AckBoth AckStyle = "both"
	// AckSilent gives no acknowledgement.
	AckSilent AckStyle = "silent"
)

// ParseAckStyle reads a setting name, defaulting to the chime.
func ParseAckStyle(s string) AckStyle {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "speak", "voice", "say":
		return AckSpeak
	case "both", "chime+speak":
		return AckBoth
	case "silent", "none", "off":
		return AckSilent
	default:
		return AckChime
	}
}

// Tone descriptions for the sounds Freya makes about herself.
type tone struct {
	freq     float64
	duration time.Duration
}

// wakeTones rise, which reads as attention rather than completion.
var wakeTones = []tone{
	{880.0, 70 * time.Millisecond},  // A5
	{1174.7, 90 * time.Millisecond}, // D6
}

// doneTones fall, closing the interaction.
var doneTones = []tone{
	{1174.7, 70 * time.Millisecond},
	{880.0, 90 * time.Millisecond},
}

// errorTones sit lower and closer together, which reads as a problem without
// being alarming.
var errorTones = []tone{
	{440.0, 90 * time.Millisecond},
	{392.0, 130 * time.Millisecond},
}

const (
	chimeSampleRate = 22050
	chimeAmplitude  = 0.22 // quiet: this plays often and must not startle
)

// chimeCache holds generated sounds so a tone is synthesised once per run.
var (
	chimeMu    sync.Mutex
	chimeFiles = map[string]string{}
)

// Chime plays the wake acknowledgement.
func Chime() error { return playTones("wake", wakeTones) }

// ChimeDone plays the completion sound.
func ChimeDone() error { return playTones("done", doneTones) }

// ChimeError plays the failure sound.
func ChimeError() error { return playTones("error", errorTones) }

// playTones renders a sequence to a cached file and plays it.
func playTones(name string, tones []tone) error {
	path, err := toneFile(name, tones)
	if err != nil {
		return err
	}

	player, args := chimePlayer(path)
	if player == "" {
		return fmt.Errorf("no audio player available for the chime")
	}
	// Detached: an acknowledgement must not delay the work it acknowledges.
	cmd := exec.Command(player, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// toneFile returns a WAV of the given tones, generating it on first use.
func toneFile(name string, tones []tone) (string, error) {
	chimeMu.Lock()
	defer chimeMu.Unlock()

	if path, ok := chimeFiles[name]; ok {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	data := renderTones(tones)
	path := filepath.Join(os.TempDir(), fmt.Sprintf("freya-chime-%s.wav", name))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	chimeFiles[name] = path
	return path, nil
}

// renderTones builds a 16-bit mono WAV from a tone sequence.
func renderTones(tones []tone) []byte {
	var samples []int16

	for _, t := range tones {
		n := int(float64(chimeSampleRate) * t.duration.Seconds())
		for i := range n {
			pos := float64(i) / float64(n)

			// A raised-cosine envelope. Without one, the abrupt start and stop
			// of a sine produce a click at each edge that is more noticeable
			// than the tone itself.
			env := 0.5 * (1 - math.Cos(2*math.Pi*math.Min(pos*4, 1)*0.5))
			if pos > 0.6 {
				env *= 1 - (pos-0.6)/0.4
			}

			v := math.Sin(2*math.Pi*t.freq*float64(i)/chimeSampleRate) * env * chimeAmplitude
			samples = append(samples, int16(v*32767))
		}
	}

	var buf bytes.Buffer
	dataLen := uint32(len(samples) * 2)

	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, 36+dataLen)
	buf.WriteString("WAVEfmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&buf, binary.LittleEndian, uint32(chimeSampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(chimeSampleRate*2))
	binary.Write(&buf, binary.LittleEndian, uint16(2))
	binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, dataLen)
	for _, s := range samples {
		binary.Write(&buf, binary.LittleEndian, s)
	}
	return buf.Bytes()
}

// chimePlayer picks a command that plays a WAV file.
func chimePlayer(path string) (string, []string) {
	switch {
	case have("paplay"):
		return "paplay", []string{path}
	case have("aplay"):
		return "aplay", []string{"-q", path}
	case have("play"):
		return "play", []string{"-q", path}
	default:
		return "", nil
	}
}

// Acknowledge signals that the wake word was heard, in the requested style.
//
// The chime fires before anything else and does not block: the entire point is
// that it lands while the user is still speaking, not after the request has
// been processed.
func Acknowledge(style AckStyle, speak func(string) error) {
	switch style {
	case AckSilent:
		return
	case AckSpeak:
		if speak != nil {
			go func() { _ = speak("Yes?") }()
		}
	case AckBoth:
		_ = Chime()
		if speak != nil {
			go func() {
				time.Sleep(250 * time.Millisecond) // let the tone finish first
				_ = speak("Yes?")
			}()
		}
	default:
		_ = Chime()
	}
}
