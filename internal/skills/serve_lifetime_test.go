package skills

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/akins/jarvis/internal/term"
)

// A server outlives the exchange that started it, and dies with her.
//
// # What the user asked, and why it needed proving rather than reading
//
// "If she runs a server, it stays running until she explicitly closes it or she
// restarts, right?" The code says yes — term.Manager holds its sessions and
// nothing closes them per turn; the only CloseAll is deferred from run(), which
// returns when the process exits. But every server started during a day of
// testing was dead afterwards, because every one of those runs was a one-shot
// -ask, where the process exits the moment the answer is printed. Reading the
// code and watching the machine gave opposite answers, and the code was right
// about the daemon while the machine was right about what had actually been run.
//
// So this pins the property the answer depends on: a session survives an
// arbitrary number of turn boundaries, and stops only when something stops it.
func TestAServerOutlivesTheExchangeAndDiesWithHer(t *testing.T) {
	if !have("python3") {
		t.Skip("serving needs python3")
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	m := term.NewManager()
	name := fmt.Sprintf("serve-%d", port)

	s, err := m.Start(name, "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(fmt.Sprintf("python3 -m http.server %d", port)); err != nil {
		t.Fatal(err)
	}
	if !waitListening(port, 10*time.Second) {
		t.Fatalf("the server never came up on %d", port)
	}

	// Turns end. Nothing in the loop touches the manager, so this is what the
	// exchange boundary actually looks like from the server's point of view:
	// nothing at all.
	for turn := 1; turn <= 3; turn++ {
		time.Sleep(120 * time.Millisecond)
		if !answers(port) {
			t.Fatalf("the server stopped answering after turn %d — a URL she handed over "+
				"would be dead with nothing to say so", turn)
		}
	}

	// And it is still listed as hers, which is what serve_list reads.
	var listed bool
	for _, sess := range m.List() {
		if sess.Name == name {
			listed = true
		}
	}
	if !listed {
		t.Error("the session vanished from the manager while its server was still up")
	}

	// Explicitly stopped: gone.
	if err := m.Close(name); err != nil {
		t.Fatal(err)
	}
	if !gone(port, 5*time.Second) {
		t.Error("serve_stop left the server running")
	}
}

// A one-shot run takes its servers with it, which is the case that made every
// URL handed over during testing dead on arrival — and is why serve reports it.
func TestAOneShotRunTakesItsServersWithIt(t *testing.T) {
	if !have("python3") {
		t.Skip("serving needs python3")
	}
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	m := term.NewManager()
	s, err := m.Start(fmt.Sprintf("serve-%d", port), "", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(fmt.Sprintf("python3 -m http.server %d", port)); err != nil {
		t.Fatal(err)
	}
	if !waitListening(port, 10*time.Second) {
		t.Fatalf("the server never came up on %d", port)
	}

	// run()'s deferred CloseAll, which is what the end of a -ask process is.
	m.CloseAll()
	if !gone(port, 5*time.Second) {
		t.Error("a one-shot run left an orphan server behind")
	}
}

// answers asks the port rather than the bookkeeping, the same as serve_list.
func answers(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	// A listening socket is not proof of a server; ask for a page.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func gone(port int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !answers(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
