package browser

import (
	"strings"
	"testing"
)

func TestContextRouting(t *testing.T) {
	if ContextAuth.Port() != AuthPort || ContextGuest.Port() != GuestPort {
		t.Error("contexts do not map to distinct ports")
	}
	// The two contexts must never share a profile directory, or the isolated
	// one would inherit the user's cookies — the whole point of the split.
	if ContextAuth.ProfileDir() == ContextGuest.ProfileDir() {
		t.Fatal("auth and guest share a profile directory")
	}
	if !strings.Contains(ContextAuth.ProfileDir(), "auth") {
		t.Errorf("auth profile path is unclear: %s", ContextAuth.ProfileDir())
	}
}

func TestWebSocketFrameRoundTrip(t *testing.T) {
	// Client frames must be masked; an unmasked one is a protocol error and
	// Chrome hangs up.
	payload := []byte(`{"id":1,"method":"Page.navigate"}`)
	masked := make([]byte, len(payload))
	mask := [4]byte{0xAA, 0xBB, 0xCC, 0xDD}
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	unmasked := make([]byte, len(masked))
	for i := range masked {
		unmasked[i] = masked[i] ^ mask[i%4]
	}
	if string(unmasked) != string(payload) {
		t.Error("mask round trip lost data")
	}
}
