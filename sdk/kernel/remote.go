// Package kernel — RemoteBrainPool 实现远程 brain 连接池。
//
// 当 brain 运行在远程机器上时，通过 HTTP JSON-RPC 协议通信，
// 而非本地 sidecar 进程的 stdio 管道。
//
// 这是 v3 架构的骨架实现，提供基本的连接管理和 JSON-RPC 调用能力。
package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// RemoteBrainConfig 描述一个远程 brain 的连接配置。
type RemoteBrainConfig struct {
	// Kind 标识 brain 角色。
	Kind agent.Kind

	// Endpoint 是远程 brain 的 HTTP 地址（如 "https://brain.example.com:8443"）。
	Endpoint string

	// APIKey 是 Bearer token 用于认证。
	APIKey string

	// Timeout 是单次 RPC 调用的超时时间。默认 30 秒。
	Timeout time.Duration

	// TLS 是否启用 TLS（当前骨架实现由 Endpoint scheme 决定）。
	TLS bool

	// AutoStart 标记是否在 AutoStart 阶段做 health check。
	AutoStart bool
}

// RemoteBrainPool 基于 HTTP JSON-RPC 的远程 BrainPool 实现。
// 它管理到远程 brain 服务的连接，通过网络协议而非 stdio 通信。
type RemoteBrainPool struct {
	configs map[agent.Kind]*RemoteBrainConfig
	conns   map[agent.Kind]*remoteConn
	agents  map[agent.Kind]*remoteAgent
	mu      sync.Mutex
}

// NewRemoteBrainPool 创建一个远程 BrainPool。
// validateEndpoint 检查 Endpoint URL 是否使用合法的 HTTP(S) scheme。
func validateEndpoint(endpoint string) error {
	if endpoint == "" {
		return fmt.Errorf("remote brain: endpoint 不能为空")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("remote brain: endpoint %q 解析失败: %w", endpoint, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("remote brain: endpoint %q 的 scheme %q 非法，仅支持 http/https", endpoint, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("remote brain: endpoint %q 缺少 host", endpoint)
	}
	return nil
}

func NewRemoteBrainPool(configs []*RemoteBrainConfig) (*RemoteBrainPool, error) {
	cfgMap := make(map[agent.Kind]*RemoteBrainConfig, len(configs))
	for _, c := range configs {
		if err := validateEndpoint(c.Endpoint); err != nil {
			return nil, fmt.Errorf("remote pool: brain %s: %w", c.Kind, err)
		}
		if c.Timeout == 0 {
			c.Timeout = 30 * time.Second
		}
		cfgMap[c.Kind] = c
	}
	return &RemoteBrainPool{
		configs: cfgMap,
		conns:   make(map[agent.Kind]*remoteConn),
		agents:  make(map[agent.Kind]*remoteAgent),
	}, nil
}

// GetBrain 返回一个连接到远程 brain 的 Agent 句柄。
// 如果连接不存在则创建新连接并做 health check。
func (p *RemoteBrainPool) GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 复用已有连接。
	if ag, ok := p.agents[kind]; ok {
		return ag, nil
	}

	cfg, ok := p.configs[kind]
	if !ok {
		return nil, fmt.Errorf("remote pool: no config for brain kind %q", kind)
	}

	// 创建连接。
	conn := newRemoteConn(cfg)

	// Health check：验证远程 brain 可达。
	if err := conn.HealthCheck(ctx); err != nil {
		return nil, fmt.Errorf("remote pool: health check failed for %s at %s: %w", kind, cfg.Endpoint, err)
	}

	ag := &remoteAgent{
		kind: kind,
		desc: agent.Descriptor{
			Kind:      kind,
			LLMAccess: agent.LLMAccessProxied,
		},
		conn: conn,
	}

	p.conns[kind] = conn
	p.agents[kind] = ag
	return ag, nil
}

// Status 返回所有配置的远程 brain 的状态快照。
func (p *RemoteBrainPool) Status() map[agent.Kind]BrainStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	result := make(map[agent.Kind]BrainStatus, len(p.configs))
	for kind, cfg := range p.configs {
		_, running := p.agents[kind]
		result[kind] = BrainStatus{
			Kind:    kind,
			Running: running,
			Binary:  cfg.Endpoint, // 远程用 endpoint 代替 binary 路径
		}
	}
	return result
}

// AutoStart 对所有标记 AutoStart 的远程 brain 执行 health check。
func (p *RemoteBrainPool) AutoStart(ctx context.Context) {
	for kind, cfg := range p.configs {
		if !cfg.AutoStart {
			continue
		}
		fmt.Printf("remote pool: health-checking %s at %s...\n", kind, cfg.Endpoint)
		if _, err := p.GetBrain(ctx, kind); err != nil {
			fmt.Printf("remote pool: %s health check failed: %v\n", kind, err)
		} else {
			fmt.Printf("remote pool: %s ok\n", kind)
		}
	}
}

// Shutdown 关闭所有远程连接。
func (p *RemoteBrainPool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	agents := make(map[agent.Kind]*remoteAgent, len(p.agents))
	for k, v := range p.agents {
		agents[k] = v
	}
	p.agents = make(map[agent.Kind]*remoteAgent)
	p.conns = make(map[agent.Kind]*remoteConn)
	p.mu.Unlock()

	var lastErr error
	for _, ag := range agents {
		if err := ag.Shutdown(ctx); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// ---------- remoteConn: HTTP JSON-RPC 传输层 ----------

// jsonRPCRequest 是标准 JSON-RPC 2.0 请求。
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse 是标准 JSON-RPC 2.0 响应。
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError JSON-RPC 错误对象。
type jsonRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *jsonRPCError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// remoteConn 封装到远程 brain 的 HTTP JSON-RPC 连接。
type remoteConn struct {
	endpoint string
	apiKey   string
	timeout  time.Duration
	client   *http.Client
	idSeq    atomic.Int64
}

// newRemoteConn 创建一个远程连接。
func newRemoteConn(cfg *RemoteBrainConfig) *remoteConn {
	return &remoteConn{
		endpoint: cfg.Endpoint,
		apiKey:   cfg.APIKey,
		timeout:  cfg.Timeout,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Call 发送 JSON-RPC 请求到远程 brain 并返回结果。
// 使用 Content-Length framed HTTP POST，与本地 sidecar 协议语义一致。
func (c *remoteConn) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.idSeq.Add(1)

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("remote call: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("remote call: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	// Content-Length 由 http 包自动设置。
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("remote call: http do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("remote call: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("remote call: decode response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return rpcResp.Result, nil
}

// HealthCheck 执行 health.ping 检查远程 brain 是否可达。
func (c *remoteConn) HealthCheck(ctx context.Context) error {
	result, err := c.Call(ctx, "health.ping", nil)
	if err != nil {
		return err
	}

	// 期望返回 {"status":"ok"} 或类似的确认。
	var status struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(result, &status); err != nil {
		// 即使解析失败，只要调用成功就算 healthy。
		return nil
	}
	return nil
}

// Close 关闭底层 HTTP client 的空闲连接。
func (c *remoteConn) Close() {
	c.client.CloseIdleConnections()
}

// ---------- remoteAgent: 实现 agent.Agent 接口 ----------

// remoteAgent 是远程 brain 的 Agent 句柄。
type remoteAgent struct {
	kind agent.Kind
	desc agent.Descriptor
	conn *remoteConn
}

// Kind 返回 brain 角色。
func (a *remoteAgent) Kind() agent.Kind {
	return a.kind
}

// Descriptor 返回 brain 描述符。
func (a *remoteAgent) Descriptor() agent.Descriptor {
	return a.desc
}

// Ready 对远程 brain 执行 health check 来确认就绪。
func (a *remoteAgent) Ready(ctx context.Context) error {
	return a.conn.HealthCheck(ctx)
}

// Shutdown 关闭远程连接（远程 brain 进程本身不受影响）。
func (a *remoteAgent) Shutdown(ctx context.Context) error {
	a.conn.Close()
	return nil
}
