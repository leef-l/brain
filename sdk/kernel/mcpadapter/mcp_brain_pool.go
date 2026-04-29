// Package mcpadapter — MCPBrainPool 将 MCP 服务器作为 brain 接入 BrainPool 接口。
//
// MCPBrainPool 是 kernel.BrainPool 的 MCP 适配实现。它为每个 agent.Kind 维护一个
// mcpadapter.Adapter 实例，在 GetBrain 时自动连接并初始化 MCP 服务器。
//
// 设计参考：35-v3重构路径与开发计划.md Step 12
package mcpadapter

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/tool"
)

// MCPServerConfig 描述一个 MCP 服务器的启动配置。
// 每个 agent.Kind 对应一个 MCP 服务器。
type MCPServerConfig struct {
	// Kind 标识这个 MCP brain 的角色。
	Kind agent.Kind

	// BinPath 是 MCP 服务器可执行文件的路径。
	BinPath string

	// Args 是传给 MCP 服务器的额外命令行参数。
	Args []string

	// Env 是 MCP 服务器进程的环境变量。nil 继承当前进程环境。
	Env []string

	// ToolPrefix 添加到每个 MCP 工具名称前，防止命名冲突。
	// 例如: "mcp.code." → 工具 "search" 变为 "mcp.code.search"
	ToolPrefix string

	// AutoStart 控制此 MCP 服务器是否在 AutoStart 调用时自动启动。
	AutoStart bool
}

// BrainStatus 描述单个 MCP brain 的状态。
// 这是 MCPBrainPool 内部使用的类型，与 kernel.BrainStatus 对应。
type BrainStatus struct {
	Kind    agent.Kind
	Running bool
	Binary  string
}

// MCPBrainPool 实现 kernel.BrainPool 接口，将 MCP 服务器作为 brain 接入。
//
// 内部维护 map[agent.Kind]*Adapter，在 GetBrain 时按需连接 MCP 服务器。
type MCPBrainPool struct {
	mu sync.Mutex

	// configs 存储每个 kind 的 MCP 服务器配置。
	configs map[agent.Kind]*MCPServerConfig

	// adapters 存储已启动的 MCP adapter。
	adapters map[agent.Kind]*Adapter

	// agents 存储已包装的 MCPAgent（实现 agent.Agent 接口）。
	agents map[agent.Kind]*MCPAgent
}

// NewMCPBrainPool 创建一个 MCPBrainPool。
// configs 是 MCP 服务器配置列表，每个 config 对应一个 agent.Kind。
func NewMCPBrainPool(configs []MCPServerConfig) *MCPBrainPool {
	p := &MCPBrainPool{
		configs:  make(map[agent.Kind]*MCPServerConfig, len(configs)),
		adapters: make(map[agent.Kind]*Adapter),
		agents:   make(map[agent.Kind]*MCPAgent),
	}
	for i := range configs {
		cfg := &configs[i]
		p.configs[cfg.Kind] = cfg
	}
	return p
}

// GetBrain 返回一个正在运行的 MCP brain agent。
// 如果 adapter 尚未连接，先 Connect + Init（Start + DiscoverTools）。
func (p *MCPBrainPool) GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 1. 已有存活的 agent，直接复用。
	if ag, ok := p.agents[kind]; ok {
		return ag, nil
	}

	// 2. 查找配置。
	cfg, ok := p.configs[kind]
	if !ok {
		return nil, fmt.Errorf("mcpadapter: no MCP server configured for kind %q", kind)
	}

	// 3. 创建并启动 adapter。
	adapter := &Adapter{
		BinPath:    cfg.BinPath,
		Args:       cfg.Args,
		Env:        cfg.Env,
		ToolPrefix: cfg.ToolPrefix,
	}

	if err := adapter.Start(ctx); err != nil {
		return nil, fmt.Errorf("mcpadapter: start MCP server for %q: %w", kind, err)
	}

	// 4. 发现工具（可选，不影响 agent 返回）。
	schemas, err := adapter.DiscoverTools(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcpadapter: tool discovery for %q failed: %v\n", kind, err)
		// adapter 已启动但工具发现失败，仍然可用
	}

	// 5. 包装为 MCPAgent。
	ag := &MCPAgent{
		kind:    kind,
		adapter: adapter,
		desc: agent.Descriptor{
			Kind:      kind,
			Version:   "mcp-1.0",
			LLMAccess: agent.LLMAccessProxied,
		},
		schemas: schemas,
	}

	p.adapters[kind] = adapter
	p.agents[kind] = ag

	return ag, nil
}

// Status 返回所有已注册 MCP brain 的状态快照。
// 返回类型为 map[agent.Kind]BrainStatus，其中 BrainStatus 是本包内定义的类型。
// 调用方（kernel 包）通过接口约定将其转换为 kernel.BrainStatus。
func (p *MCPBrainPool) Status() map[agent.Kind]BrainStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	result := make(map[agent.Kind]BrainStatus, len(p.configs))
	for kind, cfg := range p.configs {
		status := BrainStatus{
			Kind:   kind,
			Binary: cfg.BinPath,
		}
		if _, ok := p.agents[kind]; ok {
			status.Running = true
		}
		result[kind] = status
	}
	return result
}

// AutoStart 启动所有标记为 AutoStart=true 的 MCP 服务器。
// 错误会打印到 stderr 但不会阻止其他 brain 启动。
func (p *MCPBrainPool) AutoStart(ctx context.Context) {
	// 先收集需要启动的 kind，避免持锁调用 GetBrain。
	p.mu.Lock()
	var toStart []agent.Kind
	for kind, cfg := range p.configs {
		if cfg.AutoStart {
			if _, ok := p.agents[kind]; !ok {
				toStart = append(toStart, kind)
			}
		}
	}
	p.mu.Unlock()

	for _, kind := range toStart {
		fmt.Fprintf(os.Stderr, "mcpadapter: auto-starting MCP brain %q...\n", kind)
		if _, err := p.GetBrain(ctx, kind); err != nil {
			fmt.Fprintf(os.Stderr, "mcpadapter: auto-start %q failed: %v\n", kind, err)
		} else {
			fmt.Fprintf(os.Stderr, "mcpadapter: auto-start %q ok\n", kind)
		}
	}
}

// Shutdown 优雅关闭所有 MCP 连接。
func (p *MCPBrainPool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	adapters := make(map[agent.Kind]*Adapter, len(p.adapters))
	for k, v := range p.adapters {
		adapters[k] = v
	}
	// 清空活跃池。
	p.agents = make(map[agent.Kind]*MCPAgent)
	p.adapters = make(map[agent.Kind]*Adapter)
	p.mu.Unlock()

	var lastErr error
	for kind, adapter := range adapters {
		if err := adapter.Stop(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "mcpadapter: shutdown %q failed: %v\n", kind, err)
			lastErr = err
		}
	}
	return lastErr
}

// RegisterAllTools discovers and registers all MCP brain tools into the given
// registry. It starts each adapter lazily if not already running.
func (p *MCPBrainPool) RegisterAllTools(ctx context.Context, registry tool.Registry) error {
	for _, kind := range p.AvailableKinds() {
		ag, err := p.GetBrain(ctx, kind)
		if err != nil {
			return fmt.Errorf("mcpadapter: get brain %q: %w", kind, err)
		}
		mcpAgent, ok := ag.(*MCPAgent)
		if !ok {
			continue
		}
		if _, err := mcpAgent.Adapter().RegisterTools(ctx, registry); err != nil {
			return fmt.Errorf("mcpadapter: register tools %q: %w", kind, err)
		}
	}
	return nil
}

// AvailableKinds 返回所有已配置的 MCP brain kind 列表。
func (p *MCPBrainPool) AvailableKinds() []agent.Kind {
	p.mu.Lock()
	defer p.mu.Unlock()
	kinds := make([]agent.Kind, 0, len(p.configs))
	for k := range p.configs {
		kinds = append(kinds, k)
	}
	return kinds
}

// ---------------------------------------------------------------------------
// MCPAgent — 将 MCP Adapter 包装为 agent.Agent 接口
// ---------------------------------------------------------------------------

// MCPAgent 将一个 MCP Adapter 包装为 agent.Agent 接口的实现。
// MCP 服务器不是完整的 BrainAgent（缺少 BrainPlan、Agent Loop 等），
// 但可以通过此适配器暴露为 BrainPool 可管理的 agent。
type MCPAgent struct {
	kind    agent.Kind
	adapter *Adapter
	desc    agent.Descriptor
	schemas []tool.Schema
}

// Kind 返回此 MCP brain 的角色。
func (a *MCPAgent) Kind() agent.Kind {
	return a.kind
}

// Descriptor 返回此 agent 的描述符。
func (a *MCPAgent) Descriptor() agent.Descriptor {
	return a.desc
}

// Ready 对 MCP agent 来说，Start 成功即表示就绪。
func (a *MCPAgent) Ready(_ context.Context) error {
	return nil
}

// Shutdown 关闭底层 MCP adapter。
func (a *MCPAgent) Shutdown(ctx context.Context) error {
	return a.adapter.Stop(ctx)
}

// Adapter 返回底层的 MCP Adapter，供需要直接调用工具的场景使用。
func (a *MCPAgent) Adapter() *Adapter {
	return a.adapter
}

// --- 编译期接口断言 ---
var _ agent.Agent = (*MCPAgent)(nil)
