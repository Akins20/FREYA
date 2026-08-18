//go:build js && wasm

// The motion layer of the film, in the same language the assistant is written in.
//
// # Why this is not CSS
//
// Everything a stylesheet can move, the stylesheet moves: windows, cards, type.
// What is left over is the per-pixel work, and there are four pieces of it that
// carry the film:
//
//	grain      temporally coherent film grain, not per-frame white noise
//	dust       slow motes drifting through the pool of light on the desk
//	dissolve   scene changes cut through a noise field instead of cross-fading
//	bloom      a soft amber lift where the accent lives, so it reads as light
//
// All four are the same shape of problem: read a value per pixel, write four
// bytes. That is a loop over 360,000 pixels thirty times a second, which is
// exactly the work a compiled language should be doing and exactly the work a
// per-frame JavaScript loop makes you feel.
//
// # Half resolution on purpose
//
// The stage is 1600x900 and this renders 800x450, scaled up by the canvas. Grain
// and bloom have no high-frequency detail worth preserving, dust is round, and
// the dissolve edge is noise-modulated, so nothing here is sharpened by the
// missing pixels. It quarters the work and quarters the bytes crossing into JS.
//
// # The buffer is allocated once
//
// A frame is 1.44MB of RGBA. Allocating that per frame would hand the garbage
// collector 43MB a second and put a stutter in a film whose whole argument is
// that nothing jitters. So the pixel buffer, the noise tables and the mote
// positions are all built at init and reused.
package main

import (
	"math"
	"syscall/js"
)

const (
	// Noise is sampled from a fixed table rather than computed per pixel. The
	// table is a power of two so the index masks instead of dividing.
	noiseBits = 16
	noiseSize = 1 << noiseBits
	noiseMask = noiseSize - 1
)

var (
	w, h  int
	px    []byte    // the additive plate: grain, dust, bloom
	cut   []byte    // the cut plate: the dissolve, which has to be able to darken
	noise []float32 // white noise, sampled with a moving offset
	fbm   []float32 // low frequency field, for the dissolve edge
	motes []mote

	jsPixels js.Value // the Uint8ClampedArray the additive canvas reads from
	jsCut    js.Value // and the one the cut canvas reads from
)

type mote struct {
	x, y   float32
	vx, vy float32
	r      float32
	bright float32
}

// A tiny deterministic generator. Nothing here should differ between two runs of
// the capture, or the same second of film renders differently on the second take.
type rng struct{ s uint32 }

func (r *rng) next() uint32 {
	r.s ^= r.s << 13
	r.s ^= r.s >> 17
	r.s ^= r.s << 5
	return r.s
}

func (r *rng) f32() float32 { return float32(r.next()&0xFFFFFF) / float32(0x1000000) }

func initFX(this js.Value, args []js.Value) any {
	w = args[0].Int()
	h = args[1].Int()
	px = make([]byte, w*h*4)
	cut = make([]byte, w*h*4)

	r := rng{s: 0x9E3779B9}

	noise = make([]float32, noiseSize)
	for i := range noise {
		noise[i] = r.f32()
	}

	// Value noise smoothed twice. Two passes is enough for a field that only has
	// to look like drifting density, and cheaper than a real fBm octave stack.
	fbm = make([]float32, w*h)
	for i := range fbm {
		fbm[i] = r.f32()
	}
	for pass := 0; pass < 2; pass++ {
		smooth(fbm, w, h)
	}

	motes = make([]mote, 58)
	for i := range motes {
		motes[i] = mote{
			x:      r.f32() * float32(w),
			y:      r.f32() * float32(h),
			vx:     (r.f32() - 0.5) * 0.10,
			vy:     -0.04 - r.f32()*0.07, // drift upward, the way dust does in a beam
			r:      0.6 + r.f32()*1.3,
			bright: 0.18 + r.f32()*0.55,
		}
	}

	jsPixels = js.Global().Get("Uint8ClampedArray").New(len(px))
	jsCut = js.Global().Get("Uint8ClampedArray").New(len(cut))
	js.Global().Set("fxPixels", jsPixels)
	js.Global().Set("fxCut", jsCut)
	return nil
}

// smooth is a separable box blur over a scalar field, in place.
func smooth(f []float32, w, h int) {
	tmp := make([]float32, len(f))
	const rad = 6
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			var sum float32
			var n int
			for d := -rad; d <= rad; d++ {
				xx := x + d
				if xx < 0 || xx >= w {
					continue
				}
				sum += f[row+xx]
				n++
			}
			tmp[row+x] = sum / float32(n)
		}
	}
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			var sum float32
			var n int
			for d := -rad; d <= rad; d++ {
				yy := y + d
				if yy < 0 || yy >= h {
					continue
				}
				sum += tmp[yy*w+x]
				n++
			}
			f[y*w+x] = sum / float32(n)
		}
	}
}

// frame renders one frame of the effects layer.
//
//	t         seconds since the film started
//	grainAmt  0..1
//	dustAmt   0..1
//	wipe      0..1, how far a dissolve has travelled. 0 means no dissolve.
//	warm      0..1, how much amber bloom sits over the plate
//	flip      mirror the dissolve, so covering and uncovering read as one sweep
//
// Returns true when the cut plate has anything in it, so the compositor can skip
// a full-frame draw on the ninety-nine per cent of frames that are not a cut.
func frame(this js.Value, args []js.Value) any {
	t := float32(args[0].Float())
	grainAmt := float32(args[1].Float())
	dustAmt := float32(args[2].Float())
	wipe := float32(args[3].Float())
	warm := float32(args[4].Float())
	flip := args[5].Bool()

	// Grain that is re-randomised every frame flickers like broken television.
	// Real film grain moves, but the same grain persists across a few frames, so
	// the sample offset walks rather than jumps.
	off := int(t*9.0) * 7919 // a prime stride keeps the walk from repeating early
	shimmer := 0.55 + 0.45*float32(math.Sin(float64(t)*0.7))

	// A dissolve travels left to right, but the edge is pushed around by the low
	// frequency field so it eats the frame in patches instead of as a clean bar.
	wipeActive := wipe > 0.0001

	fw := float32(w)
	fh := float32(h)

	for y := 0; y < h; y++ {
		row := y * w
		fy := float32(y) / fh
		for x := 0; x < w; x++ {
			i := row + x
			n := noise[(i+off)&noiseMask]

			var v float32

			if grainAmt > 0 {
				// Centred on zero so grain lifts and darkens equally. A grain that
				// only ever adds light turns the black grey.
				v += (n - 0.5) * grainAmt * 255 * shimmer
			}

			if warm > 0 {
				// A wide soft lift, brightest where the desk light pools.
				// The pool sits over the left third, where the conversation lives, so
				// the bright paper on the right is not blown out by it.
				fx := float32(x) / fw
				dx := fx - 0.28
				dy := fy - 0.46
				d := dx*dx*1.1 + dy*dy*2.0
				lift := warm * 26 * float32(math.Exp(float64(-d*4.5)))
				v += lift
			}

			c := v
			if c < 0 {
				c = 0
			}
			// Amber tint: the red channel takes the full lift, green most of it,
			// blue very little. That is what makes the light read as tungsten
			// rather than as a grey fog.
			r := c
			g := c * 0.78
			b := c * 0.40

			px[i*4+0] = clamp(r)
			px[i*4+1] = clamp(g)
			px[i*4+2] = clamp(b)
			px[i*4+3] = 255

			if wipeActive {
				// The dissolve cannot live on the additive plate: screen blending
				// only ever lightens, and a cut has to be able to take the picture
				// away. So it gets its own plate, drawn over the top with alpha.
				fx := float32(x) / fw
				if flip {
					fx = 1 - fx
				}
				// The edge is pushed around by the low frequency field, so the frame
				// is eaten in patches rather than swept by a clean bar.
				edge := wipe*1.3 - 0.15 + (fbm[i]-0.5)*0.34
				band := edge - fx
				switch {
				case band <= 0:
					cut[i*4+3] = 0
				case band < 0.03:
					// right at the edge it flares, the way a cut in film does
					f := 1 - band/0.03
					cut[i*4+0] = clamp(240 * f)
					cut[i*4+1] = clamp(168 * f)
					cut[i*4+2] = clamp(60 * f)
					cut[i*4+3] = clamp(255 * (0.55 + 0.45*f))
				default:
					cut[i*4+0] = 8
					cut[i*4+1] = 8
					cut[i*4+2] = 9
					cut[i*4+3] = 255
				}
			}
		}
	}

	if dustAmt > 0 {
		drawMotes(t, dustAmt)
	}

	js.CopyBytesToJS(jsPixels, px)
	if wipeActive {
		js.CopyBytesToJS(jsCut, cut)
	}
	return wipeActive
}

// drawMotes advances and paints the dust. They are drawn after the field so the
// grain does not eat them, and they wrap rather than respawn so the density
// stays constant instead of pulsing.
func drawMotes(t, amt float32) {
	fw := float32(w)
	fh := float32(h)
	for i := range motes {
		m := &motes[i]
		m.x += m.vx
		m.y += m.vy
		// A slow lateral sway, phase-offset per mote so they never move as a sheet.
		sway := float32(math.Sin(float64(t*0.6)+float64(i)*0.7)) * 0.16
		m.x += sway * 0.3

		if m.y < -4 {
			m.y = fh + 4
		}
		if m.x < -4 {
			m.x = fw + 4
		}
		if m.x > fw+4 {
			m.x = -4
		}

		// Motes fade in and out over long periods, so the field never looks like a
		// fixed constellation.
		life := 0.5 + 0.5*float32(math.Sin(float64(t*0.35)+float64(i)*1.31))
		b := m.bright * life * amt * 165
		if b < 2 {
			continue
		}

		rad := int(m.r + 1)
		cx := int(m.x)
		cy := int(m.y)
		for dy := -rad; dy <= rad; dy++ {
			yy := cy + dy
			if yy < 0 || yy >= h {
				continue
			}
			for dx := -rad; dx <= rad; dx++ {
				xx := cx + dx
				if xx < 0 || xx >= w {
					continue
				}
				d := float32(dx*dx + dy*dy)
				fall := 1 - d/(m.r*m.r+1)
				if fall <= 0 {
					continue
				}
				v := b * fall * fall
				j := (yy*w + xx) * 4
				px[j+0] = clamp(float32(px[j+0]) + v)
				px[j+1] = clamp(float32(px[j+1]) + v*0.82)
				px[j+2] = clamp(float32(px[j+2]) + v*0.52)
			}
		}
	}
}

func clamp(v float32) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v)
}

func main() {
	js.Global().Set("fxInit", js.FuncOf(initFX))
	js.Global().Set("fxFrame", js.FuncOf(frame))
	js.Global().Get("document").Call("dispatchEvent",
		js.Global().Get("Event").New("fx-ready"))
	select {} // the exports have to outlive main
}
