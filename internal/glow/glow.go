package glow

import (
	"context"
	"math"
	"sync"
	"time"
)

// The indicator itself.
//
// # Making "subtle but noticeable" a design rule
//
// Those pull in opposite directions, and the resolution is motion rather than
// brightness. A static bright bar is noticeable and quickly irritating; a
// static dim bar is subtle and easily missed. A dim bar with a slow travelling
// highlight is caught by peripheral vision — which is tuned for movement — while
// never being bright enough to compete with what you are actually reading.
//
// Four pixels tall, because the strip must be visible without displacing
// anything. It sits over the very top of the screen where almost nothing is
// ever rendered.

const (
	// stripHeight is thin enough to ignore, thick enough to see.
	stripHeight = 4
	// frameInterval is about 20fps: smooth enough for a slow sweep, and far
	// too little work to notice on any machine.
	frameInterval = 50 * time.Millisecond
	// sweepPeriod is one full pass of the highlight.
	sweepPeriod = 2600 * time.Millisecond
	// segments is how many rectangles make up the gradient. More is smoother
	// and costs more requests; 96 across 1920 pixels is 20px each, which reads
	// as continuous.
	segments = 96
)

// What the colour means.
//
// # Why colour and speed together
//
// Colour alone is a poor status signal at four pixels tall in peripheral
// vision, and it fails outright for the roughly one man in twelve who cannot
// separate red from green. So each state also has its own sweep speed: a slow
// drift when idle, a measured pass when listening, a quick one when working.
// The two cues are redundant on purpose — either one alone is enough to read
// the state, so neither has to be looked at directly.
type State int

const (
	// StateIdle is running but not listening: red, slow, dim. The metaphor is a
	// standby light — present, not attending.
	StateIdle State = iota
	// StateListening is attending: blue, the original.
	StateListening
	// StateBusy is working on something: amber, quicker.
	StateBusy
)

// String names a state.
func (s State) String() string {
	switch s {
	case StateListening:
		return "listening"
	case StateBusy:
		return "busy"
	default:
		return "idle"
	}
}

// palette is one state's appearance.
//
// Base is the colour of the dim bar; peak is what gets added at the centre of
// the sweep. Keeping them separate means the resting brightness can be tuned
// independently of how loud the highlight is, which is the whole trick behind
// "subtle but noticeable".
type palette struct {
	baseR, baseG, baseB float64
	peakR, peakG, peakB float64
	// period is one full pass of the highlight.
	period time.Duration
	// spread is the width of the gaussian: a wider highlight reads as softer.
	spread float64
}

var palettes = map[State]palette{
	// Red, and deliberately the dimmest of the three. Red at any real
	// brightness reads as an error, and this state is the opposite of an error
	// — it is the resting state, which she will be in most of the time.
	StateIdle: {
		baseR: 26, baseG: 4, baseB: 6,
		peakR: 150, peakG: 18, peakB: 26,
		period: 4200 * time.Millisecond,
		spread: 0.060,
	},
	// Blue into cyan. Unchanged: this one was tuned against the real screen and
	// approved, so it is the fixed point the other two are built around.
	StateListening: {
		baseR: 6, baseG: 20, baseB: 42,
		peakR: 24, peakG: 150, peakB: 190,
		period: 2600 * time.Millisecond,
		spread: 0.045,
	},
	// Amber rather than a pure yellow. Pure yellow needs green at full strength
	// to look yellow at all, and at that brightness a four-pixel strip glares.
	StateBusy: {
		baseR: 34, baseG: 22, baseB: 2,
		peakR: 200, peakG: 150, peakB: 12,
		period: 1300 * time.Millisecond,
		spread: 0.038,
	},
}

// Indicator is a screen-top glow.
type Indicator struct {
	mu      sync.Mutex
	conn    *conn
	window  uint32
	gc      uint32
	width   uint16
	running bool
	cancel  context.CancelFunc
	state   State
	// phase carries the sweep position across a state change, so switching
	// colour does not make the highlight jump back to the left edge.
	phase float64
}

// New prepares an indicator without showing it.
func New() (*Indicator, error) {
	c, err := dial()
	if err != nil {
		return nil, err
	}
	if err := c.queryShape(); err != nil {
		c.close()
		return nil, err
	}

	width := c.screenWidth
	// Base colour is a deep blue rather than black: the strip should read as
	// deliberate even at its dimmest point.
	wid := c.createWindow(0, 0, width, stripHeight, 0x00000d1f)
	if err := c.makeClickThrough(wid); err != nil {
		c.close()
		return nil, err
	}
	gc := c.createGC(wid)

	return &Indicator{conn: c, window: wid, gc: gc, width: width, state: StateListening}, nil
}

// Showing reports whether the glow is visible.
func (in *Indicator) Showing() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.running
}

// State reports what the strip is currently showing.
func (in *Indicator) State() State {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state
}

// SetState changes the colour without interrupting the sweep.
//
// Safe to call whether or not the strip is visible: a state set while hidden
// takes effect the next time it is shown, which lets a caller track state
// unconditionally instead of guarding every call.
func (in *Indicator) SetState(s State) {
	in.mu.Lock()
	changed := in.state != s
	in.state = s
	in.mu.Unlock()

	// Repaint at once rather than waiting up to a frame, so a state change
	// feels like a response to what just happened.
	if changed && in.Showing() {
		in.drawFrame(in.currentPhase())
	}
}

func (in *Indicator) currentPhase() float64 {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.phase
}

// ShowState makes the strip visible in a given state.
func (in *Indicator) ShowState(ctx context.Context, s State) {
	in.SetState(s)
	in.Show(ctx)
}

// Show maps the strip and starts animating it.
func (in *Indicator) Show(ctx context.Context) {
	in.mu.Lock()
	if in.running {
		in.mu.Unlock()
		return
	}
	in.running = true
	animCtx, cancel := context.WithCancel(ctx)
	in.cancel = cancel
	in.conn.mapWindow(in.window)
	in.mu.Unlock()

	go in.animate(animCtx)
}

// Hide unmaps the strip.
func (in *Indicator) Hide() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.running {
		return
	}
	in.running = false
	if in.cancel != nil {
		in.cancel()
		in.cancel = nil
	}
	in.conn.unmapWindow(in.window)
}

// Close releases the connection.
func (in *Indicator) Close() error {
	in.Hide()
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.conn.close()
}

// animate redraws the gradient until cancelled.
func (in *Indicator) animate(ctx context.Context) {
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	raiseEvery := time.NewTicker(3 * time.Second)
	defer raiseEvery.Stop()

	last := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-raiseEvery.C:
			in.mu.Lock()
			if in.running {
				in.conn.raise(in.window)
			}
			in.mu.Unlock()
		case now := <-ticker.C:
			// Phase advances by elapsed-time-over-period rather than being
			// computed from a fixed start. That is what lets the period change
			// with the state without the highlight teleporting: the position is
			// carried forward, only its rate changes.
			elapsed := now.Sub(last).Seconds()
			last = now

			in.mu.Lock()
			period := palettes[in.state].period.Seconds()
			in.phase = math.Mod(in.phase+elapsed/period, 1.0)
			phase := in.phase
			in.mu.Unlock()

			in.drawFrame(phase)
		}
	}
}

// drawFrame paints one frame of the sweep.
func (in *Indicator) drawFrame(phase float64) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.running {
		return
	}

	p, ok := palettes[in.state]
	if !ok {
		p = palettes[StateListening]
	}

	segWidth := int(in.width) / segments
	if segWidth < 1 {
		segWidth = 1
	}

	for i := range segments {
		pos := float64(i) / float64(segments)

		// Distance from the highlight, measured the short way round so the
		// sweep wraps continuously instead of jumping at the edge.
		d := math.Abs(pos - phase)
		if d > 0.5 {
			d = 1 - d
		}

		// A narrow gaussian: bright at the centre of the sweep, falling away
		// quickly enough that most of the bar stays dim.
		intensity := math.Exp(-(d * d) / (2 * p.spread * p.spread))

		r := clamp8(p.baseR + p.peakR*intensity)
		g := clamp8(p.baseG + p.peakG*intensity)
		b := clamp8(p.baseB + p.peakB*intensity)

		in.conn.setForeground(in.gc, r<<16|g<<8|b)
		x := int16(i * segWidth)
		w := uint16(segWidth)
		if i == segments-1 {
			// The last segment absorbs the rounding remainder, so the strip
			// reaches the right edge exactly.
			w = in.width - uint16(x)
		}
		in.conn.fillRect(in.window, in.gc, x, 0, w, stripHeight)
	}
}

// clamp8 keeps a channel inside a byte.
//
// The palettes are all within range, but a value that wrapped would show up as
// a bright wrong-coloured band rather than as a slightly-too-bright one, which
// is a disproportionate result for an arithmetic slip.
func clamp8(v float64) uint32 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint32(v)
}
