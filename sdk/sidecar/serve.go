package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leef-l/brain/sdk/diaglog"
	"github.com/leef-l/brain/sdk/protocol"
)

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// ListenAndServe 将 BrainHandler 包装为 HTTP+WebSocket 网络服务。
// 提供三个端点：
//   - POST /rpc       — 单次 JSON-RPC 请求/响应（无反向调用）
//   - GET  /ws        — WebSocket 双向 RPC（支持反向调用，如 llm.complete）
//   - GET  /health    — 健康检查
//
// addr 格式如 ":8080" 或 "0.0.0.0:8080"。
func ListenAndServe(addr string, handler BrainHandler) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	mux := http.NewServeMux()

	// GET /health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"kind":    string(handler.Kind()),
			"version": handler.Version(),
		})
	})

	// POST /rpc — 单次 JSON-RPC（HTTP 模式，无反向 RPC）
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleHTTPRPC(w, r, handler)
	})

	// GET /ws — WebSocket 双向 RPC（完整模式，支持反向调用）
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWSSession(ctx, w, r, handler)
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	fmt.Fprintf(os.Stderr, "brain-%s sidecar v%s listening on %s (network mode)\n",
		handler.Kind(), handler.Version(), addr)
	diaglog.Logf("rpc", "kind=%s listen addr=%s mode=network", handler.Kind(), addr)

	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		server.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			diaglog.Logf("rpc", "kind=%s listen failed err=%v", handler.Kind(), err)
			return fmt.Errorf("sidecar serve: %w", err)
		}
		return nil
	}
}

// handleHTTPRPC 处理单次 JSON-RPC POST 请求。
func handleHTTPRPC(w http.ResponseWriter, r *http.Request, handler BrainHandler) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024))
	if err != nil {
		diaglog.Logf("rpc", "kind=%s http read_body failed err=%v", handler.Kind(), err)
		writeRPCError(w, nil, -32700, "parse error")
		return
	}

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      interface{}     `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		diaglog.Logf("rpc", "kind=%s http parse_request failed err=%v", handler.Kind(), err)
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	diaglog.Logf("rpc", "kind=%s http method=%s", handler.Kind(), req.Method)

	// 内置方法
	var result interface{}
	var methodErr error

	switch req.Method {
	case "health.ping":
		result = map[string]string{"status": "ok"}
	case "initialize":
		result = map[string]interface{}{
			"protocolVersion": "1.0",
			"capabilities":    map[string]interface{}{"tools": true},
			"serverInfo": map[string]interface{}{
				"name":    fmt.Sprintf("brain-%s", handler.Kind()),
				"version": handler.Version(),
			},
			"brainDescriptor": map[string]interface{}{
				"kind":            string(handler.Kind()),
				"version":         handler.Version(),
				"llm_access":      "proxied",
				"supported_tools": handler.Tools(),
			},
		}
	case "tools/list":
		specs := toolSpecsForHandler(handler)
		result = map[string]interface{}{"tools": specs}
	default:
		result, methodErr = handler.HandleMethod(r.Context(), req.Method, req.Params)
		if methodErr == ErrMethodNotFound {
			diaglog.Logf("rpc", "kind=%s http method=%s not_found", handler.Kind(), req.Method)
			writeRPCError(w, req.ID, -32601, "method not found")
			return
		}
	}

	if methodErr != nil {
		diaglog.Logf("rpc", "kind=%s http method=%s failed err=%v", handler.Kind(), req.Method, methodErr)
		writeRPCError(w, req.ID, -32000, methodErr.Error())
		return
	}
	diaglog.Logf("rpc", "kind=%s http method=%s ok", handler.Kind(), req.Method)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id interface{}, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	})
}

// handleWSSession 处理 WebSocket 双向 RPC 会话。
// 为每个连接创建独立的 BidirRPC 会话，支持反向调用。
func handleWSSession(ctx context.Context, w http.ResponseWriter, r *http.Request, handler BrainHandler) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		diaglog.Logf("rpc", "kind=%s ws upgrade failed remote=%s err=%v", handler.Kind(), r.RemoteAddr, err)
		return
	}
	defer conn.Close()

	// 创建帧读写器（复用 kernel 包的实现会造成循环依赖，这里内联简化版）
	reader := &sidecarWSReader{conn: conn}
	writer := newSidecarWSWriter(conn)
	defer writer.Close()
	rpc := protocol.NewBidirRPC(protocol.RoleSidecar, reader, writer)

	// 注册内置方法
	registerBuiltinMethods(rpc, handler)

	// 注入反向 RPC 能力
	if rich, ok := handler.(RichBrainHandler); ok {
		rich.SetKernelCaller(&rpcKernelCaller{rpc: rpc})
	}

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	go func() {
		<-rpc.Done()
		sessionCancel()
	}()

	if err := rpc.Start(sessionCtx); err != nil {
		diaglog.Logf("rpc", "kind=%s ws start failed remote=%s err=%v", handler.Kind(), r.RemoteAddr, err)
		return
	}

	fmt.Fprintf(os.Stderr, "brain-%s: ws session established from %s\n", handler.Kind(), r.RemoteAddr)
	diaglog.Logf("rpc", "kind=%s ws session established remote=%s", handler.Kind(), r.RemoteAddr)

	<-sessionCtx.Done()
	diaglog.Logf("rpc", "kind=%s ws session closed remote=%s", handler.Kind(), r.RemoteAddr)
	rpc.Close()
}

// registerBuiltinMethods 注册 sidecar 的标准 RPC 方法。
func registerBuiltinMethods(rpc protocol.BidirRPC, handler BrainHandler) {
	rpc.Handle("initialize", func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return map[string]interface{}{
			"protocolVersion": "1.0",
			"capabilities":    map[string]interface{}{"tools": true},
			"serverInfo": map[string]interface{}{
				"name":    fmt.Sprintf("brain-%s", handler.Kind()),
				"version": handler.Version(),
			},
			"brainDescriptor": map[string]interface{}{
				"kind":            string(handler.Kind()),
				"version":         handler.Version(),
				"llm_access":      "proxied",
				"supported_tools": handler.Tools(),
			},
		}, nil
	})

	rpc.Handle("notifications/initialized", func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		return nil, nil
	})

	rpc.Handle("tools/list", func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		specs := toolSpecsForHandler(handler)
		return map[string]interface{}{"tools": specs}, nil
	})

	rpc.Handle("tools/call", func(ctx context.Context, params json.RawMessage) (interface{}, error) {
		return handler.HandleMethod(ctx, "tools/call", params)
	})

	for _, method := range []string{"brain/execute", "brain/plan", "brain/verify", "brain/metrics", "brain/learn"} {
		m := method
		rpc.Handle(m, func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			return handler.HandleMethod(ctx, m, params)
		})
	}
}

// sidecarWSReader/Writer 是轻量的 WebSocket FrameReader/FrameWriter 实现。
// 与 kernel 包的 WSFrameReader/Writer 功能相同，但在 sidecar 包内定义以避免循环依赖。

type wsFrameEnvelope struct {
	ContentLength int             `json:"content_length"`
	ContentType   string          `json:"content_type,omitempty"`
	Body          json.RawMessage `json:"body"`
}

type sidecarWSReader struct {
	conn *websocket.Conn
}

func (r *sidecarWSReader) ReadFrame(ctx context.Context) (*protocol.Frame, error) {
	r.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	msgType, data, err := r.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("ws read: %w", err)
	}
	if msgType != websocket.TextMessage {
		return nil, fmt.Errorf("ws: expected text message, got %d", msgType)
	}
	var env wsFrameEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("ws: unmarshal frame: %w", err)
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

type sidecarWSWriter struct {
	conn *websocket.Conn
	ch   chan writeJob
	done chan struct{}
}

type writeJob struct {
	data  []byte
	errCh chan error
}

func newSidecarWSWriter(conn *websocket.Conn) *sidecarWSWriter {
	w := &sidecarWSWriter{
		conn: conn,
		ch:   make(chan writeJob, 64),
		done: make(chan struct{}),
	}
	go w.loop()
	return w
}

func (w *sidecarWSWriter) loop() {
	defer close(w.done)
	for job := range w.ch {
		w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		job.errCh <- w.conn.WriteMessage(websocket.TextMessage, job.data)
	}
}

func (w *sidecarWSWriter) WriteFrame(ctx context.Context, frame *protocol.Frame) error {
	env := wsFrameEnvelope{
		ContentLength: frame.ContentLength,
		ContentType:   frame.ContentType,
		Body:          frame.Body,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	errCh := make(chan error, 1)
	select {
	case w.ch <- writeJob{data: data, errCh: errCh}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *sidecarWSWriter) Close() error {
	close(w.ch)
	<-w.done
	return nil
}
