package glow

import (
	"math"
	"testing"
)

// These exercise the parts that do not need an X server: the palettes, the
// colour arithmetic, and the phase advance. Anything touching the wire is left
// to running it, since a mock X server would test the mock.

func TestEveryStateHasAPalette(t *testing.T) {
	for _, s := range []State{StateIdle, StateListening, StateBusy} {
		p, ok := palettes[s]
		if !ok {
			t.Errorf("state %s has no palette, so it would fall back to blue", s)
			continue
		}
		if p.period <= 0 {
			t.Errorf("state %s has a non-positive period, which would divide by zero", s)
		}
		if p.spread <= 0 {
			t.Errorf("state %s has a non-positive spread", s)
		}
	}
}

func TestStateNames(t *testing.T) {
	cases := map[State]string{
		StateIdle: "idle", StateListening: "listening", StateBusy: "busy",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q want %q", s, got, want)
		}
	}
}

// TestStatesAreVisuallyDistinct checks the thing the feature is for: at a
// glance, the three states must not look alike.
func TestStatesAreVisuallyDistinct(t *testing.T) {
	// The dominant channel at peak brightness is what the eye reads.
	dominant := func(s State) string {
		p := palettes[s]
		r, g, b := p.baseR+p.peakR, p.baseG+p.peakG, p.baseB+p.peakB
		switch {
		case r > g && r > b:
			return "red"
		case b > r && b > g:
			return "blue"
		case g >= r && g > b:
			return "green"
		}
		return "none"
	}

	if got := dominant(StateIdle); got != "red" {
		t.Errorf("idle reads as %s, should be red", got)
	}
	if got := dominant(StateListening); got != "blue" {
		t.Errorf("listening reads as %s, should be blue", got)
	}
	// Amber is red-dominant with substantial green and almost no blue, which is
	// what separates it from the plain red of idle.
	busy := palettes[StateBusy]
	if busy.peakG < busy.peakR/3 {
		t.Error("busy has too little green to read as amber rather than red")
	}
	if busy.peakB > busy.peakG/2 {
		t.Error("busy has too much blue to read as amber")
	}
}

// TestIdleIsDimmest matters because red at brightness reads as an alarm, and
// idle is the state she will spend most of her time in.
func TestIdleIsDimmest(t *testing.T) {
	brightness := func(s State) float64 {
		p := palettes[s]
		return p.baseR + p.baseG + p.baseB
	}
	if brightness(StateIdle) > brightness(StateBusy) {
		t.Error("idle rests brighter than busy — a standby light should not shout")
	}
}

// TestBusySweepsFastest gives the colour-blind path its own signal.
func TestBusySweepsFastest(t *testing.T) {
	if palettes[StateBusy].period >= palettes[StateListening].period {
		t.Error("busy should sweep faster than listening")
	}
	if palettes[StateListening].period >= palettes[StateIdle].period {
		t.Error("listening should sweep faster than idle")
	}
}

func TestClamp8(t *testing.T) {
	cases := map[float64]uint32{
		-50: 0, 0: 0, 127.9: 127, 255: 255, 300: 255, 1e9: 255,
	}
	for in, want := range cases {
		if got := clamp8(in); got != want {
			t.Errorf("clamp8(%v) = %d want %d", in, got, want)
		}
	}
}

// TestPeakStaysInRange is what clamp8 protects against: a palette whose base
// plus peak exceeds a byte would wrap to a bright wrong colour.
func TestPeakStaysInRange(t *testing.T) {
	for _, s := range []State{StateIdle, StateListening, StateBusy} {
		p := palettes[s]
		for _, v := range []float64{p.baseR + p.peakR, p.baseG + p.peakG, p.baseB + p.peakB} {
			if v > 255 {
				t.Errorf("state %s peaks at %v, above a byte", s, v)
			}
		}
	}
}

// TestPhaseAdvanceIsContinuousAcrossAPeriodChange is the reason phase is
// carried rather than recomputed from a start time: switching state mid-sweep
// must not make the highlight jump.
func TestPhaseAdvanceIsContinuousAcrossAPeriodChange(t *testing.T) {
	phase := 0.5
	const step = 0.05 // seconds per frame

	advance := func(p float64, period float64) float64 {
		return math.Mod(p+step/period, 1.0)
	}

	// A frame at the listening rate, then one at the busy rate.
	slow := advance(phase, palettes[StateListening].period.Seconds())
	fast := advance(slow, palettes[StateBusy].period.Seconds())

	// Each step must be small: a jump would be a visible teleport.
	if d := slow - phase; d <= 0 || d > 0.1 {
		t.Errorf("a frame moved the phase by %v, which is not a smooth step", d)
	}
	if d := fast - slow; d <= 0 || d > 0.1 {
		t.Errorf("after a state change the phase moved by %v, which would be visible as a jump", d)
	}
	// And the faster state must genuinely move further per frame.
	if (fast - slow) <= (slow - phase) {
		t.Error("the busy state is not advancing faster per frame")
	}
}

func TestPhaseWrapsRatherThanGrowing(t *testing.T) {
	phase := 0.0
	for range 1000 {
		phase = math.Mod(phase+0.05/palettes[StateBusy].period.Seconds(), 1.0)
		if phase < 0 || phase >= 1 {
			t.Fatalf("phase left [0,1): %v", phase)
		}
	}
}

func TestUnknownStateFallsBackRatherThanGoingBlack(t *testing.T) {
	// drawFrame looks the state up and falls back; a missing entry must not
	// produce an invisible bar.
	if _, ok := palettes[State(99)]; ok {
		t.Skip("99 is a real state now")
	}
	p, ok := palettes[State(99)]
	if !ok {
		p = palettes[StateListening]
	}
	if p.baseB == 0 && p.baseR == 0 && p.baseG == 0 {
		t.Error("the fallback palette is black, so an unknown state would be invisible")
	}
}
