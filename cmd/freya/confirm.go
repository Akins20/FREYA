package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"sync"

	"github.com/Akins20/FREYA/internal/guard"
	"github.com/Akins20/FREYA/internal/gui"
)

// confirmPrompt asks the user to approve an action.
//
// Two deliberate design choices:
//
// Destructive actions require the word "yes" typed in full, not "y". The extra
// second of friction is the point — muscle memory presses y before the eyes
// have finished reading, and that is precisely the moment this exists to catch.
//
// Anything unparsed is a no. A prompt that treats ambiguity as consent is worse
// than no prompt, because it looks like a safeguard.
func confirmPrompt(in *bufio.Reader) guard.ConfirmFunc {
	return func(ctx context.Context, action guard.Action, a guard.Assessment) bool {
		fmt.Println()
		fmt.Printf("%s┌─ confirmation required ─────────────────────────────%s\n", cYellow, cReset)

		cmd := commandText(action)
		fmt.Printf("%s│%s %s%s%s\n", cYellow, cReset, cBold, cmd, cReset)

		if action.Reason != "" {
			fmt.Printf("%s│%s %sreason:%s %s\n", cYellow, cReset, cDim, cReset, action.Reason)
		}

		riskColour := cYellow
		if a.Risk >= guard.RiskHigh {
			riskColour = cRed
		}
		fmt.Printf("%s│%s risk: %s%s%s", cYellow, cReset, riskColour, a.Risk, cReset)
		if !a.Reversible {
			fmt.Printf(" %s· cannot be undone%s", cRed, cReset)
		}
		fmt.Println()

		for _, reason := range a.Reasons {
			fmt.Printf("%s│%s   %s· %s%s\n", cYellow, cReset, cDim, reason, cReset)
		}
		if a.Preview != "" {
			fmt.Printf("%s│%s %seffect:%s %s\n", cYellow, cReset, cBold, cReset, a.Preview)
		}
		fmt.Printf("%s└─────────────────────────────────────────────────────%s\n", cYellow, cReset)

		// High risk demands the full word; lower risk accepts y.
		if a.Risk >= guard.RiskHigh {
			fmt.Printf("  type %syes%s to proceed, anything else to cancel: ", cBold, cReset)
		} else {
			fmt.Print("  proceed? [y/N]: ")
		}

		line, err := in.ReadString('\n')
		if err != nil {
			fmt.Printf("%s  cancelled%s\n\n", cDim, cReset)
			return false
		}
		answer := strings.ToLower(strings.TrimSpace(line))

		ok := answer == "yes"
		if a.Risk < guard.RiskHigh {
			ok = answer == "y" || answer == "yes"
		}

		if ok {
			fmt.Printf("%s  proceeding%s\n\n", cDim, cReset)
		} else {
			fmt.Printf("%s  cancelled%s\n\n", cDim, cReset)
		}
		return ok
	}
}

// commandText renders what will actually run.
func commandText(action guard.Action) string {
	var s string
	switch {
	case action.Shell != "":
		s = action.Shell
	case action.Command != "":
		s = action.Command
		if len(action.Args) > 0 {
			s += " " + strings.Join(action.Args, " ")
		}
	default:
		s = string(action.Kind)
		if len(action.Paths) > 0 {
			s += " " + strings.Join(action.Paths, " ")
		}
	}
	if action.Elevated && !strings.HasPrefix(s, "sudo") {
		s = "sudo " + s
	}
	return s
}

// confirmRoutes is every channel a question can reach a person on.
//
// # Why this is a router and not a field
//
// guard.Guard has one Confirm and one Attended, both read without a lock
// (guard.go:237,266,282). The code that knows about each channel comes up at a
// different moment — the terminal at startup, voice after the recorder opens,
// the window when one is served — so each used to *overwrite* the field as it
// arrived. Two consequences, both real: installing voiceConfirm in the daemon
// meant the terminal could never be asked again, and every one of those
// assignments raced a goroutine already calling Run.
//
// So the field is set once, here, and the channels register with this instead.
// Nothing after startup touches the guard.
type confirmRoutes struct {
	mu       sync.RWMutex
	window   *gui.Server
	voice    guard.ConfirmFunc
	terminal guard.ConfirmFunc
}

// newConfirmRoutes installs itself on the guard. Call it before anything can run
// an action, and never assign g.Confirm or g.Attended again.
func newConfirmRoutes(g *guard.Guard, terminal guard.ConfirmFunc) *confirmRoutes {
	c := &confirmRoutes{terminal: terminal}
	g.Confirm = c.confirm
	g.Attended = c.attended
	return c
}

func (c *confirmRoutes) setWindow(s *gui.Server) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.window = s
}

func (c *confirmRoutes) setVoice(f guard.ConfirmFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.voice = f
}

// attended reports whether any channel could carry the question.
//
// Deliberately not "is there a terminal". A daemon with a window open or a
// microphone live is attended; a piped session with neither is not, and the
// guard then refuses in its own words rather than reporting a refusal nobody
// made.
//
// That distinction was bought the hard way. The headless case used to be a
// ConfirmFunc that printed "refused (no interactive terminal to confirm)" to
// stderr and returned false — but returning false is how a user says no, so the
// guard reported ErrDenied, "declined by user", and that is what reached the
// model, the reply and the defect report for an action no user had been shown.
// The explanation went to stderr, which is neither in the loop nor in front of
// anyone. Asked to write hello.html in a session with no terminal, she stopped
// after one tool call and said she had been refused.
func (c *confirmRoutes) attended() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.window != nil && c.window.HasWindow() {
		return true
	}
	return c.voice != nil || c.terminal != nil
}

// confirm asks the first channel that can answer.
//
// Order: window, terminal, voice. The plan for this said window-voice-terminal;
// the swap is deliberate. A window and a terminal are both places somebody is
// looking and typing, and their answer is exactly the word they meant. A spoken
// answer goes through the recorder, transcription and a yes/no parse, any of
// which can turn "no" into "I didn't catch that" — so speech is the channel of
// last resort, used when it is the only one there is, which in the daemon it
// usually is.
//
// A channel that was asked owns the answer, including a no and including a
// timeout. Falling through to the next one on a refusal would mean asking the
// same person the same question twice through different hardware until one of
// them said yes.
func (c *confirmRoutes) confirm(ctx context.Context, action guard.Action, a guard.Assessment) bool {
	c.mu.RLock()
	window, voice, terminal := c.window, c.voice, c.terminal
	c.mu.RUnlock()

	if window != nil {
		ok, asked := window.Confirm(ctx, commandText(action), action.Reason,
			a.Risk.String(), a.Preview)
		if asked {
			return ok
		}
	}
	if terminal != nil {
		return terminal(ctx, action, a)
	}
	if voice != nil {
		return voice(ctx, action, a)
	}
	// Unreachable while attended() is honest, and a deny if it ever is not.
	return false
}
