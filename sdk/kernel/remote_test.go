package kernel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// TestRemotePool_Creation 测试 RemoteBrainPool 基本创建。
func TestRemotePool_Creation(t *testing.T) {
	configs := []*RemoteBrainConfig{
		{
			Kind:     agent.KindCode,
			Endpoint: "http://localhost:9999",
			APIKey:   "test-token",
			Timeout:  5 * time.Second,
		},
		{
			Kind:     agent.KindBrowser,
			Endpoint: "http://localhost:9998",
		},
	}

	pool, err := NewRemoteBrainPool(configs)
	if err != nil {
		t.Fatalf("NewRemoteBrainPool error: %v", err)
	}
	if pool == nil {
		t.Fatal("NewRemoteBrainPool returned nil")
	}

	// 验证配置正确存储。
	if len(pool.configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(pool.configs))
	}

	// 验证默认 timeout 被设置。
	browserCfg := pool.configs[agent.KindBrowser]
	if browserCfg.Timeout != 30*time.Second {
		t.Errorf("expected default timeout 30s, got %v", browserCfg.Timeout)
	}

	// 验证 Status 返回所有配置的 brain。
	status := pool.Status()
	if len(status) != 2 {
		t.Fatalf("expected 2 status entries, got %d", len(status))
	}
	for _, s := range status {
		if s.Running {
			t.Errorf("brain %s should not be running before GetBrain", s.Kind)
		}
	}
}

// TestRemotePool_GetBrain_WithServer 使用 httptest 模拟远程 brain 服务器。
func TestRemotePool_GetBrain_WithServer(t *testing.T) {
	// 模拟远程 brain 的 JSON-RPC 服务器。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证认证头。
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// 验证 Content-Type。
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}

		// 解析 JSON-RPC 请求。
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// 根据方法返回不同响应。
		switch req.Method {
		case "health.ping":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"status":"ok"}`),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		case "brain.execute":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{"output":"hello from remote brain"}`),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)

		default:
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &jsonRPCError{
					Code:    -32601,
					Message: "method not found",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer srv.Close()

	configs := []*RemoteBrainConfig{
		{
			Kind:     agent.KindCode,
			Endpoint: srv.URL,
			APIKey:   "test-secret",
			Timeout:  5 * time.Second,
		},
	}

	pool, err := NewRemoteBrainPool(configs)
	if err != nil {
		t.Fatalf("NewRemoteBrainPool error: %v", err)
	}
	ctx := context.Background()

	// 获取远程 brain。
	ag, err := pool.GetBrain(ctx, agent.KindCode)
	if err != nil {
		t.Fatalf("GetBrain failed: %v", err)
	}

	if ag.Kind() != agent.KindCode {
		t.Errorf("expected kind %s, got %s", agent.KindCode, ag.Kind())
	}

	// 验证 Status 现在显示 running。
	status := pool.Status()
	codeStatus, ok := status[agent.KindCode]
	if !ok {
		t.Fatal("code brain not in status")
	}
	if !codeStatus.Running {
		t.Error("code brain should be running after GetBrain")
	}

	// 验证 Ready 成功。
	if err := ag.Ready(ctx); err != nil {
		t.Errorf("Ready failed: %v", err)
	}

	// 重复获取应返回同一 agent。
	ag2, err := pool.GetBrain(ctx, agent.KindCode)
	if err != nil {
		t.Fatalf("second GetBrain failed: %v", err)
	}
	if ag != ag2 {
		t.Error("expected same agent on repeated GetBrain")
	}
}

// TestRemotePool_GetBrain_UnknownKind 测试获取未配置的 brain kind。
func TestRemotePool_GetBrain_UnknownKind(t *testing.T) {
	pool, poolErr := NewRemoteBrainPool(nil)
	if poolErr != nil {
		t.Fatalf("NewRemoteBrainPool error: %v", poolErr)
	}
	_, err := pool.GetBrain(context.Background(), agent.KindFault)
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

// TestRemotePool_Shutdown 测试关闭所有连接。
func TestRemotePool_Shutdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  json.RawMessage(`{"status":"ok"}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	pool, poolErr := NewRemoteBrainPool([]*RemoteBrainConfig{
		{Kind: agent.KindCode, Endpoint: srv.URL, Timeout: 5 * time.Second},
	})
	if poolErr != nil {
		t.Fatalf("NewRemoteBrainPool error: %v", poolErr)
	}

	ctx := context.Background()

	// 先建立连接。
	_, err := pool.GetBrain(ctx, agent.KindCode)
	if err != nil {
		t.Fatalf("GetBrain failed: %v", err)
	}

	// 关闭。
	if err := pool.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// 验证 pool 已清空。
	status := pool.Status()
	for _, s := range status {
		if s.Running {
			t.Errorf("brain %s should not be running after shutdown", s.Kind)
		}
	}
}

// TestRemoteConn_Call 测试 remoteConn 的 JSON-RPC 调用。
func TestRemoteConn_Call(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"answer":42}`),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	conn := newRemoteConn(&RemoteBrainConfig{
		Endpoint: srv.URL,
		Timeout:  5 * time.Second,
	})

	result, err := conn.Call(context.Background(), "test.method", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	var parsed struct {
		Answer int `json:"answer"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed.Answer != 42 {
		t.Errorf("expected answer 42, got %d", parsed.Answer)
	}
}

// TestRemoteConn_CallError 测试 JSON-RPC 错误响应。
func TestRemoteConn_CallError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    -32600,
				Message: "invalid request",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	conn := newRemoteConn(&RemoteBrainConfig{
		Endpoint: srv.URL,
		Timeout:  5 * time.Second,
	})

	_, err := conn.Call(context.Background(), "bad.method", nil)
	if err == nil {
		t.Fatal("expected error for JSON-RPC error response")
	}

	rpcErr, ok := err.(*jsonRPCError)
	if !ok {
		t.Fatalf("expected *jsonRPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != -32600 {
		t.Errorf("expected error code -32600, got %d", rpcErr.Code)
	}
}

// TestRemoteConn_AuthHeader 验证 Bearer token 正确传递。
func TestRemoteConn_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&req)
		resp := jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{}`),
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	conn := newRemoteConn(&RemoteBrainConfig{
		Endpoint: srv.URL,
		APIKey:   "my-secret-token",
		Timeout:  5 * time.Second,
	})

	_, err := conn.Call(context.Background(), "test", nil)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	expected := "Bearer my-secret-token"
	if gotAuth != expected {
		t.Errorf("expected auth header %q, got %q", expected, gotAuth)
	}
}
