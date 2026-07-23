// Package glow draws an ambient indicator along the top of the screen.
//
// # Why raw X11 rather than a toolkit
//
// The requirement is a thin strip that sits above everything, ignores the
// window manager, and cannot be clicked. GTK or Qt would do it, at the cost of
// a large dependency and a runtime that has to be present on the machine. The
// X11 core protocol needed for a coloured, click-through, always-on-top
// rectangle is a few hundred lines of byte packing and no dependencies at all.
//
// # The part that matters most
//
// Click-through. A strip across the top of the screen that swallows input would
// break every window title bar and menu underneath it, and would be discovered
// at the worst possible moment. The SHAPE extension solves it: setting the
// window's *input* region to nothing while leaving its visible region intact
// means pointer events pass straight through to whatever is behind.
package glow

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// X protocol opcodes used here.
const (
	opCreateWindow      = 1
	opChangeWindowAttrs = 2
	opMapWindow         = 8
	opUnmapWindow       = 10
	opConfigureWindow   = 12
	opCreateGC          = 55
	opChangeGC          = 56
	opPolyFillRectangle = 70
	opQueryExtension    = 98
)

// Window attribute value-mask bits.
const (
	cwBackPixel        = 0x00000002
	cwOverrideRedirect = 0x00000200
)

// Graphics context value-mask bits.
const gcForeground = 0x00000004

// SHAPE extension minor opcodes and constants.
const (
	shapeRectangles = 1
	shapeSet        = 0
	shapeKindInput  = 2
	shapeUnsorted   = 0
)

// conn is a connection to the X server.
type conn struct {
	mu     sync.Mutex
	sock   net.Conn
	nextID uint32
	idMask uint32
	idBase uint32
	seq    uint16

	root         uint32
	visual       uint32
	depth        byte
	screenWidth  uint16
	screenHeight uint16

	shapeOpcode byte

	// dead is set when a write fails or the drain sees the socket close. Reads
	// are lock-free so the animation loop can check liveness on every frame
	// without contending for the write lock.
	dead atomic.Bool
}

// drain reads and discards everything the server sends after the handshake.
//
// # Why a write-only X client cannot exist
//
// This overlay only ever writes: it draws and never asks a question. But the
// server still sends — errors, and any events the window generates — and an X
// server whose output to a client cannot drain will, once its buffer fills,
// stop servicing and eventually disconnect that client, destroying its windows.
// That is exactly the failure this had: the bar appeared, drew a few thousand
// requests a second with nobody reading the replies, and vanished a minute
// later with no error, because the error was the thing that could not be read.
//
// So one goroutine reads into a scratch buffer forever and throws it away. The
// point is not the bytes; it is that the server is never blocked on us.
func (c *conn) drain() {
	buf := make([]byte, 4096)
	for {
		n, err := c.sock.Read(buf)
		if err != nil {
			c.dead.Store(true)
			return
		}
		if n == 0 {
			c.dead.Store(true)
			return
		}
	}
}

// alive reports whether the connection is still usable.
func (c *conn) alive() bool { return !c.dead.Load() }

// authEntry is one record from .Xauthority.
type authEntry struct {
	display string
	name    string
	data    []byte
}

// readAuth finds the MIT-MAGIC-COOKIE for a display.
//
// Without it the server rejects the connection, and the failure is a bare
// "no protocol specified" that gives no hint that authorisation was the issue.
func readAuth(display string) (name string, data []byte, err error) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		path = filepath.Join(home, ".Xauthority")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("glow: read %s: %w", path, err)
	}

	var entries []authEntry
	for i := 0; i+2 <= len(raw); {
		read := func(n int) []byte {
			if i+n > len(raw) {
				i = len(raw) + 1
				return nil
			}
			b := raw[i : i+n]
			i += n
			return b
		}
		if read(2) == nil { // family
			break
		}
		addrLen := read(2)
		if addrLen == nil {
			break
		}
		if read(int(binary.BigEndian.Uint16(addrLen))) == nil {
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
		entries = append(entries, authEntry{
			display: string(number),
			name:    string(nameBytes),
			data:    append([]byte(nil), dataBytes...),
		})
	}

	// Prefer an entry for this display; fall back to any cookie, since a
	// single-seat machine usually has exactly one.
	for _, e := range entries {
		if e.display == display && e.name == "MIT-MAGIC-COOKIE-1" {
			return e.name, e.data, nil
		}
	}
	for _, e := range entries {
		if e.name == "MIT-MAGIC-COOKIE-1" {
			return e.name, e.data, nil
		}
	}
	return "", nil, fmt.Errorf("glow: no MIT-MAGIC-COOKIE-1 in %s", path)
}

// dial connects to the X server and completes the setup handshake.
func dial() (*conn, error) {
	display := os.Getenv("DISPLAY")
	if display == "" {
		return nil, fmt.Errorf("glow: DISPLAY is not set")
	}
	// ":0.0" — the screen suffix is not part of the socket name.
	num := strings.TrimPrefix(display, ":")
	if i := strings.Index(num, "."); i >= 0 {
		num = num[:i]
	}
	if _, err := strconv.Atoi(num); err != nil {
		return nil, fmt.Errorf("glow: cannot parse DISPLAY %q", display)
	}

	sock, err := net.Dial("unix", "/tmp/.X11-unix/X"+num)
	if err != nil {
		return nil, fmt.Errorf("glow: connect to X: %w", err)
	}

	authName, authData, err := readAuth(num)
	if err != nil {
		sock.Close()
		return nil, err
	}

	var req []byte
	req = append(req, 'l', 0)                       // little-endian
	req = binary.LittleEndian.AppendUint16(req, 11) // major
	req = binary.LittleEndian.AppendUint16(req, 0)  // minor
	req = binary.LittleEndian.AppendUint16(req, uint16(len(authName)))
	req = binary.LittleEndian.AppendUint16(req, uint16(len(authData)))
	req = append(req, 0, 0)
	req = append(req, pad4([]byte(authName))...)
	req = append(req, pad4(authData)...)

	if _, err := sock.Write(req); err != nil {
		sock.Close()
		return nil, err
	}

	header := make([]byte, 8)
	if _, err := readFull(sock, header); err != nil {
		sock.Close()
		return nil, err
	}
	if header[0] != 1 {
		reasonLen := int(header[1])
		reason := make([]byte, reasonLen)
		_, _ = readFull(sock, reason)
		sock.Close()
		return nil, fmt.Errorf("glow: X server refused the connection: %s",
			strings.TrimSpace(string(reason)))
	}

	bodyLen := int(binary.LittleEndian.Uint16(header[6:8])) * 4
	body := make([]byte, bodyLen)
	if _, err := readFull(sock, body); err != nil {
		sock.Close()
		return nil, err
	}

	c := &conn{sock: sock}
	if err := c.parseSetup(body); err != nil {
		sock.Close()
		return nil, err
	}
	return c, nil
}

// parseSetup extracts the resource id range and the first screen's properties.
func (c *conn) parseSetup(b []byte) error {
	if len(b) < 32 {
		return fmt.Errorf("glow: setup reply too short")
	}
	c.idBase = binary.LittleEndian.Uint32(b[4:8])
	c.idMask = binary.LittleEndian.Uint32(b[8:12])
	vendorLen := int(binary.LittleEndian.Uint16(b[16:18]))
	numFormats := int(b[21])

	// Skip the fixed header, the vendor string and the pixmap formats to reach
	// the screen list.
	offset := 32 + (vendorLen+3)/4*4 + numFormats*8
	if offset+40 > len(b) {
		return fmt.Errorf("glow: setup reply truncated before the screen list")
	}

	c.root = binary.LittleEndian.Uint32(b[offset : offset+4])
	c.screenWidth = binary.LittleEndian.Uint16(b[offset+20 : offset+22])
	c.screenHeight = binary.LittleEndian.Uint16(b[offset+22 : offset+24])
	c.visual = binary.LittleEndian.Uint32(b[offset+32 : offset+36])
	c.depth = b[offset+38]

	c.nextID = 0
	return nil
}

// newID allocates a resource identifier.
func (c *conn) newID() uint32 {
	id := c.idBase | (c.nextID & c.idMask)
	c.nextID++
	return id
}

// send writes a request.
func (c *conn) send(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	_, err := c.sock.Write(b)
	if err != nil {
		c.dead.Store(true)
	}
	return err
}

// queryShape asks the server for the SHAPE extension's opcode.
func (c *conn) queryShape() error {
	name := "SHAPE"
	var req []byte
	req = append(req, opQueryExtension, 0)
	req = binary.LittleEndian.AppendUint16(req, uint16(2+(len(name)+3)/4))
	req = binary.LittleEndian.AppendUint16(req, uint16(len(name)))
	req = append(req, 0, 0)
	req = append(req, pad4([]byte(name))...)

	c.mu.Lock()
	c.seq++
	if _, err := c.sock.Write(req); err != nil {
		c.mu.Unlock()
		return err
	}
	reply := make([]byte, 32)
	_, err := readFull(c.sock, reply)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if reply[0] != 1 || reply[8] == 0 {
		return fmt.Errorf("glow: the X server has no SHAPE extension, so the " +
			"indicator would intercept clicks along the top of the screen")
	}
	c.shapeOpcode = reply[9]
	return nil
}

// createWindow makes an override-redirect window.
//
// Override-redirect tells the window manager to keep its hands off: no frame,
// no title bar, no placement policy, no appearing in the task switcher. It is
// what makes this a strip of colour rather than an application window.
func (c *conn) createWindow(x, y int16, w, h uint16, background uint32) uint32 {
	wid := c.newID()

	var req []byte
	req = append(req, opCreateWindow, c.depth)
	req = binary.LittleEndian.AppendUint16(req, 8+2) // fixed length plus 2 values
	req = binary.LittleEndian.AppendUint32(req, wid)
	req = binary.LittleEndian.AppendUint32(req, c.root)
	req = binary.LittleEndian.AppendUint16(req, uint16(x))
	req = binary.LittleEndian.AppendUint16(req, uint16(y))
	req = binary.LittleEndian.AppendUint16(req, w)
	req = binary.LittleEndian.AppendUint16(req, h)
	req = binary.LittleEndian.AppendUint16(req, 0) // border width
	req = binary.LittleEndian.AppendUint16(req, 1) // InputOutput
	req = binary.LittleEndian.AppendUint32(req, c.visual)
	req = binary.LittleEndian.AppendUint32(req, cwBackPixel|cwOverrideRedirect)
	req = binary.LittleEndian.AppendUint32(req, background)
	req = binary.LittleEndian.AppendUint32(req, 1) // override-redirect: true

	_ = c.send(req)
	return wid
}

// makeClickThrough empties the window's input region.
//
// The visible region is untouched, so the strip still draws; only pointer
// events stop landing on it. Without this the top of the screen becomes a dead
// band that swallows clicks meant for whatever is underneath.
func (c *conn) makeClickThrough(wid uint32) error {
	var req []byte
	req = append(req, c.shapeOpcode, shapeRectangles)
	req = binary.LittleEndian.AppendUint16(req, 4) // no rectangles follow
	req = append(req, shapeSet, shapeKindInput, shapeUnsorted, 0)
	req = binary.LittleEndian.AppendUint32(req, wid)
	req = binary.LittleEndian.AppendUint16(req, 0) // x offset
	req = binary.LittleEndian.AppendUint16(req, 0) // y offset
	return c.send(req)
}

// createGC makes a graphics context for drawing.
func (c *conn) createGC(drawable uint32) uint32 {
	gc := c.newID()
	var req []byte
	req = append(req, opCreateGC, 0)
	req = binary.LittleEndian.AppendUint16(req, 4+1)
	req = binary.LittleEndian.AppendUint32(req, gc)
	req = binary.LittleEndian.AppendUint32(req, drawable)
	req = binary.LittleEndian.AppendUint32(req, gcForeground)
	req = binary.LittleEndian.AppendUint32(req, 0)
	_ = c.send(req)
	return gc
}

// setForeground changes the drawing colour.
func (c *conn) setForeground(gc uint32, colour uint32) {
	var req []byte
	req = append(req, opChangeGC, 0)
	req = binary.LittleEndian.AppendUint16(req, 3+1)
	req = binary.LittleEndian.AppendUint32(req, gc)
	req = binary.LittleEndian.AppendUint32(req, gcForeground)
	req = binary.LittleEndian.AppendUint32(req, colour)
	_ = c.send(req)
}

// fillRect paints a rectangle in the current foreground colour.
func (c *conn) fillRect(drawable, gc uint32, x, y int16, w, h uint16) {
	var req []byte
	req = append(req, opPolyFillRectangle, 0)
	req = binary.LittleEndian.AppendUint16(req, 3+2)
	req = binary.LittleEndian.AppendUint32(req, drawable)
	req = binary.LittleEndian.AppendUint32(req, gc)
	req = binary.LittleEndian.AppendUint16(req, uint16(x))
	req = binary.LittleEndian.AppendUint16(req, uint16(y))
	req = binary.LittleEndian.AppendUint16(req, w)
	req = binary.LittleEndian.AppendUint16(req, h)
	_ = c.send(req)
}

func (c *conn) mapWindow(wid uint32) {
	var req []byte
	req = append(req, opMapWindow, 0)
	req = binary.LittleEndian.AppendUint16(req, 2)
	req = binary.LittleEndian.AppendUint32(req, wid)
	_ = c.send(req)
}

func (c *conn) unmapWindow(wid uint32) {
	var req []byte
	req = append(req, opUnmapWindow, 0)
	req = binary.LittleEndian.AppendUint16(req, 2)
	req = binary.LittleEndian.AppendUint32(req, wid)
	_ = c.send(req)
}

// raise puts the window back on top.
//
// Override-redirect keeps the window manager away but does not pin the stacking
// order, so a newly mapped window can cover the strip. Raising periodically is
// cheaper and more reliable than trying to detect when it happened.
func (c *conn) raise(wid uint32) {
	const stackModeAbove = 0
	var req []byte
	req = append(req, opConfigureWindow, 0)
	req = binary.LittleEndian.AppendUint16(req, 3+1)
	req = binary.LittleEndian.AppendUint32(req, wid)
	req = binary.LittleEndian.AppendUint16(req, 0x0040) // stack-mode
	req = append(req, 0, 0)
	req = binary.LittleEndian.AppendUint32(req, stackModeAbove)
	_ = c.send(req)
}

func (c *conn) close() error { return c.sock.Close() }

// pad4 pads a byte slice to a four-byte boundary, as the protocol requires.
func pad4(b []byte) []byte {
	if n := len(b) % 4; n != 0 {
		return append(b, make([]byte, 4-n)...)
	}
	return b
}

func readFull(c net.Conn, b []byte) (int, error) {
	read := 0
	for read < len(b) {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := c.Read(b[read:])
		if err != nil {
			return read, err
		}
		read += n
	}
	_ = c.SetReadDeadline(time.Time{})
	return read, nil
}
