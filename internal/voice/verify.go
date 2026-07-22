package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Speaker verification — deciding whether the voice that just spoke is the
// person Freya belongs to.
//
// # Measured accuracy: not good enough to enforce
//
// Read this before trusting anything below.
//
// This implementation pools mel-cepstral statistics and scores them against an
// enrolled profile. On synthetic test tones it separates speakers cleanly. On
// real speech it does not. Measured against four espeak voices, enrolling one
// and testing the others:
//
//	voice                    cosine    population
//	OWNER (unseen phrase)     0.981         0.561
//	stranger en-us            0.963         0.435
//	stranger en-sc            0.980         0.539   <- indistinguishable
//	stranger en+f3            0.722         0.219
//
// The nearest impostor lands 0.022 below the owner. No threshold separates
// them. Every voice tested was accepted at the default setting.
//
// One caveat in the method's favour: all four samples came from the same
// formant synthesiser, so they share machinery that distinct human vocal tracts
// would not. Real voices may well separate better. That is a hypothesis, not a
// result, and it is why the default policy is PolicyWarn — the gate reports
// what it sees and gets out of the way. Use /voice test to measure your own
// voice against a real second person before ever setting PolicyEnforce.
//
// # What this is, and what it is not
//
// Even working perfectly this would be acoustic *discrimination*, not
// authentication. It cannot resist a recording of your voice, a decent
// impersonator, or anyone with shell access, who can edit the profile or run
// the binary directly.
//
// A neural speaker embedding (ECAPA-TDNN or similar, via ONNX) is the real
// answer and would be markedly more accurate. Verifier is an interface
// precisely so one can be dropped in without touching the session loop.

// Verifier decides whether audio came from the enrolled speaker.
type Verifier interface {
	Name() string
	// Verify scores audio against the profile. Returns the decision, a
	// confidence in [0,1], and any error.
	Verify(ctx context.Context, audioPath string) (ok bool, score float64, err error)
	// Enrolled reports whether a profile exists to compare against.
	Enrolled() bool
}

// Policy controls what happens when a voice does not match.
type Policy string

const (
	// PolicyOff disables verification entirely.
	PolicyOff Policy = "off"
	// PolicyWarn notes the mismatch but answers anyway. Useful while tuning
	// the threshold, since it surfaces scores without locking you out.
	PolicyWarn Policy = "warn"
	// PolicyEnforce refuses to act on unrecognised voices.
	PolicyEnforce Policy = "enforce"
)

// ParsePolicy reads a policy name, defaulting to warn.
func ParsePolicy(s string) Policy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "disabled", "none":
		return PolicyOff
	case "enforce", "strict", "on":
		return PolicyEnforce
	default:
		return PolicyWarn
	}
}

// Profile is an enrolled voice.
type Profile struct {
	Name    string      `json:"name"`
	Created time.Time   `json:"created"`
	Updated time.Time   `json:"updated"`
	Samples []Embedding `json:"samples"`
	// Threshold is the minimum similarity to accept. Zero uses the default.
	Threshold float64 `json:"threshold,omitempty"`
	// SelfSimilarity records how consistently the enrolment samples matched
	// each other, which is the honest measure of how much to trust the profile.
	SelfSimilarity float64 `json:"self_similarity,omitempty"`
}

// defaultThreshold applies to the population score. It sits where the owner
// comfortably passes, accepting that impostors may too — being locked out of
// your own assistant is worse than it occasionally answering someone else,
// given this is explicitly not a security boundary. Tune it against real
// measurements from /voice test rather than trusting this number.
const defaultThreshold = 0.45

// stdFloor regularises per-dimension standard deviations. With only a handful
// of enrolment samples the estimates are noisy, and an unregularised near-zero
// deviation would make one incidental dimension dominate the whole score.
const stdFloor = 0.02

// minEnrolmentSamples is the fewest recordings that produce a usable profile.
// Fewer than three and the profile captures one mood, one posture, one
// distance from the microphone.
const minEnrolmentSamples = 3

const profileFile = "voiceprint.json"

// MFCCVerifier compares voices using mel-cepstral embeddings.
type MFCCVerifier struct {
	Dir     string // where voiceprint.json lives
	profile *Profile
}

// NewMFCCVerifier loads any existing profile from dir.
func NewMFCCVerifier(dir string) *MFCCVerifier {
	v := &MFCCVerifier{Dir: dir}
	v.load()
	return v
}

func (v *MFCCVerifier) Name() string { return "mfcc" }

// Enrolled reports whether a usable profile exists.
func (v *MFCCVerifier) Enrolled() bool {
	return v.profile != nil && len(v.profile.Samples) > 0
}

// Profile returns the loaded profile, or nil.
func (v *MFCCVerifier) Profile() *Profile { return v.profile }

func (v *MFCCVerifier) path() string { return filepath.Join(v.Dir, profileFile) }

func (v *MFCCVerifier) load() {
	b, err := os.ReadFile(v.path())
	if err != nil {
		return
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return
	}
	v.profile = &p
}

func (v *MFCCVerifier) save() error {
	b, err := json.MarshalIndent(v.profile, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.Dir, 0o755); err != nil {
		return err
	}
	tmp := v.path() + ".tmp"
	// The voiceprint is biometric data; keep it owner-readable only.
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.path())
}

// Enroll builds a profile from recordings of the owner's voice.
//
// It also measures how well the samples agree with each other. Poor agreement
// means the enrolment was noisy, and the caller should be told rather than
// left with a profile that rejects its own owner.
func (v *MFCCVerifier) Enroll(ctx context.Context, name string, audioPaths []string) (*Profile, error) {
	if len(audioPaths) < minEnrolmentSamples {
		return nil, fmt.Errorf("need at least %d recordings to enrol, got %d",
			minEnrolmentSamples, len(audioPaths))
	}

	var embeddings []Embedding
	var failures []string
	for _, path := range audioPaths {
		emb, err := embedAudioAny(ctx, path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		embeddings = append(embeddings, emb)
	}
	if len(embeddings) < minEnrolmentSamples {
		return nil, fmt.Errorf("only %d of %d recordings were usable: %s",
			len(embeddings), len(audioPaths), strings.Join(failures, "; "))
	}

	// Average pairwise similarity across the enrolment set tells us how tight
	// the profile is, and therefore how much the threshold can be trusted.
	var total float64
	var pairs int
	for i := range embeddings {
		for j := i + 1; j < len(embeddings); j++ {
			total += similarity(embeddings[i], embeddings[j])
			pairs++
		}
	}
	selfSim := 0.0
	if pairs > 0 {
		selfSim = total / float64(pairs)
	}

	now := time.Now()
	p := &Profile{
		Name:           name,
		Created:        now,
		Updated:        now,
		Samples:        embeddings,
		SelfSimilarity: selfSim,
	}
	if v.profile != nil {
		p.Created = v.profile.Created
		p.Threshold = v.profile.Threshold
	}
	v.profile = p
	if err := v.save(); err != nil {
		return nil, fmt.Errorf("save voiceprint: %w", err)
	}
	return p, nil
}

// populationScore measures how far a candidate sits from the enrolment set,
// weighting each dimension by how consistent the enrolment samples were in it.
//
// This beats plain cosine similarity because it asks the right question. Cosine
// asks "does this look broadly like the owner", and every human voice does.
// This asks "does this deviate along the axes where the owner is reliably
// stable", which is where an impostor actually shows up. Measured on real
// audio it roughly doubled the owner/impostor margin — from 0.001 to 0.022 —
// which is still not enough to enforce, but is the better of the two.
func populationScore(samples []Embedding, x Embedding) float64 {
	if len(samples) == 0 || len(x) == 0 {
		return 0
	}
	dims := len(x)

	mean := make([]float64, dims)
	for _, s := range samples {
		for d := range dims {
			mean[d] += s[d]
		}
	}
	for d := range dims {
		mean[d] /= float64(len(samples))
	}

	var sumSq float64
	for d := range dims {
		var variance float64
		for _, s := range samples {
			variance += (s[d] - mean[d]) * (s[d] - mean[d])
		}
		std := math.Sqrt(variance/float64(len(samples))) + stdFloor
		z := (x[d] - mean[d]) / std
		sumSq += z * z
	}

	// Root-mean-square deviation mapped into (0,1]: 1 is identical, 0 is far.
	rms := math.Sqrt(sumSq / float64(dims))
	return 1.0 / (1.0 + rms)
}

// Verify scores audio against the enrolled profile.
func (v *MFCCVerifier) Verify(ctx context.Context, audioPath string) (bool, float64, error) {
	if !v.Enrolled() {
		return false, 0, fmt.Errorf("no voice profile enrolled")
	}
	emb, err := embedAudioAny(ctx, audioPath)
	if err != nil {
		return false, 0, err
	}

	score := populationScore(v.profile.Samples, emb)
	threshold := v.profile.Threshold
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	return score >= threshold, score, nil
}

// SetThreshold adjusts the acceptance threshold and persists it.
func (v *MFCCVerifier) SetThreshold(t float64) error {
	if v.profile == nil {
		return fmt.Errorf("no profile enrolled")
	}
	if t < 0 || t > 1 {
		return fmt.Errorf("threshold must be between 0 and 1")
	}
	v.profile.Threshold = t
	v.profile.Updated = time.Now()
	return v.save()
}

// Forget deletes the enrolled voiceprint.
func (v *MFCCVerifier) Forget() error {
	v.profile = nil
	if err := os.Remove(v.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Describe summarises the profile for display.
func (v *MFCCVerifier) Describe() string {
	if !v.Enrolled() {
		return "no voice enrolled — run /voice enroll"
	}
	p := v.profile
	threshold := p.Threshold
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	quality := "good"
	switch {
	case p.SelfSimilarity < 0.75:
		quality = "poor — consider re-enrolling somewhere quieter"
	case p.SelfSimilarity < 0.85:
		quality = "fair"
	}
	return fmt.Sprintf("%s · %d samples · threshold %.2f · consistency %.2f (%s) · enrolled %s",
		p.Name, len(p.Samples), threshold, p.SelfSimilarity, quality,
		p.Created.Format("2 Jan 2006"))
}

// embedAudioAny accepts any format sox can read, converting to the PCM WAV the
// analyser needs.
func embedAudioAny(ctx context.Context, path string) (Embedding, error) {
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		return embedAudio(path)
	}
	if !have("sox") {
		return nil, fmt.Errorf("sox is required to analyse %s files", filepath.Ext(path))
	}
	tmp := path + ".analysis.wav"
	defer os.Remove(tmp)

	cmd := exec.CommandContext(ctx, "sox", path,
		"-r", "16000", "-c", "1", "-b", "16", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("convert for analysis: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return embedAudio(tmp)
}

// EnrollmentPhrases are varied on purpose. Reading the same sentence repeatedly
// produces a profile that only recognises that sentence's particular rhythm.
var EnrollmentPhrases = []string{
	"The quick brown fox jumps over the lazy dog near the river bank.",
	"I need you to check my system status and tell me what needs attention.",
	"Remind me to review the project notes before the end of the week.",
	"Search the web for the latest news and summarise what you find.",
	"This is my voice, and I want you to recognise it whenever I speak.",
}

// ScoreReport holds diagnostic scores from a verification check.
type ScoreReport struct {
	Score     float64
	Threshold float64
	Accepted  bool
	PerSample []float64
}

// Diagnose scores audio against every enrolment sample, for tuning.
func (v *MFCCVerifier) Diagnose(ctx context.Context, audioPath string) (*ScoreReport, error) {
	if !v.Enrolled() {
		return nil, fmt.Errorf("no voice profile enrolled")
	}
	emb, err := embedAudioAny(ctx, audioPath)
	if err != nil {
		return nil, err
	}

	report := &ScoreReport{Threshold: v.profile.Threshold}
	if report.Threshold <= 0 {
		report.Threshold = defaultThreshold
	}
	// Per-sample cosine figures are diagnostic only; the decision uses the
	// population score, so report exactly what Verify would decide on.
	for _, sample := range v.profile.Samples {
		report.PerSample = append(report.PerSample, similarity(emb, sample))
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(report.PerSample)))
	report.Score = populationScore(v.profile.Samples, emb)
	report.Accepted = report.Score >= report.Threshold
	return report, nil
}
