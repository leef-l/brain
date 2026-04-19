// Package kernel — BrainPool 接口与 ProcessBrainPool 实现。
//
// BrainPool 将进程池管理从 Orchestrator 中抽离，使多个 Run 可以共享一个
// 全局 pool。这是 v3 架构过渡的第一步。
//
// 设计参考：35-BrainPool实现设计.md
package kernel

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
)

// BrainPool 定义了进程池的核心接口。
// 多个 Run / Orchestrator 可以共享同一个 BrainPool 实例。
type BrainPool interface {
	// GetBrain 返回一个正在运行的 sidecar agent，如果不存在则启动。
	// 内部包含 nil-placeholder 并发控制、健康检查和崩溃重启逻辑。
	GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error)

	// Status 返回所有已知 brain 的状态快照。
	Status() map[agent.Kind]BrainStatus

	// AutoStart 启动所有标记为 AutoStart=true 的 brain sidecar。
	AutoStart(ctx context.Context)

	// Shutdown 优雅关停所有运行中的 sidecar。
	Shutdown(ctx context.Context) error
}

type brainPoolCatalog interface {
	AvailableKinds() []agent.Kind
}

type brainPoolRegistrationCatalog interface {
	Registrations() []BrainRegistration
}

// ProcessBrainPool 基于进程的 BrainPool 实现。
// 逻辑从 orchestrator.go 的 getOrStartSidecar / waitForSidecar / removeSidecar /
// AutoStartBrains / ListBrains / Shutdown 中提取而来。
type ProcessBrainPool struct {
	runner      BrainRunner
	binResolver func(kind agent.Kind) (string, error)

	// available 记录哪些 sidecar 二进制文件在磁盘上可用。
	available map[agent.Kind]bool

	// registrations 存储配置驱动的 brain 注册信息。
	registrations map[agent.Kind]*BrainRegistration

	mu     sync.Mutex
	active map[agent.Kind]agent.Agent // 运行中的 sidecar 池（可复用）
}

// NewProcessBrainPool 创建一个基于进程的 BrainPool。
// 它会探测文件系统上的可用 sidecar 二进制文件，记录哪些 kind 可以被启动。
func NewProcessBrainPool(runner BrainRunner, binResolver func(agent.Kind) (string, error), cfg OrchestratorConfig) *ProcessBrainPool {
	p := &ProcessBrainPool{
		runner:        runner,
		binResolver:   binResolver,
		available:     make(map[agent.Kind]bool),
		registrations: make(map[agent.Kind]*BrainRegistration),
		active:        make(map[agent.Kind]agent.Agent),
	}

	if len(cfg.Brains) > 0 {
		// 配置驱动：只探测已配置的 brain。
		for i := range cfg.Brains {
			reg := &cfg.Brains[i]
			p.registrations[reg.Kind] = reg
			p.probeRegistration(reg, binResolver)
		}
	} else {
		// 向后兼容：探测所有内置 kind。
		for _, kind := range agent.BuiltinKinds() {
			p.probeBinResolver(kind, binResolver)
		}
	}

	return p
}

// probeRegistration 检查一个配置 brain 的二进制文件是否存在。
func (p *ProcessBrainPool) probeRegistration(reg *BrainRegistration, binResolver func(agent.Kind) (string, error)) {
	// 1. 显式二进制路径优先。
	if reg.Binary != "" {
		if _, err := os.Stat(reg.Binary); err == nil {
			p.available[reg.Kind] = true
			return
		}
	}
	// 2. 回退到 binResolver。
	p.probeBinResolver(reg.Kind, binResolver)
}

// probeBinResolver 通过 bin resolver 探测单个 kind。
func (p *ProcessBrainPool) probeBinResolver(kind agent.Kind, binResolver func(agent.Kind) (string, error)) {
	if binResolver == nil {
		return
	}
	path, err := binResolver(kind)
	if err != nil {
		return
	}
	if _, statErr := os.Stat(path); statErr == nil {
		p.available[kind] = true
	}
}

// GetBrain 返回已运行的 sidecar，或启动一个新的。
// 如果缓存的 sidecar 已死亡，则移除并重新启动。
// 使用 nil-placeholder 防止并发启动重复的 sidecar。
func (p *ProcessBrainPool) GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	p.mu.Lock()

	// 复用已有的存活 sidecar（跳过 nil placeholder）。
	if ag, ok := p.active[kind]; ok && ag != nil {
		if p.isAlive(ag) {
			p.mu.Unlock()
			return ag, nil
		}
		// sidecar 已死亡——从池中移除。
		fmt.Fprintf(os.Stderr, "pool: %s sidecar dead, removing from pool\n", kind)
		delete(p.active, kind)
		// 后台清理。
		go func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ag.Shutdown(shutCtx)
			p.runner.Stop(shutCtx, kind)
		}()
	}

	// 标记为"正在启动"：插入 nil，让并发调用者不会重复启动。
	if ag, starting := p.active[kind]; starting && ag == nil {
		// 另一个 goroutine 正在启动——在锁外等待。
		p.mu.Unlock()

		resolved, resolvedAg, resolvedErr := p.waitForSidecar(ctx, kind)
		if resolvedErr != nil {
			return nil, resolvedErr
		}
		if resolved {
			return resolvedAg, nil
		}

		// 启动者失败并移除了 placeholder——我们自己来启动。
		p.mu.Lock()
		// 再次检查：也许另一个等待者已经启动了。
		if ag, ok := p.active[kind]; ok && ag != nil {
			p.mu.Unlock()
			return ag, nil
		}
		// 继续走启动流程。
	}
	p.active[kind] = nil // placeholder: "正在启动"
	p.mu.Unlock()

	// 在锁外启动新的 sidecar。
	desc := agent.Descriptor{
		Kind:      kind,
		LLMAccess: agent.LLMAccessProxied,
	}

	ag, err := p.startWithRegistration(ctx, kind, desc)
	if err != nil {
		// 启动失败——移除 placeholder。
		p.mu.Lock()
		if p.active[kind] == nil {
			delete(p.active, kind)
		}
		p.mu.Unlock()
		return nil, err
	}

	// 缓存 sidecar 以供复用（替换 nil placeholder）。
	p.mu.Lock()
	p.active[kind] = ag
	p.mu.Unlock()

	return ag, nil
}

func (p *ProcessBrainPool) startWithRegistration(ctx context.Context, kind agent.Kind, desc agent.Descriptor) (agent.Agent, error) {
	reg := p.registrations[kind]
	if reg == nil {
		return p.runner.Start(ctx, kind, desc)
	}

	switch runner := p.runner.(type) {
	case *ProcessRunner:
		cfgRunner := &ProcessRunner{
			BinPath:         runner.BinPath,
			BinResolver:     runner.BinResolver,
			Env:             append([]string(nil), runner.Env...),
			Args:            append([]string(nil), runner.Args...),
			InitTimeout:     runner.InitTimeout,
			ShutdownTimeout: runner.ShutdownTimeout,
			ProtocolVersion: runner.ProtocolVersion,
			KernelVersion:   runner.KernelVersion,
		}
		if reg.Binary != "" {
			if _, err := os.Stat(reg.Binary); err == nil {
				cfgRunner.BinPath = reg.Binary
			}
		}
		if cfgRunner.BinPath == "" {
			cfgRunner.BinResolver = runner.BinResolver
		}
		if len(reg.Args) > 0 {
			cfgRunner.Args = append([]string(nil), reg.Args...)
		}
		cfgRunner.Env = mergeProcessEnv(runner.Env, reg.Env)
		return cfgRunner.Start(ctx, kind, desc)
	default:
		return p.runner.Start(ctx, kind, desc)
	}
}

func mergeProcessEnv(base []string, extra []string) []string {
	if len(extra) == 0 {
		if base == nil {
			return nil
		}
		return append([]string(nil), base...)
	}

	out := append([]string(nil), base...)
	if out == nil {
		out = os.Environ()
	}
	index := make(map[string]int, len(out))
	for i, entry := range out {
		key, ok := envKey(entry)
		if !ok {
			continue
		}
		index[key] = i
	}
	for _, entry := range extra {
		key, ok := envKey(entry)
		if !ok {
			continue
		}
		if i, exists := index[key]; exists {
			out[i] = entry
			continue
		}
		index[key] = len(out)
		out = append(out, entry)
	}
	return out
}

func envKey(entry string) (string, bool) {
	key, _, ok := strings.Cut(entry, "=")
	return key, ok && key != ""
}

// Status 返回所有可用 brain 的状态快照。
func (p *ProcessBrainPool) Status() map[agent.Kind]BrainStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	result := make(map[agent.Kind]BrainStatus, len(p.available))
	for kind := range p.available {
		status := BrainStatus{
			Kind:    kind,
			Running: p.isAlive(p.active[kind]),
		}
		if p.binResolver != nil {
			if path, err := p.binResolver(kind); err == nil {
				status.Binary = path
			}
		}
		result[kind] = status
	}
	return result
}

// AutoStart 启动所有标记为 AutoStart=true 的 brain sidecar。
// 错误会打印到 stderr 但不会阻止其他 brain 启动。
func (p *ProcessBrainPool) AutoStart(ctx context.Context) {
	for kind, reg := range p.registrations {
		if !reg.AutoStart {
			continue
		}
		if !p.available[kind] {
			fmt.Fprintf(os.Stderr, "pool: auto-start %s skipped (no binary)\n", kind)
			continue
		}
		fmt.Fprintf(os.Stderr, "pool: auto-starting %s sidecar...\n", kind)
		if _, err := p.GetBrain(ctx, kind); err != nil {
			fmt.Fprintf(os.Stderr, "pool: auto-start %s failed: %v\n", kind, err)
		} else {
			fmt.Fprintf(os.Stderr, "pool: auto-start %s ok\n", kind)
		}
	}
}

// Shutdown 优雅关停所有运行中的 sidecar。
func (p *ProcessBrainPool) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	agents := make(map[agent.Kind]agent.Agent, len(p.active))
	for k, v := range p.active {
		agents[k] = v
	}
	p.active = make(map[agent.Kind]agent.Agent)
	p.mu.Unlock()

	var lastErr error
	for kind, ag := range agents {
		if ag != nil {
			ag.Shutdown(ctx)
		}
		if err := p.runner.Stop(ctx, kind); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// Available 报告给定 kind 是否有可用的 sidecar 二进制文件。
func (p *ProcessBrainPool) Available(kind agent.Kind) bool {
	return p.available[kind]
}

// AvailableKinds 返回所有有 sidecar 二进制文件的 kind 列表。
func (p *ProcessBrainPool) AvailableKinds() []agent.Kind {
	kinds := make([]agent.Kind, 0, len(p.available))
	for k := range p.available {
		kinds = append(kinds, k)
	}
	return kinds
}

// Registrations returns the configured brain registrations known to the pool.
func (p *ProcessBrainPool) Registrations() []BrainRegistration {
	out := make([]BrainRegistration, 0, len(p.registrations))
	for _, reg := range p.registrations {
		if reg == nil {
			continue
		}
		out = append(out, *reg)
	}
	return out
}

// RemoveBrain 从活跃池中移除一个 sidecar 并尝试清理。
func (p *ProcessBrainPool) RemoveBrain(kind agent.Kind) {
	p.mu.Lock()
	ag, ok := p.active[kind]
	if ok {
		delete(p.active, kind)
	}
	p.mu.Unlock()

	if ok && ag != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ag.Shutdown(shutCtx)
		p.runner.Stop(shutCtx, kind)
	}
}

// waitForSidecar 等待另一个 goroutine 完成 sidecar 启动。
// 返回 (resolved, agent, err)：
//   - resolved=true, agent!=nil: 其他 goroutine 成功启动了 sidecar
//   - resolved=false: 启动者失败并移除了 placeholder，调用者应自行启动
//   - err!=nil: context 被取消或超时
func (p *ProcessBrainPool) waitForSidecar(ctx context.Context, kind agent.Kind) (bool, agent.Agent, error) {
	for attempts := 0; attempts < 50; attempts++ { // 50 x 100ms = 最多 5s
		time.Sleep(100 * time.Millisecond)
		if ctx.Err() != nil {
			return false, nil, ctx.Err()
		}
		p.mu.Lock()
		ag, ok := p.active[kind]
		p.mu.Unlock()
		if !ok {
			// 启动者失败并移除了 placeholder。
			return false, nil, nil
		}
		if ag != nil {
			// 启动者成功。
			return true, ag, nil
		}
		// 仍然是 nil placeholder——继续等待。
	}
	// 等待超时。
	return false, nil, fmt.Errorf("timeout waiting for %s sidecar to start", kind)
}

// isAlive 检查缓存的 sidecar agent 是否仍然存活。
func (p *ProcessBrainPool) isAlive(ag agent.Agent) bool {
	if ag == nil {
		return false
	}
	// 检查基于进程的 agent。
	type processChecker interface {
		ProcessExited() bool
	}
	if pc, ok := ag.(processChecker); ok {
		return !pc.ProcessExited()
	}
	// 其他类型的 agent，默认存活。
	return true
}
