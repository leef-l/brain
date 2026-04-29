// Package kernel — BrainPool 接口与 ProcessBrainPool 实现。
//
// BrainPool 将进程池管理从 Orchestrator 中抽离，使多个 Run 可以共享一个
// 全局 pool。v3 升级后，每个 kind 可维护多个实例，由负载均衡策略自动选择。
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
	// 内部使用默认负载均衡策略选择最优实例。
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
type BrainEvent struct {
	Kind   agent.Kind
	Action string // "start" | "stop" | "restart"
	Agent  agent.Agent
	Error  error
	Time   time.Time
}

// ProcessBrainPool 管理每个 kind 的多个 brain 实例，支持负载均衡。
type ProcessBrainPool struct {
	runner      BrainRunner
	binResolver func(kind agent.Kind) (string, error)

	// available 记录哪些 sidecar 二进制文件在磁盘上可用。
	available map[agent.Kind]bool

	// registrations 存储配置驱动的 brain 注册信息。
	registrations map[agent.Kind]*BrainRegistration

	mu     sync.Mutex
	active map[agent.Kind][]*poolEntry // 运行中的 sidecar 实例池（每个 kind 可多实例）

	// starting 标记某个 kind 是否正在启动中（防止并发重复启动）。
	starting map[agent.Kind]bool

	// entrySeq 为每个 kind 生成唯一的实例序号。
	entrySeq map[agent.Kind]int

	// defaultStrategy 是 GetBrain 使用的默认负载均衡策略。
	defaultStrategy LoadBalanceStrategy

	// notifyCh 接收 sidecar 生命周期事件（可选，外部可订阅）。
	notifyCh chan<- BrainEvent

	// warmKinds 是需要在后台预热的 brain 种类。
	warmKinds []agent.Kind
}

// NewProcessBrainPool 创建一个基于进程的 BrainPool。
// 它会探测文件系统上的可用 sidecar 二进制文件，记录哪些 kind 可以被启动。
func NewProcessBrainPool(runner BrainRunner, binResolver func(agent.Kind) (string, error), cfg OrchestratorConfig) *ProcessBrainPool {
	p := &ProcessBrainPool{
		runner:          runner,
		binResolver:     binResolver,
		available:       make(map[agent.Kind]bool),
		registrations:   make(map[agent.Kind]*BrainRegistration),
		active:          make(map[agent.Kind][]*poolEntry),
		starting:        make(map[agent.Kind]bool),
		entrySeq:        make(map[agent.Kind]int),
		defaultStrategy: DefaultLoadBalanceStrategy(),
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

// SetLoadBalanceStrategy 设置默认负载均衡策略。
func (p *ProcessBrainPool) SetLoadBalanceStrategy(s LoadBalanceStrategy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s != nil {
		p.defaultStrategy = s
	}
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

// GetBrain 返回已运行的 sidecar 实例，或启动一个新的。
// 如果该 kind 已有存活实例，使用默认负载均衡策略选择最优的一个。
// 使用 nil-placeholder 防止并发启动重复的 sidecar。
func (p *ProcessBrainPool) GetBrain(ctx context.Context, kind agent.Kind) (agent.Agent, error) {
	p.mu.Lock()

	// 1. 过滤存活实例。
	alive := p.filterAliveLocked(p.active[kind])

	// 2. 如果有存活实例，使用负载均衡策略选择。
	if len(alive) > 0 {
		p.mu.Unlock()
		selected := p.defaultStrategy.Select(alive)
		if selected != nil {
			selected.Acquire()
			return selected.agent, nil
		}
	}

	// 3. 没有存活实例，需要启动。
	if p.starting[kind] {
		// 另一个 goroutine 正在启动——在锁外等待。
		p.mu.Unlock()

		resolved, resolvedAg, resolvedErr := p.waitForSidecar(ctx, kind)
		if resolvedErr != nil {
			return nil, resolvedErr
		}
		if resolved {
			return resolvedAg, nil
		}

		// 启动者失败——我们自己来启动。
		p.mu.Lock()
		// 再次检查：也许另一个等待者已经启动了。
		alive = p.filterAliveLocked(p.active[kind])
		if len(alive) > 0 {
			p.mu.Unlock()
			selected := p.defaultStrategy.Select(alive)
			if selected != nil {
				selected.Acquire()
				return selected.agent, nil
			}
		}
		// 继续走启动流程。
	}
	p.starting[kind] = true
	p.mu.Unlock()

	// 4. 在锁外启动新的 sidecar。
	desc := agent.Descriptor{
		Kind:      kind,
		LLMAccess: agent.LLMAccessProxied,
	}

	ag, err := p.startWithRegistration(ctx, kind, desc)
	if err != nil {
		// 启动失败——移除 starting 标记。
		p.mu.Lock()
		delete(p.starting, kind)
		p.mu.Unlock()
		return nil, err
	}

	// 5. 包装为 poolEntry 并加入活跃池。
	entry := p.newEntryLocked(kind, ag)
	entry.Acquire()

	p.mu.Lock()
	p.active[kind] = append(p.active[kind], entry)
	delete(p.starting, kind)
	p.mu.Unlock()

	p.notify(BrainEvent{Kind: kind, Action: "start", Agent: ag, Time: time.Now()})

	return entry.agent, nil
}

// newEntryLocked 创建一个新的 poolEntry（必须在锁外调用，但使用锁保护的序号）。
func (p *ProcessBrainPool) newEntryLocked(kind agent.Kind, ag agent.Agent) *poolEntry {
	p.mu.Lock()
	seq := p.entrySeq[kind]
	p.entrySeq[kind] = seq + 1
	p.mu.Unlock()
	return newPoolEntry(ag, fmt.Sprintf("%s-%d", kind, seq))
}

// SelectBrain 使用指定策略选择一个 brain 实例。
// 如果 strategy 为 nil，使用默认策略。
func (p *ProcessBrainPool) SelectBrain(ctx context.Context, kind agent.Kind, strategy LoadBalanceStrategy) (agent.Agent, error) {
	p.mu.Lock()
	alive := p.filterAliveLocked(p.active[kind])
	p.mu.Unlock()

	if len(alive) == 0 {
		// 没有存活实例，fallback 到 GetBrain 启动新实例。
		return p.GetBrain(ctx, kind)
	}

	if strategy == nil {
		strategy = p.defaultStrategy
	}
	selected := strategy.Select(alive)
	if selected == nil {
		return p.GetBrain(ctx, kind)
	}
	selected.Acquire()
	return selected.agent, nil
}

// ScaleBrain 将指定 kind 的实例数扩缩到 targetCount。
// 如果当前存活实例数 >= targetCount，不做任何操作。
func (p *ProcessBrainPool) ScaleBrain(ctx context.Context, kind agent.Kind, targetCount int) error {
	if targetCount <= 0 {
		return fmt.Errorf("targetCount must be > 0")
	}

	p.mu.Lock()
	alive := p.filterAliveLocked(p.active[kind])
	current := len(alive)
	p.mu.Unlock()

	if current >= targetCount {
		return nil
	}

	desc := agent.Descriptor{
		Kind:      kind,
		LLMAccess: agent.LLMAccessProxied,
	}

	for i := current; i < targetCount; i++ {
		ag, err := p.startWithRegistration(ctx, kind, desc)
		if err != nil {
			return fmt.Errorf("scale %s instance %d: %w", kind, i, err)
		}
		entry := p.newEntryLocked(kind, ag)
		p.mu.Lock()
		p.active[kind] = append(p.active[kind], entry)
		p.mu.Unlock()
		p.notify(BrainEvent{Kind: kind, Action: "start", Agent: ag, Time: time.Now()})
	}
	return nil
}

// InstanceCount 返回指定 kind 的存活实例数。
func (p *ProcessBrainPool) InstanceCount(kind agent.Kind) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.filterAliveLocked(p.active[kind]))
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
		status := p.buildStatusLocked(kind)
		result[kind] = status
	}
	return result
}

// BrainDetail 返回单个 brain 的详细状态。
func (p *ProcessBrainPool) BrainDetail(kind agent.Kind) (BrainStatus, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.available[kind] {
		return BrainStatus{}, false
	}
	return p.buildStatusLocked(kind), true
}

func (p *ProcessBrainPool) buildStatusLocked(kind agent.Kind) BrainStatus {
	entries := p.active[kind]
	aliveCount := len(p.filterAliveLocked(entries))

	status := BrainStatus{
		Kind:      kind,
		Running:   aliveCount > 0,
		Instances: aliveCount,
	}
	if p.binResolver != nil {
		if path, err := p.binResolver(kind); err == nil {
			status.Binary = path
		}
	}
	if reg := p.registrations[kind]; reg != nil {
		status.AutoStart = reg.AutoStart
		status.Model = reg.Model
		status.MinApprovalLevel = reg.MinApprovalLevel
	}
	return status
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
	allEntries := make(map[agent.Kind][]*poolEntry, len(p.active))
	for k, v := range p.active {
		allEntries[k] = append([]*poolEntry(nil), v...)
	}
	p.active = make(map[agent.Kind][]*poolEntry)
	p.starting = make(map[agent.Kind]bool)
	p.mu.Unlock()

	var lastErr error
	for kind, entries := range allEntries {
		for _, entry := range entries {
			if entry != nil && entry.agent != nil {
				entry.agent.Shutdown(ctx)
			}
		}
		if err := p.runner.Stop(ctx, kind); err != nil {
			lastErr = err
		}
		p.notify(BrainEvent{Kind: kind, Action: "stop", Time: time.Now()})
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

// RemoveBrain 从活跃池中移除指定 kind 的所有实例并尝试清理。
func (p *ProcessBrainPool) RemoveBrain(kind agent.Kind) {
	p.mu.Lock()
	entries := p.active[kind]
	delete(p.active, kind)
	delete(p.starting, kind)
	p.mu.Unlock()

	for _, entry := range entries {
		if entry != nil && entry.agent != nil {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			entry.agent.Shutdown(shutCtx)
			p.runner.Stop(shutCtx, kind)
			cancel()
		}
	}
}

// RestartBrain 停止并重新启动指定 kind 的 sidecar。
// 用于 Dashboard 管理操作和故障恢复。
func (p *ProcessBrainPool) RestartBrain(ctx context.Context, kind agent.Kind) error {
	p.RemoveBrain(kind)
	// 给 shutdown 一点时间释放端口/句柄
	time.Sleep(200 * time.Millisecond)
	_, err := p.GetBrain(ctx, kind)
	return err
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
		alive := p.filterAliveLocked(p.active[kind])
		starting := p.starting[kind]
		p.mu.Unlock()

		if !starting && len(alive) == 0 {
			// 启动者失败。
			return false, nil, nil
		}
		if len(alive) > 0 {
			// 启动者成功。
			selected := p.defaultStrategy.Select(alive)
			if selected != nil {
				selected.Acquire()
				return true, selected.agent, nil
			}
		}
		// 仍在启动中——继续等待。
	}
	// 等待超时。
	return false, nil, fmt.Errorf("timeout waiting for %s sidecar to start", kind)
}

// notify sends a lifecycle event to the optional notifyCh (non-blocking).
func (p *ProcessBrainPool) notify(ev BrainEvent) {
	if p.notifyCh == nil {
		return
	}
	select {
	case p.notifyCh <- ev:
	default:
	}
}

// SetNotifyCh sets the channel that receives BrainEvent lifecycle notifications.
func (p *ProcessBrainPool) SetNotifyCh(ch chan<- BrainEvent) {
	p.notifyCh = ch
}

// WarmPool starts background warming of the specified brain kinds.
// It pre-starts sidecars so the first real delegation is faster.
func (p *ProcessBrainPool) WarmPool(ctx context.Context, kinds ...agent.Kind) {
	p.warmKinds = kinds
	for _, kind := range kinds {
		if !p.available[kind] {
			continue
		}
		go func(k agent.Kind) {
			if _, err := p.GetBrain(ctx, k); err != nil {
				fmt.Fprintf(os.Stderr, "pool: warm %s failed: %v\n", k, err)
			} else {
				fmt.Fprintf(os.Stderr, "pool: warm %s ok\n", k)
			}
		}(kind)
	}
}

// HealthCheck 返回所有活跃 brain 的健康状态。
func (p *ProcessBrainPool) HealthCheck() map[agent.Kind]bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	result := make(map[agent.Kind]bool, len(p.active))
	for kind, entries := range p.active {
		result[kind] = len(p.filterAliveLocked(entries)) > 0
	}
	return result
}

// Drain 关闭除保留列表外的所有活跃 brain。
func (p *ProcessBrainPool) Drain(ctx context.Context, keep ...agent.Kind) error {
	keepSet := make(map[agent.Kind]bool, len(keep))
	for _, k := range keep {
		keepSet[k] = true
	}

	p.mu.Lock()
	var toDrain []agent.Kind
	for kind := range p.active {
		if !keepSet[kind] {
			toDrain = append(toDrain, kind)
		}
	}
	p.mu.Unlock()

	var errs []error
	for _, kind := range toDrain {
		if err := p.shutdownBrain(ctx, kind); err != nil {
			errs = append(errs, fmt.Errorf("drain %s: %w", kind, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("drain errors: %v", errs)
	}
	return nil
}

// Register 动态注册一个新的 brain 到 pool。
func (p *ProcessBrainPool) Register(reg BrainRegistration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registrations[reg.Kind] = &reg
	p.probeRegistration(&reg, p.binResolver)
}

// shutdownBrain 关闭单个 kind 的所有实例并清理状态。
func (p *ProcessBrainPool) shutdownBrain(ctx context.Context, kind agent.Kind) error {
	p.mu.Lock()
	entries := p.active[kind]
	delete(p.active, kind)
	delete(p.starting, kind)
	p.mu.Unlock()

	var lastErr error
	for _, entry := range entries {
		if entry != nil && entry.agent != nil {
			if shutdowner, ok := entry.agent.(interface{ Shutdown(context.Context) error }); ok {
				if err := shutdowner.Shutdown(ctx); err != nil {
					lastErr = err
				}
			}
		}
	}
	return lastErr
}

// ReleaseAgent 释放指定 agent 的负载计数。
// 在 delegateOnce / CallTool 完成后调用，让负载均衡策略知道该实例
// 的并发负载已减少。
func (p *ProcessBrainPool) ReleaseAgent(ag agent.Agent) {
	if ag == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, entries := range p.active {
		for _, e := range entries {
			if e != nil && e.agent == ag {
				e.Release()
				return
			}
		}
	}
}

// filterAliveLocked 过滤出存活的 poolEntry（必须在锁内调用）。
func (p *ProcessBrainPool) filterAliveLocked(entries []*poolEntry) []*poolEntry {
	if len(entries) == 0 {
		return nil
	}
	alive := make([]*poolEntry, 0, len(entries))
	for _, e := range entries {
		if e != nil && p.isAlive(e.agent) {
			alive = append(alive, e)
		}
	}
	return alive
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
