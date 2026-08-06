package bench

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The cap-exhaustion metric is one of the gate numbers, and it has already been
// silently zero once — it matched a sentence in the reply that had been reworded.
// Rounds are the durable signal, but only while this constant tracks the agent's.
func TestRoundCapConstantTracksTheAgent(t *testing.T) {
	src, err := os.ReadFile("../agent/agent.go")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("const maxToolRounds = %d", agentRoundCap)
	if !strings.Contains(string(src), want) {
		t.Fatalf("agent's round cap no longer matches bench's %d — the cap-exhaustion "+
			"metric will read zero and the gate will pass on a number that measures nothing",
			agentRoundCap)
	}
}
