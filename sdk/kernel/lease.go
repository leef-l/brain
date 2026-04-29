// lease.go — Capability Lease 机制
//
// 防止多个 brain 同时对同一资源执行互斥操作。LeaseManager 接口定义了
// 获取/释放租约的语义，MemLeaseManager 提供基于内存 map 的骨架实现。
//
// Phase A 阶段：turn_executor 尚未调用，仅提供接口和内存实现。
package kernel

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ────────────────────────── 常量/枚举 ──────────────────────────

// AccessMode 描述对资源的访问模式。
type AccessMode string

const (
	// AccessSharedRead 允许多个读取者并发持有。
	AccessSharedRead AccessMode = "shared-read"
	// AccessSharedWriteAppend 允许多个追加写者并发持有，也兼容 SharedRead。
	AccessSharedWriteAppend AccessMode = "shared-write-append"
	// AccessExclusiveWrite 独占写：不兼容任何其他模式。
	AccessExclusiveWrite AccessMode = "exclusive-write"
	// AccessExclusiveSession 独占会话：不兼容任何其他模式。
	AccessExclusiveSession AccessMode = "exclusive-session"
)

// LeaseScope 描述租约的生命周期范围。
type LeaseScope string

const (
	// ScopeTurn 租约在一个 turn 结束后自动过期。
	ScopeTurn LeaseScope = "turn"
	// ScopeTask 租约在 task 完成后过期。
	ScopeTask LeaseScope = "task"
	// ScopeDaemon 租约在 brain 退出前一直有效。
	ScopeDaemon LeaseScope = "daemon"
)

// ────────────────────────── 请求/接口 ──────────────────────────

// LeaseRequest 描述一次租约请求。
type LeaseRequest struct {
	Capability  string     // 能力名称，如 "file-write"
	ResourceKey string     // 资源标识，如 "/tmp/foo.txt"
	AccessMode  AccessMode // 访问模式
	Scope       LeaseScope // 生命周期范围
	HolderID    string     // 持有者 ID（brain ID 或 turn ID）
}

// Lease 代表一个已获取的租约。
type Lease interface {
	// ID 返回租约的唯一标识。
	ID() string
	// Release 释放租约，唤醒等待该资源的其他获取者。
	Release()
}

// LeaseManager 管理 Capability 租约的获取与释放。
type LeaseManager interface {
	// AcquireSet 原子地获取一组租约。全部成功才返回；任一失败则
	// 回滚已获取的租约并重试（最多 3 次，指数退避+jitter）。
	// ctx 取消时返回 ErrAcquireTimeout。
	AcquireSet(ctx context.Context, reqs []LeaseRequest) ([]Lease, error)

	// ReleaseAll 释放一组租约。
	ReleaseAll(leases []Lease)
}

// ────────────────────────── 错误 ──────────────────────────

// ErrAcquireTimeout 在 ctx 超时/取消时返回。
var ErrAcquireTimeout = errors.New("lease: acquire timeout or context canceled")

// ────────────────────────── 兼容性矩阵 ──────────────────────────

// compatible 判断两种访问模式是否兼容。
//
// 兼容矩阵：
//
//	                 SR    SWA   EW    ES
//	SharedRead       ✓     ✓     ✗     ✗
//	SharedWriteAppend✓     ✓     ✗     ✗
//	ExclusiveWrite   ✗     ✗     ✗     ✗
//	ExclusiveSession ✗     ✗     ✗     ✗
func compatible(a, b AccessMode) bool {
	// 排他模式不兼容任何访问
	if a == AccessExclusiveWrite || a == AccessExclusiveSession {
		return false
	}
	if b == AccessExclusiveWrite || b == AccessExclusiveSession {
		return false
	}
	// SharedRead 和 SharedWriteAppend 之间互相兼容
	return true
}

// ────────────────────────── MemLeaseManager ──────────────────────────

// memLease 是 Lease 接口的内存实现。
type memLease struct {
	id      string
	cap     string     // Capability
	resKey  string     // ResourceKey
	mode    AccessMode // 持有的访问模式
	mgr     *MemLeaseManager
	once    sync.Once // 保证 Release 只执行一次
}

func (l *memLease) ID() string { return l.id }

// Release 释放此租约，从 manager 中移除并唤醒等待者。
func (l *memLease) Release() {
	l.once.Do(func() {
		l.mgr.removeLease(l)
	})
}

// MemLeaseManager 是 LeaseManager 的内存实现，使用 sync.Mutex 保证线程安全。
type MemLeaseManager struct {
	mu      sync.Mutex
	cond    *sync.Cond
	counter atomic.Int64

	// slots: key = "Capability\x00ResourceKey" → 当前持有的租约列表
	slots map[string][]*memLease
}

// NewMemLeaseManager 创建一个内存租约管理器。
func NewMemLeaseManager() *MemLeaseManager {
	m := &MemLeaseManager{
		slots: make(map[string][]*memLease),
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// slotKey 生成 slot 的 key。
func slotKey(capability, resourceKey string) string {
	return capability + "\x00" + resourceKey
}

// nextID 生成单调递增的租约 ID。
func (m *MemLeaseManager) nextID() string {
	n := m.counter.Add(1)
	return fmt.Sprintf("lease-%d", n)
}

// AcquireSet 原子地获取一组租约。
//
// 实现逻辑：
//  1. 按 canonical order（Capability+ResourceKey 字典序）排序，防止死锁
//  2. 逐个检查兼容性并获取
//  3. 任一失败 → 释放已获取的全部 → 指数退避+jitter → 重试（最多 3 次）
//  4. 全部成功 → 返回
//  5. ctx 取消时返回 ErrAcquireTimeout
func (m *MemLeaseManager) AcquireSet(ctx context.Context, reqs []LeaseRequest) ([]Lease, error) {
	if len(reqs) == 0 {
		return nil, nil
	}

	// 按 canonical order 排序：Capability + ResourceKey 字典序
	sorted := make([]LeaseRequest, len(reqs))
	copy(sorted, reqs)
	sort.Slice(sorted, func(i, j int) bool {
		ki := sorted[i].Capability + "\x00" + sorted[i].ResourceKey
		kj := sorted[j].Capability + "\x00" + sorted[j].ResourceKey
		return ki < kj
	})

	const maxRetries = 3

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 检查 ctx 是否已取消
		if err := ctx.Err(); err != nil {
			return nil, ErrAcquireTimeout
		}

		acquired, ok := m.tryAcquireAll(sorted)
		if ok {
			// 全部获取成功，转为 []Lease 返回
			result := make([]Lease, len(acquired))
			for i, l := range acquired {
				result[i] = l
			}
			return result, nil
		}

		// 获取失败，已在 tryAcquireAll 中释放了部分获取的租约
		if attempt < maxRetries {
			// 指数退避 + jitter
			base := time.Duration(1<<uint(attempt)) * 10 * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(base / 2)))
			wait := base + jitter

			// 等待：要么超时退避结束，要么有租约释放（cond 唤醒），要么 ctx 取消
			done := make(chan struct{})
			go func() {
				m.mu.Lock()
				// 等待被唤醒或超时
				timer := time.AfterFunc(wait, func() {
					m.cond.Broadcast()
				})
				m.cond.Wait()
				timer.Stop()
				m.mu.Unlock()
				close(done)
			}()

			select {
			case <-done:
				// 继续重试
			case <-ctx.Done():
				// ctx 取消，确保 goroutine 不泄漏
				m.cond.Broadcast()
				<-done
				return nil, ErrAcquireTimeout
			}
		}
	}

	// 超出最大重试次数，视为获取超时
	return nil, ErrAcquireTimeout
}

// tryAcquireAll 尝试一次性获取全部排序后的请求。
// 成功返回所有租约和 true；失败返回 nil 和 false（已释放部分获取的租约）。
func (m *MemLeaseManager) tryAcquireAll(sorted []LeaseRequest) ([]*memLease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var acquired []*memLease

	for _, req := range sorted {
		key := slotKey(req.Capability, req.ResourceKey)
		existing := m.slots[key]

		// 检查与所有已持有租约的兼容性
		conflict := false
		for _, held := range existing {
			if !compatible(req.AccessMode, held.mode) {
				conflict = true
				break
			}
		}

		if conflict {
			// 回滚已获取的全部租约
			for _, l := range acquired {
				lkey := slotKey(l.cap, l.resKey)
				m.removeFromSlotLocked(lkey, l)
			}
			return nil, false
		}

		// 兼容，创建租约
		lease := &memLease{
			id:     m.nextID(),
			cap:    req.Capability,
			resKey: req.ResourceKey,
			mode:   req.AccessMode,
			mgr:    m,
		}
		m.slots[key] = append(m.slots[key], lease)
		acquired = append(acquired, lease)
	}

	return acquired, true
}

// removeLease 从 slots 中移除租约并唤醒等待者。
func (m *MemLeaseManager) removeLease(l *memLease) {
	m.mu.Lock()
	key := slotKey(l.cap, l.resKey)
	m.removeFromSlotLocked(key, l)
	m.mu.Unlock()

	// 唤醒所有等待者
	m.cond.Broadcast()
}

// removeFromSlotLocked 在持有锁的情况下从 slot 中移除指定租约。
func (m *MemLeaseManager) removeFromSlotLocked(key string, target *memLease) {
	leases := m.slots[key]
	for i, l := range leases {
		if l == target {
			m.slots[key] = append(leases[:i], leases[i+1:]...)
			if len(m.slots[key]) == 0 {
				delete(m.slots, key)
			}
			return
		}
	}
}

// ReleaseAll 释放一组租约。
func (m *MemLeaseManager) ReleaseAll(leases []Lease) {
	for _, l := range leases {
		l.Release()
	}
}

// LeaseSnapshot 描述单个租约的快照信息，用于 dashboard 展示。
type LeaseSnapshot struct {
	ID          string     `json:"id"`
	Capability  string     `json:"capability"`
	ResourceKey string     `json:"resource_key"`
	AccessMode  AccessMode `json:"access_mode"`
}

// Snapshot 返回当前所有活跃租约的快照列表（线程安全）。
func (m *MemLeaseManager) Snapshot() []LeaseSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []LeaseSnapshot
	for _, leases := range m.slots {
		for _, l := range leases {
			out = append(out, LeaseSnapshot{
				ID:          l.id,
				Capability:  l.cap,
				ResourceKey: l.resKey,
				AccessMode:  l.mode,
			})
		}
	}
	return out
}

// ActiveCount 返回当前活跃租约总数（线程安全）。
func (m *MemLeaseManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int
	for _, leases := range m.slots {
		count += len(leases)
	}
	return count
}

// UniqueResourceCount 返回当前被占用的独立资源数（线程安全）。
func (m *MemLeaseManager) UniqueResourceCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.slots)
}

// Query 查询特定 capability + resourceKey 的当前租约状态。
func (m *MemLeaseManager) Query(capability, resourceKey string) []LeaseSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := slotKey(capability, resourceKey)
	var out []LeaseSnapshot
	for _, l := range m.slots[key] {
		out = append(out, LeaseSnapshot{
			ID:          l.id,
			Capability:  l.cap,
			ResourceKey: l.resKey,
			AccessMode:  l.mode,
		})
	}
	return out
}

// Renew 尝试续期一组租约（内存实现中租约无固定过期时间，仅做存在性校验）。
func (m *MemLeaseManager) Renew(leases []Lease) ([]Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var renewed []Lease
	for _, lease := range leases {
		ml, ok := lease.(*memLease)
		if !ok {
			continue
		}
		key := slotKey(ml.cap, ml.resKey)
		found := false
		for _, l := range m.slots[key] {
			if l.id == ml.id {
				found = true
				break
			}
		}
		if found {
			renewed = append(renewed, ml)
		}
	}
	return renewed, nil
}

// ForceRevoke 强制撤销指定 capability + resourceKey 上的所有租约。
func (m *MemLeaseManager) ForceRevoke(capability, resourceKey string) int {
	m.mu.Lock()
	key := slotKey(capability, resourceKey)
	leases := m.slots[key]
	delete(m.slots, key)
	m.mu.Unlock()

	for _, l := range leases {
		l.once.Do(func() {
			// 不调用 removeLease 避免重复加锁
		})
	}
	m.cond.Broadcast()
	return len(leases)
}

// Close 关闭租约管理器，释放所有租约并清理状态。
func (m *MemLeaseManager) Close() {
	m.mu.Lock()
	m.slots = make(map[string][]*memLease)
	m.mu.Unlock()
	m.cond.Broadcast()
}
