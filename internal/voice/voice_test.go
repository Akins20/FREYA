package voice

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// synthWAV writes a 16-bit mono WAV built from a fundamental plus harmonics.
// Different fundamentals and harmonic mixes stand in for different voices,
// which is enough to exercise the discrimination logic deterministically.
func synthWAV(t *testing.T, path string, fundamental float64, harmonics []float64, seconds float64) {
	t.Helper()

	const rate = 16000
	n := int(rate * seconds)
	samples := make([]int16, n)
	for i := range n {
		tt := float64(i) / rate
		var v float64
		for h, amp := range harmonics {
			v += amp * math.Sin(2*math.Pi*fundamental*float64(h+1)*tt)
		}
		// Amplitude modulation gives the frames something to vary over, as
		// speech does; a pure tone yields degenerate statistics.
		v *= 0.6 + 0.4*math.Sin(2*math.Pi*3*tt)
		samples[i] = int16(math.Max(-32000, math.Min(32000, v*12000)))
	}

	var buf []byte
	put32 := func(v uint32) { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); buf = append(buf, b...) }
	put16 := func(v uint16) { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); buf = append(buf, b...) }

	dataLen := uint32(len(samples) * 2)
	buf = append(buf, []byte("RIFF")...)
	put32(36 + dataLen)
	buf = append(buf, []byte("WAVEfmt ")...)
	put32(16)
	put16(1) // PCM
	put16(1) // mono
	put32(rate)
	put32(rate * 2)
	put16(2)
	put16(16)
	buf = append(buf, []byte("data")...)
	put32(dataLen)
	for _, s := range samples {
		put16(uint16(s))
	}

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write wav: %v", err)
	}
}

func TestReadWAVMono(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.wav")
	synthWAV(t, path, 120, []float64{1, 0.5, 0.25}, 1.0)

	samples, rate, err := readWAVMono(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rate != 16000 {
		t.Errorf("rate = %d, want 16000", rate)
	}
	if len(samples) != 16000 {
		t.Errorf("samples = %d, want 16000", len(samples))
	}
	for _, s := range samples {
		if s < -1.01 || s > 1.01 {
			t.Fatalf("sample %f outside normalised range", s)
		}
	}
}

func TestReadWAVRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.wav")
	if err := os.WriteFile(path, []byte("this is definitely not a wav file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWAVMono(path); err == nil {
		t.Fatal("garbage input accepted as WAV")
	}
}

func TestFFTAgainstKnownSignal(t *testing.T) {
	// A pure sinusoid at bin k must concentrate its energy in bin k.
	const n = 512
	const k = 8
	re := make([]float64, n)
	im := make([]float64, n)
	for i := range n {
		re[i] = math.Sin(2 * math.Pi * float64(k) * float64(i) / float64(n))
	}
	fft(re, im)

	peak, peakMag := 0, 0.0
	for i := 1; i < n/2; i++ {
		if mag := math.Hypot(re[i], im[i]); mag > peakMag {
			peak, peakMag = i, mag
		}
	}
	if peak != k {
		t.Fatalf("FFT peak at bin %d, expected %d", peak, k)
	}
}

func TestMelScaleRoundTrip(t *testing.T) {
	for _, hz := range []float64{100, 440, 1000, 4000, 8000} {
		if got := melToHz(hzToMel(hz)); math.Abs(got-hz) > 0.5 {
			t.Errorf("mel round trip for %.0f Hz gave %.2f", hz, got)
		}
	}
}

func TestEmbeddingIsStableAndDiscriminating(t *testing.T) {
	dir := t.TempDir()
	me1 := filepath.Join(dir, "me1.wav")
	me2 := filepath.Join(dir, "me2.wav")
	other := filepath.Join(dir, "other.wav")

	// Same "voice", two takes of differing length.
	synthWAV(t, me1, 120, []float64{1, 0.5, 0.3, 0.15}, 2.0)
	synthWAV(t, me2, 122, []float64{1, 0.48, 0.31, 0.16}, 1.6)
	// A markedly different timbre.
	synthWAV(t, other, 240, []float64{1, 0.1, 0.9, 0.05}, 2.0)

	embMe1, err := embedAudio(me1)
	if err != nil {
		t.Fatalf("embed me1: %v", err)
	}
	embMe2, err := embedAudio(me2)
	if err != nil {
		t.Fatalf("embed me2: %v", err)
	}
	embOther, err := embedAudio(other)
	if err != nil {
		t.Fatalf("embed other: %v", err)
	}

	if len(embMe1) != embeddingDims {
		t.Fatalf("embedding has %d dims, want %d", len(embMe1), embeddingDims)
	}

	same := similarity(embMe1, embMe2)
	diff := similarity(embMe1, embOther)
	t.Logf("same-voice similarity %.3f, different-voice similarity %.3f", same, diff)

	if same <= diff {
		t.Errorf("cannot discriminate: same=%.3f is not above different=%.3f", same, diff)
	}
	if same < 0.5 {
		t.Errorf("two takes of the same voice scored only %.3f", same)
	}
}

func TestEmbeddingIsUnitLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.wav")
	synthWAV(t, path, 150, []float64{1, 0.4, 0.2}, 1.5)

	emb, err := embedAudio(path)
	if err != nil {
		t.Fatal(err)
	}
	var norm float64
	for _, v := range emb {
		norm += v * v
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-9 {
		t.Errorf("embedding norm = %f, want 1", math.Sqrt(norm))
	}
}

func TestEmbedRejectsTooShortAudio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blip.wav")
	synthWAV(t, path, 120, []float64{1}, 0.02) // 20 ms
	if _, err := embedAudio(path); err == nil {
		t.Fatal("20ms of audio should not produce a voiceprint")
	}
}

func TestEnrollmentAndVerification(t *testing.T) {
	dir := t.TempDir()
	audio := t.TempDir()

	var enrolPaths []string
	for i, f := range []float64{120, 121, 119, 122} {
		p := filepath.Join(audio, "enrol"+string(rune('a'+i))+".wav")
		synthWAV(t, p, f, []float64{1, 0.5, 0.3, 0.15}, 2.0)
		enrolPaths = append(enrolPaths, p)
	}

	v := NewMFCCVerifier(dir)
	if v.Enrolled() {
		t.Fatal("fresh verifier reports an enrolled profile")
	}
	if _, _, err := v.Verify(context.Background(), enrolPaths[0]); err == nil {
		t.Fatal("verifying without a profile should error")
	}

	profile, err := v.Enroll(context.Background(), "owner", enrolPaths)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if len(profile.Samples) != 4 {
		t.Fatalf("profile holds %d samples, want 4", len(profile.Samples))
	}
	if profile.SelfSimilarity < 0.5 {
		t.Errorf("enrolment samples disagree with each other: %.3f", profile.SelfSimilarity)
	}

	// The owner speaking again must be accepted.
	ownerAgain := filepath.Join(audio, "again.wav")
	synthWAV(t, ownerAgain, 120.5, []float64{1, 0.5, 0.29, 0.16}, 1.8)
	ok, score, err := v.Verify(context.Background(), ownerAgain)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owner re-verification: ok=%v score=%.3f", ok, score)
	if !ok {
		t.Errorf("owner rejected with score %.3f", score)
	}

	// A different voice must score lower.
	stranger := filepath.Join(audio, "stranger.wav")
	synthWAV(t, stranger, 260, []float64{1, 0.05, 0.95, 0.02}, 1.8)
	strangerOK, strangerScore, err := v.Verify(context.Background(), stranger)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stranger: ok=%v score=%.3f", strangerOK, strangerScore)

	// Scoring merely *lower* than the owner is not good enough — an earlier
	// version scored strangers at 0.984 against an owner's 1.000 and admitted
	// them all. The stranger must actually be turned away.
	if strangerOK {
		t.Errorf("stranger ACCEPTED with score %.3f (threshold %.2f)", strangerScore, defaultThreshold)
	}
	// And the two populations must be separated by a usable margin, not a
	// rounding error, or the threshold cannot be tuned meaningfully.
	if gap := score - strangerScore; gap < 0.15 {
		t.Errorf("owner/stranger margin is only %.3f (owner %.3f, stranger %.3f); "+
			"too narrow to threshold reliably", gap, score, strangerScore)
	}

	// A profile must survive a restart.
	v2 := NewMFCCVerifier(dir)
	if !v2.Enrolled() {
		t.Fatal("profile did not persist")
	}

	// And the voiceprint must not be world-readable.
	info, err := os.Stat(filepath.Join(dir, profileFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("voiceprint mode %o, want 600", perm)
	}
}

func TestEnrollRequiresEnoughSamples(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "one.wav")
	synthWAV(t, p, 120, []float64{1, 0.5}, 2.0)

	v := NewMFCCVerifier(dir)
	if _, err := v.Enroll(context.Background(), "owner", []string{p}); err == nil {
		t.Fatal("enrolling from a single sample should be refused")
	}
}

func TestForgetRemovesProfile(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i, f := range []float64{120, 121, 119} {
		p := filepath.Join(dir, "s"+string(rune('a'+i))+".wav")
		synthWAV(t, p, f, []float64{1, 0.5, 0.3}, 2.0)
		paths = append(paths, p)
	}
	v := NewMFCCVerifier(dir)
	if _, err := v.Enroll(context.Background(), "owner", paths); err != nil {
		t.Fatal(err)
	}
	if err := v.Forget(); err != nil {
		t.Fatal(err)
	}
	if v.Enrolled() {
		t.Fatal("profile still present after Forget")
	}
	if NewMFCCVerifier(dir).Enrolled() {
		t.Fatal("profile file still on disk after Forget")
	}
}

func TestParsePolicy(t *testing.T) {
	cases := map[string]Policy{
		"off": PolicyOff, "disabled": PolicyOff,
		"enforce": PolicyEnforce, "strict": PolicyEnforce,
		"warn": PolicyWarn, "": PolicyWarn, "nonsense": PolicyWarn,
	}
	for in, want := range cases {
		if got := ParsePolicy(in); got != want {
			t.Errorf("ParsePolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForSpeechStripsMarkup(t *testing.T) {
	in := "Check `df -h` and see [the docs](https://example.com/x). " +
		"**Important:** visit https://foo.bar\n" +
		"- first item\n- second item\n" +
		"```go\nfmt.Println()\n```"
	got := ForSpeech(in)

	for _, unwanted := range []string{"`", "**", "https://", "```", "]("} {
		if strings.Contains(got, unwanted) {
			t.Errorf("markup %q survived: %s", unwanted, got)
		}
	}
	for _, wanted := range []string{"df -h", "the docs", "Important", "first item"} {
		if !strings.Contains(got, wanted) {
			t.Errorf("content %q was lost: %s", wanted, got)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	got := splitSentences("First one. Second one! Third? Yes.")
	if len(got) != 4 {
		t.Fatalf("got %d sentences: %q", len(got), got)
	}

	// Decimals must not be treated as sentence ends.
	got = splitSentences("The drive is 99.9 percent full.")
	if len(got) != 1 {
		t.Errorf("decimal split the sentence: %q", got)
	}

	// Unpunctuated walls still get broken up for streaming.
	long := strings.Repeat("word ", 200)
	if got = splitSentences(long); len(got) < 2 {
		t.Errorf("long unpunctuated text produced %d chunks", len(got))
	}

	if got = splitSentences(""); len(got) != 0 {
		t.Errorf("empty input produced %q", got)
	}
}

func TestCleanTranscript(t *testing.T) {
	cases := map[string]string{
		"  hello there  ":  "hello there",
		"[BLANK_AUDIO]":    "",
		"[silence]":        "",
		`"quoted reply"`:   "quoted reply",
		"":                 "",
		"Normal sentence.": "Normal sentence.",
	}
	for in, want := range cases {
		if got := cleanTranscript(in); got != want {
			t.Errorf("cleanTranscript(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- benchmarks -------------------------------------------------------------

// BenchmarkEmbedAudio measures the cost of turning one utterance into a
// voiceprint. This is the whole per-turn cost of speaker verification, so it
// is the number that decides whether the feature is worth its latency.
func BenchmarkEmbedAudio(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "bench.wav")

	// Five seconds is a realistic spoken command.
	const rate = 16000
	n := int(rate * 5)
	samples := make([]int16, n)
	for i := range n {
		tt := float64(i) / rate
		v := math.Sin(2*math.Pi*120*tt) + 0.5*math.Sin(2*math.Pi*240*tt) +
			0.3*math.Sin(2*math.Pi*360*tt)
		v *= 0.6 + 0.4*math.Sin(2*math.Pi*3*tt)
		samples[i] = int16(v * 10000)
	}
	var buf []byte
	put32 := func(v uint32) { x := make([]byte, 4); binary.LittleEndian.PutUint32(x, v); buf = append(buf, x...) }
	put16 := func(v uint16) { x := make([]byte, 2); binary.LittleEndian.PutUint16(x, v); buf = append(buf, x...) }
	dataLen := uint32(len(samples) * 2)
	buf = append(buf, []byte("RIFF")...)
	put32(36 + dataLen)
	buf = append(buf, []byte("WAVEfmt ")...)
	put32(16)
	put16(1)
	put16(1)
	put32(rate)
	put32(rate * 2)
	put16(2)
	put16(16)
	buf = append(buf, []byte("data")...)
	put32(dataLen)
	for _, s := range samples {
		put16(uint16(s))
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for b.Loop() {
		if _, err := embedAudio(path); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFFT(b *testing.B) {
	re := make([]float64, fftSize)
	im := make([]float64, fftSize)
	for i := range re {
		re[i] = math.Sin(float64(i))
	}
	b.ResetTimer()
	for b.Loop() {
		copy(im, make([]float64, fftSize))
		fft(re, im)
	}
}

func TestDetectWake(t *testing.T) {
	woken := []struct{ transcript, wantCommand string }{
		{"hey freya what's my disk at", "what's my disk at"},
		{"Freya, check the build", "check the build"},
		{"hey Freyja open my assignment folder", "open my assignment folder"},
		{"ok fraya remind me in ten minutes", "remind me in ten minutes"},
		{"so, freya, what time is it?", "what time is it?"},
		{"hey freya", ""},
		{"FREYA STOP", "STOP"},
	}
	for _, tc := range woken {
		w, ok := DetectWake(tc.transcript)
		if !ok {
			t.Errorf("missed the wake word in %q", tc.transcript)
			continue
		}
		if w.Command != tc.wantCommand {
			t.Errorf("%q -> command %q, want %q", tc.transcript, w.Command, tc.wantCommand)
		}
	}

	// Ordinary conversation must not trigger it.
	ignored := []string{
		"", "what's the weather like",
		"I was talking to my friend about the project",
		"the frequency was too high",
		"free your mind",
	}
	for _, s := range ignored {
		if _, ok := DetectWake(s); ok {
			t.Errorf("woke on ordinary speech: %q", s)
		}
	}
}

func TestWakeToleratesRecognitionVariants(t *testing.T) {
	// Recognisers do not reliably render an invented name, and matching only
	// the exact spelling would mean ignoring the user most of the time.
	for _, spelling := range []string{
		"hey freya go", "hey freyja go", "hey fraya go",
		"hey freyer go", "hey freia go", "hey friar go",
	} {
		if _, ok := DetectWake(spelling); !ok {
			t.Errorf("did not recognise the name in %q", spelling)
		}
	}
}

func TestListenerRefusesWithoutDevices(t *testing.T) {
	l := &Listener{}
	if err := l.Start(context.Background()); err == nil {
		t.Fatal("started listening with no recorder or recogniser")
	}
}

// TestListenerStopsWhenIdle is the safeguard against listening forever because
// somebody turned it on and forgot.
func TestListenerStopsWhenIdle(t *testing.T) {
	l := &Listener{
		Recorder:          silentRecorder{},
		Recognizer:        emptyRecognizer{},
		InactivityTimeout: 250 * time.Millisecond,
		TempDir:           t.TempDir(),
	}
	var reason string
	var mu sync.Mutex
	l.OnStop = func(r string) { mu.Lock(); reason = r; mu.Unlock() }

	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for l.Listening() {
		select {
		case <-deadline:
			l.Stop()
			t.Fatal("listener never timed out")
		case <-time.After(50 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(reason, "listening off") {
		t.Errorf("stop reason unclear: %q", reason)
	}
}

func TestListenerStopsOnRequest(t *testing.T) {
	l := &Listener{
		Recorder:   silentRecorder{},
		Recognizer: emptyRecognizer{},
		Indefinite: true,
		TempDir:    t.TempDir(),
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !l.Listening() {
		t.Fatal("not listening after start")
	}
	l.Stop()
	deadline := time.After(3 * time.Second)
	for l.Listening() {
		select {
		case <-deadline:
			t.Fatal("did not stop on request")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type silentRecorder struct{}

func (silentRecorder) Name() string { return "silent" }
func (silentRecorder) Record(ctx context.Context, path string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(60 * time.Millisecond):
	}
	return os.WriteFile(path, make([]byte, 8), 0o644) // below the audio threshold
}

type emptyRecognizer struct{}

func (emptyRecognizer) Name() string { return "empty" }
func (emptyRecognizer) Transcribe(context.Context, string) (string, error) {
	return "", nil
}
