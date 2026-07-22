// Package browser drives Chrome through the DevTools Protocol.
//
// # Why a WebSocket implementation lives here
//
// CDP speaks JSON over a WebSocket, and the Go standard library has no
// WebSocket client. The choices were a dependency or roughly two hundred lines
// of RFC 6455 — and the client half of that specification is genuinely small:
// an HTTP upgrade handshake, then length-prefixed frames with a four-byte mask.
// Everything hard about WebSocket (server-side concurrency, extensions,
// permessage-deflate) is on the other end of the connection.
package browser

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// wsGUID is the constant the handshake accept-key is derived with, fixed by
// RFC 6455. Verifying it is what proves the peer actually spoke WebSocket
// rather than returning a coincidental 101.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Frame opcodes.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// maxFrameBytes bounds a single incoming message. A page's DOM can be large,
// but a gigabyte is a bug or an attack rather than a document.
const maxFrameBytes = 64 << 20

// wsConn is a minimal WebSocket client connection.
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex
	readMu  sync.Mutex
	closed  bool
}

// wsDial performs the upgrade handshake against a ws:// URL.
func wsDial(rawURL string, timeout time.Duration) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("websocket: bad url: %w", err)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return nil, fmt.Errorf("websocket: dial %s: %w", host, err)
	}

	var keyBytes [16]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes[:])

	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	handshake := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, u.Host, key)

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Write([]byte(handshake)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket: handshake write: %w", err)
	}

	br := bufio.NewReaderSize(conn, 64<<10)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("websocket: handshake read: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("websocket: server refused upgrade: %s", resp.Status)
	}

	// Verify the accept key. Without this a plain 101 from anything at all
	// would be treated as a live WebSocket.
	sum := sha1.Sum([]byte(key + wsGUID))
	expected := base64.StdEncoding.EncodeToString(sum[:])
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != expected {
		conn.Close()
		return nil, fmt.Errorf("websocket: accept key mismatch — peer is not a WebSocket server")
	}

	// Clear the handshake deadline; reads block until a message arrives.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, br: br}, nil
}

// writeMessage sends one text frame.
//
// Client frames must be masked, per the specification — an unmasked client
// frame is a protocol error and Chrome closes the connection.
func (c *wsConn) writeMessage(payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("websocket: connection closed")
	}

	var header []byte
	header = append(header, 0x80|opText) // FIN set, text frame

	n := len(payload)
	switch {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n <= 0xFFFF:
		header = append(header, 0x80|126)
		header = binary.BigEndian.AppendUint16(header, uint16(n))
	default:
		header = append(header, 0x80|127)
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)

	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}

	if _, err := c.conn.Write(header); err != nil {
		return fmt.Errorf("websocket: write header: %w", err)
	}
	if _, err := c.conn.Write(masked); err != nil {
		return fmt.Errorf("websocket: write payload: %w", err)
	}
	return nil
}

// readMessage returns the next complete application message, reassembling
// continuation frames and answering pings along the way.
func (c *wsConn) readMessage() ([]byte, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	var assembled []byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}

		switch opcode {
		case opPing:
			// A pong must echo the ping's payload, and is written outside the
			// read lock's concern since writes have their own.
			if err := c.writeControl(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			c.markClosed()
			return nil, io.EOF
		}

		assembled = append(assembled, payload...)
		if fin {
			return assembled, nil
		}
	}
}

// readFrame reads one frame from the wire.
func (c *wsConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return false, 0, nil, err
	}

	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}

	if length > maxFrameBytes {
		return false, 0, nil, fmt.Errorf("websocket: frame of %d bytes exceeds the limit", length)
	}

	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}

	payload = make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// writeControl sends a control frame such as pong or close.
func (c *wsConn) writeControl(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return nil
	}
	if len(payload) > 125 {
		payload = payload[:125] // control frames are bounded by the spec
	}

	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	frame := []byte{0x80 | opcode, 0x80 | byte(len(payload))}
	frame = append(frame, mask[:]...)
	for i := range payload {
		frame = append(frame, payload[i]^mask[i%4])
	}
	_, err := c.conn.Write(frame)
	return err
}

func (c *wsConn) markClosed() {
	c.writeMu.Lock()
	c.closed = true
	c.writeMu.Unlock()
}

// Close shuts the connection down politely, then hangs up.
func (c *wsConn) Close() error {
	_ = c.writeControl(opClose, []byte{0x03, 0xE8}) // 1000, normal closure
	c.markClosed()
	return c.conn.Close()
}
