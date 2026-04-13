package cdp

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- rpcError ---

func TestRPCError_Error(t *testing.T) {
	e := &rpcError{Code: -32601, Message: "not found"}
	got := e.Error()
	if !strings.Contains(got, "-32601") || !strings.Contains(got, "not found") {
		t.Errorf("Error() = %q", got)
	}
}

func TestRPCError_WithData(t *testing.T) {
	e := &rpcError{Code: -32601, Message: "not found", Data: "extra"}
	got := e.Error()
	if !strings.Contains(got, "extra") {
		t.Errorf("Error() missing data: %q", got)
	}
}

// --- rpcRequest JSON ---

func TestRPCRequest_Marshal(t *testing.T) {
	req := rpcRequest{
		ID:     1,
		Method: "Page.navigate",
		Params: json.RawMessage(`{"url":"http://example.com"}`),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	json.Unmarshal(data, &got)
	if got["method"] != "Page.navigate" {
		t.Errorf("method = %v", got["method"])
	}
	if got["id"].(float64) != 1 {
		t.Errorf("id = %v", got["id"])
	}
}

func TestRPCRequest_NoSession(t *testing.T) {
	req := rpcRequest{ID: 1, Method: "Target.getTargets"}
	data, _ := json.Marshal(req)
	if strings.Contains(string(data), "sessionId") {
		t.Error("sessionId should be omitted when empty")
	}
}

func TestRPCRequest_WithSession(t *testing.T) {
	req := rpcRequest{ID: 1, Method: "Page.navigate", SessionID: "session-123"}
	data, _ := json.Marshal(req)
	if !strings.Contains(string(data), "session-123") {
		t.Error("sessionId should be included")
	}
}

// --- rpcResponse JSON ---

func TestRPCResponse_Unmarshal(t *testing.T) {
	raw := `{"id":1,"result":{"frameId":"abc"}}`
	var resp rpcResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 1 {
		t.Errorf("ID = %d", resp.ID)
	}
	if resp.Error != nil {
		t.Error("Error should be nil")
	}
}

func TestRPCResponse_Error(t *testing.T) {
	raw := `{"id":1,"error":{"code":-32601,"message":"not found"}}`
	var resp rpcResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Errorf("Code = %d", resp.Error.Code)
	}
}

// --- rpcEvent JSON ---

func TestRPCEvent_Unmarshal(t *testing.T) {
	raw := `{"method":"Page.loadEventFired","params":{"timestamp":123.456}}`
	var ev rpcEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Method != "Page.loadEventFired" {
		t.Errorf("Method = %q", ev.Method)
	}
}

// --- Integration test with mock WebSocket server ---

// mockWSServer creates a local TCP server that does WebSocket handshake
// and responds to CDP commands.
func mockWSServer(t *testing.T, handler func(conn net.Conn, br *bufio.Reader)) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)

		// Perform WebSocket handshake
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		key := req.Header.Get("Sec-WebSocket-Key")
		magic := "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
		h := sha1.New()
		h.Write([]byte(key + magic))
		accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

		resp := fmt.Sprintf(
			"HTTP/1.1 101 Switching Protocols\r\n"+
				"Upgrade: websocket\r\n"+
				"Connection: Upgrade\r\n"+
				"Sec-WebSocket-Accept: %s\r\n"+
				"\r\n", accept)
		conn.Write([]byte(resp))

		handler(conn, br)
	}()

	addr := ln.Addr().String()
	return "ws://" + addr + "/devtools/browser/test", func() {
		ln.Close()
		wg.Wait()
	}
}

// writeUnmaskedTextFrame writes an unmasked WebSocket text frame (server→client).
func writeUnmaskedTextFrame(conn net.Conn, data []byte) error {
	frame := []byte{0x81} // FIN=1, opcode=1 (text)
	payloadLen := len(data)
	if payloadLen <= 125 {
		frame = append(frame, byte(payloadLen)) // no mask bit
	} else if payloadLen <= 65535 {
		frame = append(frame, 126)
		frame = append(frame, byte(payloadLen>>8), byte(payloadLen))
	} else {
		frame = append(frame, 127)
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, uint64(payloadLen))
		frame = append(frame, lenBytes...)
	}
	frame = append(frame, data...)
	_, err := conn.Write(frame)
	return err
}

// readMaskedTextFrame reads a client→server masked text frame.
func readMaskedTextFrame(br *bufio.Reader) ([]byte, error) {
	header := make([]byte, 2)
	if _, err := br.Read(header); err != nil {
		return nil, err
	}
	length := uint64(header[1] & 0x7F)
	if length == 126 {
		ext := make([]byte, 2)
		br.Read(ext)
		length = uint64(binary.BigEndian.Uint16(ext))
	} else if length == 127 {
		ext := make([]byte, 8)
		br.Read(ext)
		length = binary.BigEndian.Uint64(ext)
	}
	maskKey := make([]byte, 4)
	br.Read(maskKey)
	data := make([]byte, length)
	br.Read(data)
	for i := range data {
		data[i] ^= maskKey[i%4]
	}
	return data, nil
}

func TestDialAndCall(t *testing.T) {
	url, cleanup := mockWSServer(t, func(conn net.Conn, br *bufio.Reader) {
		// Read client request
		data, err := readMaskedTextFrame(br)
		if err != nil {
			return
		}
		var req rpcRequest
		json.Unmarshal(data, &req)

		// Send response
		resp, _ := json.Marshal(rpcResponse{
			ID:     req.ID,
			Result: json.RawMessage(`{"targetId":"T1"}`),
		})
		writeUnmaskedTextFrame(conn, resp)

		// Wait for close or timeout
		time.Sleep(100 * time.Millisecond)
	})
	defer cleanup()

	client, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result struct {
		TargetID string `json:"targetId"`
	}
	if err := client.Call(ctx, "Target.getTargets", nil, &result); err != nil {
		t.Fatal(err)
	}
	if result.TargetID != "T1" {
		t.Errorf("targetId = %q", result.TargetID)
	}
}

func TestDialAndCallWithError(t *testing.T) {
	url, cleanup := mockWSServer(t, func(conn net.Conn, br *bufio.Reader) {
		data, err := readMaskedTextFrame(br)
		if err != nil {
			return
		}
		var req rpcRequest
		json.Unmarshal(data, &req)

		resp, _ := json.Marshal(rpcResponse{
			ID:    req.ID,
			Error: &rpcError{Code: -32601, Message: "method not found"},
		})
		writeUnmaskedTextFrame(conn, resp)
		time.Sleep(100 * time.Millisecond)
	})
	defer cleanup()

	client, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = client.Call(ctx, "Nonexistent.method", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "method not found") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestDialAndEvent(t *testing.T) {
	url, cleanup := mockWSServer(t, func(conn net.Conn, br *bufio.Reader) {
		// Send an event
		ev, _ := json.Marshal(map[string]interface{}{
			"method": "Page.loadEventFired",
			"params": map[string]float64{"timestamp": 123.456},
		})
		writeUnmaskedTextFrame(conn, ev)
		time.Sleep(200 * time.Millisecond)
	})
	defer cleanup()

	client, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	received := make(chan json.RawMessage, 1)
	client.On("Page.loadEventFired", func(params json.RawMessage) {
		received <- params
	})

	select {
	case params := <-received:
		var p struct {
			Timestamp float64 `json:"timestamp"`
		}
		json.Unmarshal(params, &p)
		if p.Timestamp != 123.456 {
			t.Errorf("timestamp = %f", p.Timestamp)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestCallTimeout(t *testing.T) {
	url, cleanup := mockWSServer(t, func(conn net.Conn, br *bufio.Reader) {
		// Read request but never respond
		readMaskedTextFrame(br)
		time.Sleep(2 * time.Second)
	})
	defer cleanup()

	client, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err = client.Call(ctx, "Page.navigate", map[string]string{"url": "http://example.com"}, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestCallSession(t *testing.T) {
	url, cleanup := mockWSServer(t, func(conn net.Conn, br *bufio.Reader) {
		data, err := readMaskedTextFrame(br)
		if err != nil {
			return
		}
		var req rpcRequest
		json.Unmarshal(data, &req)

		if req.SessionID != "session-123" {
			return
		}

		resp, _ := json.Marshal(rpcResponse{
			ID:     req.ID,
			Result: json.RawMessage(`{"result":"ok"}`),
		})
		writeUnmaskedTextFrame(conn, resp)
		time.Sleep(100 * time.Millisecond)
	})
	defer cleanup()

	client, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result map[string]string
	err = client.CallSession(ctx, "session-123", "Page.navigate", nil, &result)
	if err != nil {
		t.Fatal(err)
	}
	if result["result"] != "ok" {
		t.Errorf("result = %v", result)
	}
}

func TestDialInvalidURL(t *testing.T) {
	_, err := Dial("ws://127.0.0.1:1/nonexistent")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestClientClose(t *testing.T) {
	url, cleanup := mockWSServer(t, func(conn net.Conn, br *bufio.Reader) {
		time.Sleep(2 * time.Second)
	})
	defer cleanup()

	client, err := Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	// Double close should not panic
	client.Close()
}
