package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/akins/jarvis/internal/guard"
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

// attend tells the guard whether anyone can actually answer a confirmation.
//
// This used to be a ConfirmFunc that printed "refused (no interactive terminal
// to confirm)" to stderr and returned false. Returning false is how a user says
// no, so the guard reported ErrDenied — "declined by user" — and that is what
// reached the model, the reply and the defect report, for an action no user had
// been shown. The explanation went to stderr, which is neither in the loop nor
// in front of the user. Asked to write hello.html in a session with no terminal,
// she stopped after one tool call and said she had been refused.
//
// So the fact is declared instead of being faked: unattended, and the guard
// says so in its own words.
func attend(g *guard.Guard, interactive bool) {
	if interactive {
		return
	}
	g.Attended = func() bool { return false }
}

// isTerminal reports whether stdin is an interactive terminal.
func isTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
