// Package hotkey grabs a single global keyboard shortcut through raw X11.
//
// # Why this exists and why it is raw protocol
//
// Push-to-talk needs a key that works everywhere, not only when a terminal is
// focused. That is a global grab, and on X11 the way to get one without a
// desktop environment's cooperation is XGrabKey on the root window. A toolkit
// would do it too, at the cost of a large dependency; the protocol itself is a
// connection handshake, one grab request, and an event loop — a few hundred
// lines and no dependency, consistent with the rest of this project.
//
// The scope is deliberately one key. A general hotkey manager would be more
// code and more ways to fail, and the daemon needs exactly one binding.
package hotkey

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// X protocol opcodes used here.
const (
	opGrabKey   = 33
	opUngrabKey = 34
)

// Event type codes. The high bit marks a synthetic event and is masked off.
const (
	evError    = 0
	evKeyPress = 2
)

// Modifier mask bits.
const (
	modShift   = 0x01
	modLock    = 0x02 // Caps Lock
	modControl = 0x04
	modMod2    = 0x10 // usually Num Lock
)

// spaceKeycode is the X keycode for the space bar.
//
// On every Linux X server using the evdev or libinput keyboard driver — which
// is all of them in practice — space is keycode 65. Querying the keyboard map
// to derive it would be more correct and, here, more code for a value that does
// not vary. FREYA_HOTKEY_KEYCODE overrides it for the vanishingly rare setup
// where it does.
const spaceKeycode = 65

// Grab is an active global hotkey grab.
type Grab struct {
	sock    net.Conn
	root    uint32
	keycode byte
}

// OpenCtrlSpace grabs Ctrl+Space globally and returns the grab.
//
// The grab is registered for every combination of the lock modifiers, because X
// treats "Ctrl+Space" and "Ctrl+Space while Num Lock is on" as different grabs;
// missing the combinations produces a key that mysteriously stops working when
// a lock key is toggled.
func OpenCtrlSpace() (*Grab, error) {
	sock, root, err := connect()
	if err != nil {
		return nil, err
	}

	keycode := byte(spaceKeycode)
	if v := os.Getenv("FREYA_HOTKEY_KEYCODE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 256 {
			keycode = byte(n)
		}
	}

	g := &Grab{sock: sock, root: root, keycode: keycode}

	// Grab Ctrl, and Ctrl with each toggle of Caps Lock and Num Lock present.
	for _, extra := range []uint16{0, modLock, modMod2, modLock | modMod2} {
		if err := g.grab(modControl | extra); err != nil {
			sock.Close()
			return nil, err
		}
	}
	return g, nil
}

// grab issues one GrabKey request.
func (g *Grab) grab(modifiers uint16) error {
	req := make([]byte, 16)
	req[0] = opGrabKey
	req[1] = 0 // owner-events false: send the events to us, the grabbing client
	binary.LittleEndian.PutUint16(req[2:4], 4)
	binary.LittleEndian.PutUint32(req[4:8], g.root)
	binary.LittleEndian.PutUint16(req[8:10], modifiers)
	req[10] = g.keycode
	req[11] = 1 // pointer-mode asynchronous
	req[12] = 1 // keyboard-mode asynchronous
	_, err := g.sock.Write(req)
	return err
}

// Listen blocks, invoking onPress each time the hotkey is pressed, until the
// context ends or the connection drops.
//
// A press fires the callback synchronously, and the callback is expected to
// return quickly or hand off to a goroutine: while it runs, later presses queue
// in the socket buffer rather than being lost, but holding the loop for the
// length of a whole spoken exchange would make a second press wait on the first.
// The daemon runs the exchange in a goroutine for exactly that reason.
func (g *Grab) Listen(ctx context.Context, onPress func()) error {
	// A reader goroutine turns blocking socket reads into a channel select, so
	// the context can cancel a Listen that is parked waiting for a key.
	events := make(chan []byte, 8)
	errs := make(chan error, 1)
	go func() {
		buf := make([]byte, 32)
		for {
			if _, err := readFull(g.sock, buf); err != nil {
				errs <- err
				return
			}
			cp := make([]byte, 32)
			copy(cp, buf)
			events <- cp
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			return err
		case ev := <-events:
			// Mask the synthetic-event bit before comparing the type.
			switch ev[0] & 0x7f {
			case evKeyPress:
				if ev[1] == g.keycode {
					onPress()
				}
			case evError:
				// A BadAccess here means another client already owns the key.
				// It is not fatal — the rest of the daemon runs fine without the
				// hotkey — so report it and keep going rather than dying.
				return fmt.Errorf("hotkey: X error %d grabbing the key (is it already bound by another app?)", ev[1])
			}
		}
	}
}

// Close releases the grab and the connection.
func (g *Grab) Close() error {
	if g == nil || g.sock == nil {
		return nil
	}
	for _, extra := range []uint16{0, modLock, modMod2, modLock | modMod2} {
		_, _ = g.sock.Write(ungrabRequest(g.root, g.keycode, modControl|extra))
	}
	return g.sock.Close()
}

// ungrabRequest builds an UngrabKey request: opcode, key, length, window,
// modifiers.
func ungrabRequest(root uint32, key byte, modifiers uint16) []byte {
	req := make([]byte, 12)
	req[0] = opUngrabKey
	req[1] = key
	binary.LittleEndian.PutUint16(req[2:4], 3)
	binary.LittleEndian.PutUint32(req[4:8], root)
	binary.LittleEndian.PutUint16(req[8:10], modifiers)
	return req
}

// --- connection ------------------------------------------------------------

// connect opens an authenticated X11 connection and returns the socket and the
// root window of the first screen.
func connect() (net.Conn, uint32, error) {
	display := os.Getenv("DISPLAY")
	if display == "" {
		return nil, 0, fmt.Errorf("hotkey: DISPLAY is not set")
	}
	num := strings.TrimPrefix(display, ":")
	if i := strings.IndexByte(num, '.'); i >= 0 {
		num = num[:i]
	}
	if _, err := strconv.Atoi(num); err != nil {
		return nil, 0, fmt.Errorf("hotkey: cannot parse DISPLAY %q", display)
	}

	sock, err := net.Dial("unix", "/tmp/.X11-unix/X"+num)
	if err != nil {
		return nil, 0, fmt.Errorf("hotkey: connect to X: %w", err)
	}

	authName, authData := readAuth(num)

	var req []byte
	req = append(req, 'l', 0)
	req = binary.LittleEndian.AppendUint16(req, 11)
	req = binary.LittleEndian.AppendUint16(req, 0)
	req = binary.LittleEndian.AppendUint16(req, uint16(len(authName)))
	req = binary.LittleEndian.AppendUint16(req, uint16(len(authData)))
	req = append(req, 0, 0)
	req = append(req, pad4([]byte(authName))...)
	req = append(req, pad4(authData)...)
	if _, err := sock.Write(req); err != nil {
		sock.Close()
		return nil, 0, err
	}

	header := make([]byte, 8)
	if _, err := readFull(sock, header); err != nil {
		sock.Close()
		return nil, 0, err
	}
	if header[0] != 1 {
		reason := make([]byte, int(header[1]))
		_, _ = readFull(sock, reason)
		sock.Close()
		return nil, 0, fmt.Errorf("hotkey: X refused the connection: %s",
			strings.TrimSpace(string(reason)))
	}

	body := make([]byte, int(binary.LittleEndian.Uint16(header[6:8]))*4)
	if _, err := readFull(sock, body); err != nil {
		sock.Close()
		return nil, 0, err
	}

	root, err := rootWindow(body)
	if err != nil {
		sock.Close()
		return nil, 0, err
	}
	return sock, root, nil
}

// rootWindow extracts the first screen's root window from the setup reply.
func rootWindow(b []byte) (uint32, error) {
	if len(b) < 32 {
		return 0, fmt.Errorf("hotkey: setup reply too short")
	}
	vendorLen := int(binary.LittleEndian.Uint16(b[16:18]))
	numFormats := int(b[21])
	offset := 32 + (vendorLen+3)/4*4 + numFormats*8
	if offset+4 > len(b) {
		return 0, fmt.Errorf("hotkey: setup reply truncated before the screen list")
	}
	return binary.LittleEndian.Uint32(b[offset : offset+4]), nil
}

// readAuth returns the MIT-MAGIC-COOKIE for the display, or empty values if none
// is found — an unauthenticated connection still works on a permissive server.
func readAuth(display string) (name string, data []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}

	i := 0
	read := func(n int) []byte {
		if i+n > len(raw) {
			i = len(raw) + 1
			return nil
		}
		b := raw[i : i+n]
		i += n
		return b
	}
	for i+2 <= len(raw) {
		if read(2) == nil { // family
			break
		}
		addr := read(2)
		if addr == nil || read(int(binary.BigEndian.Uint16(addr))) == nil {
			break
		}
		numLen := read(2)
		if numLen == nil {
			break
		}
		number := read(int(binary.BigEndian.Uint16(numLen)))
		if number == nil {
			break
		}
		nameLen := read(2)
		if nameLen == nil {
			break
		}
		nameBytes := read(int(binary.BigEndian.Uint16(nameLen)))
		if nameBytes == nil {
			break
		}
		dataLen := read(2)
		if dataLen == nil {
			break
		}
		dataBytes := read(int(binary.BigEndian.Uint16(dataLen)))
		if dataBytes == nil {
			break
		}
		if string(nameBytes) == "MIT-MAGIC-COOKIE-1" &&
			(string(number) == display || number == nil || len(number) == 0) {
			return string(nameBytes), append([]byte(nil), dataBytes...)
		}
	}
	return "", nil
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func readFull(c net.Conn, b []byte) (int, error) {
	read := 0
	for read < len(b) {
		n, err := c.Read(b[read:])
		if err != nil {
			return read, err
		}
		read += n
	}
	return read, nil
}
