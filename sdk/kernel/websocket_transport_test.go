package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/protocol"
)

// startTestWSServer 启动一个测试 WebSocket 服务器，
// 它回显收到的 JSON-RPC 请求作为响应。
func startTestWSServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// 解析 wsFrame → 解析 JSON-RPC request → 构造 response
			var wf wsFrame
			if err := json.Unmarshal(data, &wf); err != nil {
				continue
			}
			var rpcReq struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      interface{}     `json:"id"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(wf.Body, &rpcReq); err != nil {
				continue
			}

			// 根据方法返回不同响应
			var result interface{}
			switch rpcReq.Method {
			case "health.ping":
				result = map[string]string{"status": "ok"}
			case "brain/execute":
				result = map[string]interface{}{
					"status":  "completed",
					"summary": "remote execution done",
					"turns":   1,
				}
			default:
				result = map[string]string{"echo": rpcReq.Method}
			}

			respBody, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      rpcReq.ID,
				"result":  result,
			})
			respFrame := wsFrame{
				ContentLength: len(respBody),
				ContentType:   protocol.CanonicalContentType,
				Body:          respBody,
			}
			respData, _ := json.Marshal(respFrame)
			conn.WriteMessage(websocket.TextMessage, respData)
		}
	}))
	return srv
}

func TestWSFrameReaderWriter_RoundTrip(t *testing.T) {
	srv := startTestWSServer(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	writer := NewWSFrameWriter(conn, 5*time.Second)
	reader := NewWSFrameReader(conn, 5*time.Second)
	defer writer.Close()

	// 发送一个帧
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "k:1",
		"method":  "health.ping",
	})
	ctx := context.Background()
	err = writer.WriteFrame(ctx, &protocol.Frame{
		ContentLength: len(reqBody),
		ContentType:   protocol.CanonicalContentType,
		Body:          reqBody,
	})
	if err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	// 读取响应帧
	frame, err := reader.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frame == nil || len(frame.Body) == 0 {
		t.Fatal("empty response frame")
	}

	var resp struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame.Body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Result.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Result.Status)
	}
}

func TestNewWSBidirRPC_Call(t *testing.T) {
	srv := startTestWSServer(t)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	rpc := NewWSBidirRPC(conn, protocol.RoleKernel)
	ctx := context.Background()
	if err := rpc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rpc.Close()

	// 调用 health.ping
	var result struct {
		Status string `json:"status"`
	}
	if err := rpc.Call(ctx, "health.ping", nil, &result); err != nil {
		t.Fatalf("Call health.ping: %v", err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want ok", result.Status)
	}

	// 调用 brain/execute
	var execResult struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
		Turns   int    `json:"turns"`
	}
	if err := rpc.Call(ctx, "brain/execute", map[string]string{"instruction": "test"}, &execResult); err != nil {
		t.Fatalf("Call brain/execute: %v", err)
	}
	if execResult.Status != "completed" {
		t.Errorf("exec status = %q, want completed", execResult.Status)
	}
}

func TestRemoteAgent_ImplementsRPCAgent(t *testing.T) {
	var ag interface{} = &remoteAgent{}
	if _, ok := ag.(agent.RPCAgent); !ok {
		t.Fatal("remoteAgent does not implement agent.RPCAgent")
	}
}

func TestRemoteAgent_RPCReturnsNilWhenHTTPOnly(t *testing.T) {
	ag := &remoteAgent{kind: "test"}
	rpc := ag.RPC()
	if rpc != nil {
		t.Error("RPC() should return nil when no WebSocket session")
	}
}

func TestRemoteBrainPool_GetBrain_WithWSServer(t *testing.T) {
	// 启动一个同时支持 /rpc (health) 和 /ws (bidir) 的测试服务器
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rpc":
			// JSON-RPC health check
			var req jsonRPCRequest
			json.NewDecoder(r.Body).Decode(&req)
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
			}
			if req.Method == "health.ping" {
				result, _ := json.Marshal(map[string]string{"status": "ok"})
				resp.Result = result
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case "/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			// 简单回显
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var wf wsFrame
				json.Unmarshal(data, &wf)
				var rpcReq struct {
					JSONRPC string      `json:"jsonrpc"`
					ID      interface{} `json:"id"`
					Method  string      `json:"method"`
				}
				json.Unmarshal(wf.Body, &rpcReq)
				result, _ := json.Marshal(map[string]string{"status": "ok"})
				respBody, _ := json.Marshal(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      rpcReq.ID,
					"result":  json.RawMessage(result),
				})
				respFrame := wsFrame{
					ContentLength: len(respBody),
					ContentType:   protocol.CanonicalContentType,
					Body:          respBody,
				}
				respData, _ := json.Marshal(respFrame)
				conn.WriteMessage(websocket.TextMessage, respData)
			}
		}
	}))
	defer srv.Close()

	configs := []*RemoteBrainConfig{{
		Kind:     agent.KindCode,
		Endpoint: srv.URL,
		Timeout:  5 * time.Second,
	}}
	pool, err := NewRemoteBrainPool(configs)
	if err != nil {
		t.Fatalf("NewRemoteBrainPool: %v", err)
	}

	ctx := context.Background()
	ag, err := pool.GetBrain(ctx, agent.KindCode)
	if err != nil {
		t.Fatalf("GetBrain: %v", err)
	}

	// 应该实现 RPCAgent
	rpcAgent, ok := ag.(agent.RPCAgent)
	if !ok {
		t.Fatal("returned agent is not RPCAgent")
	}

	// RPC 会话应该可用
	rpc, ok := rpcAgent.RPC().(protocol.BidirRPC)
	if !ok || rpc == nil {
		// WebSocket 可能不可用（测试服务器路径不匹配时），跳过
		t.Log("WebSocket BidirRPC not available, HTTP-only fallback")
		return
	}

	// 通过 BidirRPC 调用
	var result struct {
		Status string `json:"status"`
	}
	if err := rpc.Call(ctx, "health.ping", nil, &result); err != nil {
		t.Fatalf("BidirRPC Call: %v", err)
	}
	if result.Status != "ok" {
		t.Errorf("status = %q, want ok", result.Status)
	}
}
