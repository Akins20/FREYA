package voice

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// Mel-frequency cepstral analysis, implemented here rather than pulled in as a
// dependency because it is a few hundred lines of well-specified arithmetic and
// keeps the binary free-standing.
//
// The pipeline is the standard one:
//
//	pre-emphasis -> framing -> Hamming window -> FFT -> power spectrum
//	-> mel filterbank -> log -> DCT -> cepstral mean/variance normalisation
//
// The output is a fixed-length vector summarising the timbre of a voice, which
// is what makes two recordings of the same person comparable.

const (
	frameLength = 400 // 25 ms at 16 kHz
	frameStep   = 160 // 10 ms hop
	fftSize     = 512 // next power of two above frameLength
	melFilters  = 26
	cepstra     = 13
	preEmphasis = 0.97
	melLowHz    = 300.0
	melHighHz   = 8000.0 // Nyquist at 16 kHz
)

// readWAVMono reads 16-bit PCM WAV samples, mixing to mono and normalising to
// the range [-1, 1].
func readWAVMono(path string) ([]float64, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read wav: %w", err)
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a RIFF/WAVE file")
	}

	var (
		numChannels   int
		sampleRateHz  int
		bitsPerSample int
		data          []byte
	)

	// Walk the chunk list rather than assuming a 44-byte header; real encoders
	// insert LIST and fact chunks ahead of the data.
	for offset := 12; offset+8 <= len(raw); {
		id := string(raw[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		body := offset + 8
		if size < 0 || body+size > len(raw) {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, 0, fmt.Errorf("truncated fmt chunk")
			}
			numChannels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			sampleRateHz = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			bitsPerSample = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
		case "data":
			data = raw[body : body+size]
		}
		offset = body + size
		if size%2 == 1 {
			offset++ // chunks are word-aligned
		}
	}

	if data == nil {
		return nil, 0, fmt.Errorf("no data chunk")
	}
	if bitsPerSample != 16 {
		return nil, 0, fmt.Errorf("expected 16-bit PCM, got %d-bit", bitsPerSample)
	}
	if numChannels < 1 {
		numChannels = 1
	}

	frames := len(data) / 2 / numChannels
	out := make([]float64, frames)
	for i := range frames {
		var sum float64
		for c := range numChannels {
			idx := (i*numChannels + c) * 2
			sum += float64(int16(binary.LittleEndian.Uint16(data[idx : idx+2])))
		}
		out[i] = sum / float64(numChannels) / 32768.0
	}
	return out, sampleRateHz, nil
}

// hzToMel converts frequency to the mel scale, which spaces bands the way human
// hearing resolves them: finely at low frequencies, coarsely at high ones.
func hzToMel(hz float64) float64  { return 2595 * math.Log10(1+hz/700) }
func melToHz(mel float64) float64 { return 700 * (math.Pow(10, mel/2595) - 1) }

// fft computes an in-place radix-2 Cooley-Tukey transform. len(re) must be a
// power of two.
func fft(re, im []float64) {
	n := len(re)
	if n <= 1 {
		return
	}

	// Bit-reversal permutation.
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}

	for length := 2; length <= n; length <<= 1 {
		angle := -2 * math.Pi / float64(length)
		wRe, wIm := math.Cos(angle), math.Sin(angle)
		for i := 0; i < n; i += length {
			curRe, curIm := 1.0, 0.0
			for j := 0; j < length/2; j++ {
				uRe, uIm := re[i+j], im[i+j]
				vRe := re[i+j+length/2]*curRe - im[i+j+length/2]*curIm
				vIm := re[i+j+length/2]*curIm + im[i+j+length/2]*curRe

				re[i+j], im[i+j] = uRe+vRe, uIm+vIm
				re[i+j+length/2], im[i+j+length/2] = uRe-vRe, uIm-vIm

				nextRe := curRe*wRe - curIm*wIm
				curIm = curRe*wIm + curIm*wRe
				curRe = nextRe
			}
		}
	}
}

// melFilterbank builds triangular filters spanning melLowHz to melHighHz.
func melFilterbank(sampleRateHz int) [][]float64 {
	high := math.Min(melHighHz, float64(sampleRateHz)/2)
	lowMel, highMel := hzToMel(melLowHz), hzToMel(high)

	// melFilters+2 points give each triangle a left, centre and right edge.
	points := make([]int, melFilters+2)
	for i := range points {
		mel := lowMel + (highMel-lowMel)*float64(i)/float64(melFilters+1)
		hz := melToHz(mel)
		points[i] = int(math.Floor((fftSize + 1) * hz / float64(sampleRateHz)))
	}

	bank := make([][]float64, melFilters)
	for m := 1; m <= melFilters; m++ {
		filter := make([]float64, fftSize/2+1)
		left, centre, right := points[m-1], points[m], points[m+1]
		for k := left; k < centre && k < len(filter); k++ {
			if centre > left {
				filter[k] = float64(k-left) / float64(centre-left)
			}
		}
		for k := centre; k < right && k < len(filter); k++ {
			if right > centre {
				filter[k] = float64(right-k) / float64(right-centre)
			}
		}
		bank[m-1] = filter
	}
	return bank
}

// extractMFCC computes cepstral coefficients for every voiced frame.
func extractMFCC(samples []float64, sampleRateHz int) [][]float64 {
	if len(samples) < frameLength {
		return nil
	}

	// Pre-emphasis lifts the high frequencies that carry speaker identity and
	// that microphones attenuate.
	emphasised := make([]float64, len(samples))
	emphasised[0] = samples[0]
	for i := 1; i < len(samples); i++ {
		emphasised[i] = samples[i] - preEmphasis*samples[i-1]
	}

	window := make([]float64, frameLength)
	for i := range window {
		window[i] = 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(frameLength-1))
	}

	bank := melFilterbank(sampleRateHz)
	var frames [][]float64

	for start := 0; start+frameLength <= len(emphasised); start += frameStep {
		frame := emphasised[start : start+frameLength]

		// Skip near-silent frames: they describe the room, not the speaker.
		var energy float64
		for _, v := range frame {
			energy += v * v
		}
		if energy/float64(frameLength) < 1e-6 {
			continue
		}

		re := make([]float64, fftSize)
		im := make([]float64, fftSize)
		for i := range frame {
			re[i] = frame[i] * window[i]
		}
		fft(re, im)

		power := make([]float64, fftSize/2+1)
		for i := range power {
			power[i] = (re[i]*re[i] + im[i]*im[i]) / float64(fftSize)
		}

		energies := make([]float64, melFilters)
		for m, filter := range bank {
			var sum float64
			for k, w := range filter {
				if k < len(power) {
					sum += power[k] * w
				}
			}
			energies[m] = math.Log(math.Max(sum, 1e-10))
		}

		// DCT-II reduces the correlated filterbank energies to compact cepstra.
		coeffs := make([]float64, cepstra)
		for i := range cepstra {
			var sum float64
			for m := range melFilters {
				sum += energies[m] * math.Cos(math.Pi*float64(i)*(float64(m)+0.5)/float64(melFilters))
			}
			coeffs[i] = sum
		}
		frames = append(frames, coeffs)
	}
	return frames
}

// Embedding is a fixed-length summary of a voice.
type Embedding []float64

// embeddingDims is mean plus standard deviation for every cepstral coefficient
// except c0.
//
// c0 is excluded deliberately. It encodes overall frame energy — how loud the
// speaker was and how close to the microphone — not who they are. Worse, its
// magnitude dwarfs the higher coefficients, so leaving it in makes every
// normalised embedding point in nearly the same direction. Measured with c0
// present, two completely different voices still scored 0.984 against each
// other, which is indistinguishable from a genuine match.
const embeddingDims = (cepstra - 1) * 2

// embedAudio turns a WAV file into a comparable voice vector.
func embedAudio(wavPath string) (Embedding, error) {
	samples, rate, err := readWAVMono(wavPath)
	if err != nil {
		return nil, err
	}
	if rate <= 0 {
		return nil, fmt.Errorf("invalid sample rate")
	}

	frames := extractMFCC(samples, rate)
	if len(frames) < 10 {
		return nil, fmt.Errorf("too little speech to identify a voice (%d usable frames)", len(frames))
	}

	emb := make(Embedding, embeddingDims)
	for d := 1; d < cepstra; d++ { // skip c0
		var mean float64
		for _, f := range frames {
			mean += f[d]
		}
		mean /= float64(len(frames))

		var variance float64
		for _, f := range frames {
			variance += (f[d] - mean) * (f[d] - mean)
		}
		std := math.Sqrt(variance / float64(len(frames)))

		emb[d-1] = mean
		emb[(cepstra-1)+(d-1)] = std
	}
	return emb.centred().normalised(), nil
}

// centred subtracts the vector's own mean from each component.
//
// This turns the later cosine similarity into a Pearson correlation. Without
// it, all embeddings share a large common offset and cosine similarity mostly
// measures that shared component rather than the differences between voices,
// compressing every score into a narrow band near 1.0.
func (e Embedding) centred() Embedding {
	if len(e) == 0 {
		return e
	}
	var mean float64
	for _, v := range e {
		mean += v
	}
	mean /= float64(len(e))

	out := make(Embedding, len(e))
	for i, v := range e {
		out[i] = v - mean
	}
	return out
}

// normalised scales the vector to unit length so cosine similarity is a plain
// dot product and overall loudness stops mattering.
func (e Embedding) normalised() Embedding {
	var norm float64
	for _, v := range e {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm < 1e-12 {
		return e
	}
	out := make(Embedding, len(e))
	for i, v := range e {
		out[i] = v / norm
	}
	return out
}

// similarity is the cosine similarity of two unit vectors, in [-1, 1].
func similarity(a, b Embedding) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	return math.Max(-1, math.Min(1, dot))
}
