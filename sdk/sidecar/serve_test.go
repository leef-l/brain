package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/protocol"
)

type serveTestHandler struct{}

func (h *serveTestHandler) Kind() agent.Kind { return "test" }
func (h *serveTestHandler) Version() string  { return "1.0.0" }
func (h *serveTestHandler) Tools() []string  { return []string{"test.echo"} }
func (h *serveTestHandler) HandleMethod(_ context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "brain/execute":
		return map[string]interface{}{
			"status":  "completed",
			"summary": "test done",
			"turns":   1,
		}, nil
	case "brain/metrics":
		return map[string]interface{}{
			"brain_kind": "test",
			"task_count": 42,
		}, nil
	case "tools/call":
		return map[string]interface{}{"result": "echo"}, nil
	}
	return nil, ErrMethodNotFound
}

func startTestServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	handler := &serveTestHandler{}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"kind":    string(handler.Kind()),
			"version": handler.Version(),
		})
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		handleHTTPRPC(w, r, handler)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWSSession(context.Background(), w, r, handler)
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	return addr, func() {
		srv.Close()
	}
}

func TestServe_HealthEndpoint(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	resp, err := http.Get(fmt.Sprintf("http://%s/health", addr))
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result struct {
		Status  string `json:"status"`
		Kind    string `json:"kind"`
		Version string `json:"version"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Status != "ok" {
		t.Errorf("status = %q, want ok", result.Status)
	}
	if result.Kind != "test" {
		t.Errorf("kind = %q, want test", result.Kind)
	}
}

func TestServe_HTTPRPC_HealthPing(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	body := `{"jsonrpc":"2.0","id":1,"method":"health.ping"}`
	resp, err := http.Post(fmt.Sprintf("http://%s/rpc", addr), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc: %v", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var rpcResp struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	json.Unmarshal(data, &rpcResp)
	if rpcResp.Result.Status != "ok" {
		t.Errorf("health.ping result = %q, want ok", rpcResp.Result.Status)
	}
}

func TestServe_HTTPRPC_BrainExecute(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	body := `{"jsonrpc":"2.0","id":2,"method":"brain/execute","params":{"instruction":"test"}}`
	resp, err := http.Post(fmt.Sprintf("http://%s/rpc", addr), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc: %v", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var rpcResp struct {
		Result struct {
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"result"`
	}
	json.Unmarshal(data, &rpcResp)
	if rpcResp.Result.Status != "completed" {
		t.Errorf("execute status = %q, want completed", rpcResp.Result.Status)
	}
}

func TestServe_HTTPRPC_MethodNotFound(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	body := `{"jsonrpc":"2.0","id":3,"method":"nonexistent"}`
	resp, err := http.Post(fmt.Sprintf("http://%s/rpc", addr), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /rpc: %v", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var rpcResp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(data, &rpcResp)
	if rpcResp.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", rpcResp.Error.Code)
	}
}

func TestServe_WebSocket_BidirRPC(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	wsURL := fmt.Sprintf("ws://%s/ws", addr)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// 创建 BidirRPC 客户端
	reader := &clientWSReader{conn: conn}
	writer := &clientWSWriter{conn: conn}
	rpc := protocol.NewBidirRPC(protocol.RoleKernel, reader, writer)
	ctx := context.Background()
	if err := rpc.Start(ctx); err != nil {
		t.Fatalf("start rpc: %v", err)
	}
	defer rpc.Close()

	// 调用 brain/execute
	var result struct {
		Status string `json:"status"`
	}
	if err := rpc.Call(ctx, "brain/execute", map[string]string{"instruction": "test"}, &result); err != nil {
		t.Fatalf("Call brain/execute: %v", err)
	}
	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}

	// 调用 brain/metrics
	var metrics struct {
		BrainKind string `json:"brain_kind"`
		TaskCount int    `json:"task_count"`
	}
	if err := rpc.Call(ctx, "brain/metrics", nil, &metrics); err != nil {
		t.Fatalf("Call brain/metrics: %v", err)
	}
	if metrics.TaskCount != 42 {
		t.Errorf("task_count = %d, want 42", metrics.TaskCount)
	}
}

// clientWSReader/Writer 用于测试的 WebSocket 帧读写器

type clientWSReader struct {
	conn *websocket.Conn
}

func (r *clientWSReader) ReadFrame(_ context.Context) (*protocol.Frame, error) {
	r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := r.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	var env wsFrameEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	ct := env.ContentType
	if ct == "" {
		ct = protocol.CanonicalContentType
	}
	return &protocol.Frame{
		ContentLength: len(env.Body),
		ContentType:   ct,
		Body:          env.Body,
	}, nil
}

type clientWSWriter struct {
	conn *websocket.Conn
}

func (w *clientWSWriter) WriteFrame(_ context.Context, frame *protocol.Frame) error {
	env := wsFrameEnvelope{
		ContentLength: frame.ContentLength,
		ContentType:   frame.ContentType,
		Body:          frame.Body,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return w.conn.WriteMessage(websocket.TextMessage, data)
}

func (w *clientWSWriter) Close() error { return nil }
