// Package cdp provides a zero-dependency Chrome DevTools Protocol client.
//
// It communicates with Chrome/Chromium via WebSocket using the CDP wire
// protocol (JSON-RPC over WebSocket frames). The WebSocket implementation
// is built on Go's standard library only — no third-party dependencies.
package cdp

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// wsConn is a minimal WebSocket client built on the standard library.
// It supports text frames (opcode 1) which is all CDP needs.
type wsConn struct {
	conn   net.Conn
	br     *bufio.Reader
	mu     sync.Mutex // protects writes
	closed bool
}

// wsDial performs a WebSocket upgrade handshake and returns a wsConn.
func wsDial(rawURL string) (*wsConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("cdp: parse url: %w", err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		switch u.Scheme {
		case "ws", "http":
			host += ":80"
		case "wss", "https":
			host += ":443"
		default:
			host += ":80"
		}
	}

	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, fmt.Errorf("cdp: dial %s: %w", host, err)
	}

	// Generate WebSocket key.
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cdp: generate key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	// Send HTTP upgrade request.
	path := u.RequestURI()
	reqStr := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"\r\n",
		path, u.Host, key,
	)
	if _, err := conn.Write([]byte(reqStr)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cdp: write upgrade: %w", err)
	}

	// Read HTTP response.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("cdp: read upgrade response: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 101 {
		conn.Close()
		return nil, fmt.Errorf("cdp: upgrade failed: status %d", resp.StatusCode)
	}

	// Verify Sec-WebSocket-Accept.
	magic := "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.New()
	h.Write([]byte(key + magic))
	expectedAccept := base64.StdEncoding.EncodeToString(h.Sum(nil))
	if resp.Header.Get("Sec-WebSocket-Accept") != expectedAccept {
		conn.Close()
		return nil, errors.New("cdp: invalid Sec-WebSocket-Accept")
	}

	return &wsConn{conn: conn, br: br}, nil
}

// WriteText sends a text frame (opcode 0x1) with masking (client→server).
func (ws *wsConn) WriteText(data []byte) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.closed {
		return errors.New("cdp: connection closed")
	}

	// Frame header: FIN=1, opcode=1 (text), MASK=1.
	frame := make([]byte, 0, 14+len(data))

	// Byte 0: FIN + opcode.
	frame = append(frame, 0x81) // 1000_0001

	// Byte 1+: MASK bit + payload length.
	payloadLen := len(data)
	switch {
	case payloadLen <= 125:
		frame = append(frame, byte(payloadLen)|0x80) // MASK=1
	case payloadLen <= 65535:
		frame = append(frame, 126|0x80)
		frame = append(frame, byte(payloadLen>>8), byte(payloadLen))
	default:
		frame = append(frame, 127|0x80)
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, uint64(payloadLen))
		frame = append(frame, lenBytes...)
	}

	// Masking key (4 random bytes).
	maskKey := make([]byte, 4)
	rand.Read(maskKey)
	frame = append(frame, maskKey...)

	// Masked payload.
	masked := make([]byte, payloadLen)
	for i := 0; i < payloadLen; i++ {
		masked[i] = data[i] ^ maskKey[i%4]
	}
	frame = append(frame, masked...)

	_, err := ws.conn.Write(frame)
	return err
}

// ReadMessage reads a complete WebSocket message (handles continuation frames).
// Returns the opcode of the first frame and the assembled payload.
func (ws *wsConn) ReadMessage() (opcode byte, payload []byte, err error) {
	if ws.closed {
		return 0, nil, errors.New("cdp: connection closed")
	}

	var result []byte
	var firstOpcode byte
	first := true

	for {
		// Read frame header (2 bytes minimum).
		header := make([]byte, 2)
		if _, err := io.ReadFull(ws.br, header); err != nil {
			return 0, nil, fmt.Errorf("cdp: read frame header: %w", err)
		}

		fin := header[0]&0x80 != 0
		op := header[0] & 0x0F
		masked := header[1]&0x80 != 0
		length := uint64(header[1] & 0x7F)

		if first {
			firstOpcode = op
			first = false
		}

		// Handle close frame.
		if op == 0x08 {
			ws.closed = true
			return op, nil, errors.New("cdp: received close frame")
		}

		// Handle ping — respond with pong.
		if op == 0x09 {
			// Read ping payload.
			if length == 126 {
				ext := make([]byte, 2)
				io.ReadFull(ws.br, ext)
				length = uint64(binary.BigEndian.Uint16(ext))
			} else if length == 127 {
				ext := make([]byte, 8)
				io.ReadFull(ws.br, ext)
				length = binary.BigEndian.Uint64(ext)
			}
			pingData := make([]byte, length)
			if masked {
				mask := make([]byte, 4)
				io.ReadFull(ws.br, mask)
				io.ReadFull(ws.br, pingData)
				for i := range pingData {
					pingData[i] ^= mask[i%4]
				}
			} else {
				io.ReadFull(ws.br, pingData)
			}
			// Send pong.
			ws.writePong(pingData)
			first = true // reset, ping is not a data frame
			continue
		}

		// Skip pong frames.
		if op == 0x0A {
			if length == 126 {
				ext := make([]byte, 2)
				io.ReadFull(ws.br, ext)
				length = uint64(binary.BigEndian.Uint16(ext))
			} else if length == 127 {
				ext := make([]byte, 8)
				io.ReadFull(ws.br, ext)
				length = binary.BigEndian.Uint64(ext)
			}
			discard := make([]byte, length)
			if masked {
				mask := make([]byte, 4)
				io.ReadFull(ws.br, mask)
			}
			io.ReadFull(ws.br, discard)
			first = true
			continue
		}

		// Extended payload length.
		if length == 126 {
			ext := make([]byte, 2)
			if _, err := io.ReadFull(ws.br, ext); err != nil {
				return 0, nil, fmt.Errorf("cdp: read ext len: %w", err)
			}
			length = uint64(binary.BigEndian.Uint16(ext))
		} else if length == 127 {
			ext := make([]byte, 8)
			if _, err := io.ReadFull(ws.br, ext); err != nil {
				return 0, nil, fmt.Errorf("cdp: read ext len: %w", err)
			}
			length = binary.BigEndian.Uint64(ext)
		}

		// Masking key (server→client usually unmasked, but handle it).
		var maskKey []byte
		if masked {
			maskKey = make([]byte, 4)
			if _, err := io.ReadFull(ws.br, maskKey); err != nil {
				return 0, nil, fmt.Errorf("cdp: read mask: %w", err)
			}
		}

		// Payload.
		data := make([]byte, length)
		if _, err := io.ReadFull(ws.br, data); err != nil {
			return 0, nil, fmt.Errorf("cdp: read payload: %w", err)
		}

		// Unmask if needed.
		if masked {
			for i := range data {
				data[i] ^= maskKey[i%4]
			}
		}

		result = append(result, data...)

		if fin {
			break
		}
	}

	return firstOpcode, result, nil
}

// writePong sends a pong frame.
func (ws *wsConn) writePong(data []byte) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	frame := []byte{0x8A} // FIN=1, opcode=0xA (pong)
	payloadLen := len(data)
	if payloadLen <= 125 {
		frame = append(frame, byte(payloadLen)|0x80)
	} else {
		// Pong data is typically tiny, but handle it.
		frame = append(frame, 126|0x80)
		frame = append(frame, byte(payloadLen>>8), byte(payloadLen))
	}

	maskKey := make([]byte, 4)
	rand.Read(maskKey)
	frame = append(frame, maskKey...)

	masked := make([]byte, payloadLen)
	for i := 0; i < payloadLen; i++ {
		masked[i] = data[i] ^ maskKey[i%4]
	}
	frame = append(frame, masked...)

	ws.conn.Write(frame)
}

// Close sends a close frame and closes the underlying connection.
func (ws *wsConn) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.closed {
		return nil
	}
	ws.closed = true

	// Send close frame: FIN=1, opcode=0x8, masked, no payload.
	frame := []byte{0x88, 0x80}
	maskKey := make([]byte, 4)
	rand.Read(maskKey)
	frame = append(frame, maskKey...)
	ws.conn.Write(frame)

	return ws.conn.Close()
}
