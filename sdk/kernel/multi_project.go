// multi_project.go — 多项目并发管理器
//
// MACCS Wave 6 Batch 2 — 多项目并发管理。
// 支持同时执行多个项目，提供项目隔离、资源配额和并发控制。
package kernel

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// 数据结构
// ---------------------------------------------------------------------------

// ProjectSlot 项目槽位，代表一个被管理的项目执行单元。
type ProjectSlot struct {
	SlotID      string        `json:"slot_id"`
	ProjectID   string        `json:"project_id"`
	ProjectName string        `json:"project_name"`
	Status      string        `json:"status"` // idle/running/paused/completed/failed
	Priority    int           `json:"priority"`
	Quota       ResourceQuota `json:"quota"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	CompletedAt *time.Time    `json:"completed_at,omitempty"`
	FailReason  string        `json:"fail_reason,omitempty"`
}

// ResourceQuota 资源配额，限制单个项目可消耗的资源。
type ResourceQuota struct {
	MaxBrains    int           `json:"max_brains"`
	MaxTurns     int           `json:"max_turns"`
	MaxDuration  time.Duration `json:"max_duration"`
	TurnsUsed    int           `json:"turns_used"`
	BrainsActive int           `json:"brains_active"`
}

// MultiProjectConfig 多项目管理器配置。
type MultiProjectConfig struct {
	MaxConcurrent  int           `json:"max_concurrent"`
	DefaultQuota   ResourceQuota `json:"default_quota"`
	QueueSize      int           `json:"queue_size"`
	FairScheduling bool          `json:"fair_scheduling"`
	PreemptEnabled bool          `json:"preempt_enabled"`
}

// MultiProjectStats 统计信息。
type MultiProjectStats struct {
	ActiveCount    int `json:"active_count"`
	QueuedCount    int `json:"queued_count"`
	CompletedCount int `json:"completed_count"`
	FailedCount    int `json:"failed_count"`
	TotalSubmitted int `json:"total_submitted"`
}

// ---------------------------------------------------------------------------
// MultiProjectManager
// ---------------------------------------------------------------------------

// MultiProjectManager 多项目并发管理器，负责调度、隔离和配额控制。
type MultiProjectManager struct {
	mu      sync.RWMutex
	config  MultiProjectConfig
	active  map[string]*ProjectSlot
	queue   []*ProjectSlot
	history []*ProjectSlot
	nextID  int
}

// NewMultiProjectManager 创建多项目管理器。
func NewMultiProjectManager(config MultiProjectConfig) *MultiProjectManager {
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 3
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 10
	}
	return &MultiProjectManager{
		config: config,
		active: make(map[string]*ProjectSlot),
	}
}

// Submit 提交项目到管理器。
// 如果 active 数量未达上限则直接运行，否则加入等待队列。
// 当 PreemptEnabled 且新项目优先级更高时，暂停最低优先级项目腾出槽位。
func (m *MultiProjectManager) Submit(projectID, projectName string, priority int) (*ProjectSlot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.active[projectID]; ok {
		return nil, fmt.Errorf("project %s already active", projectID)
	}
	for _, s := range m.queue {
		if s.ProjectID == projectID {
			return nil, fmt.Errorf("project %s already queued", projectID)
		}
	}

	m.nextID++
	slot := &ProjectSlot{
		SlotID:      fmt.Sprintf("slot-%d", m.nextID),
		ProjectID:   projectID,
		ProjectName: projectName,
		Status:      "idle",
		Priority:    priority,
		Quota:       m.config.DefaultQuota,
	}

	// 直接运行
	if len(m.active) < m.config.MaxConcurrent {
		m.activate(slot)
		return slot, nil
	}

	// 抢占：找最低优先级的活跃项目
	if m.config.PreemptEnabled {
		var worst *ProjectSlot
		for _, s := range m.active {
			if s.Status != "running" {
				continue
			}
			if worst == nil || s.Priority > worst.Priority { // 数值越大优先级越低
				worst = s
			}
		}
		if worst != nil && priority < worst.Priority {
			worst.Status = "paused"
			m.queue = append(m.queue, worst)
			delete(m.active, worst.ProjectID)
			m.sortQueue()
			m.activate(slot)
			return slot, nil
		}
	}

	// 入队
	if len(m.queue) >= m.config.QueueSize {
		return nil, fmt.Errorf("queue full (%d/%d)", len(m.queue), m.config.QueueSize)
	}
	slot.Status = "idle"
	m.queue = append(m.queue, slot)
	m.sortQueue()
	return slot, nil
}

// Pause 暂停指定项目。
func (m *MultiProjectManager) Pause(projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	slot, ok := m.active[projectID]
	if !ok {
		return fmt.Errorf("project %s not active", projectID)
	}
	if slot.Status != "running" {
		return fmt.Errorf("project %s not running (status=%s)", projectID, slot.Status)
	}
	slot.Status = "paused"
	return nil
}

// Resume 恢复已暂停的项目。
func (m *MultiProjectManager) Resume(projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 在 active 中查找
	if slot, ok := m.active[projectID]; ok {
		if slot.Status != "paused" {
			return fmt.Errorf("project %s not paused (status=%s)", projectID, slot.Status)
		}
		slot.Status = "running"
		return nil
	}
	// 在 queue 中查找
	for i, slot := range m.queue {
		if slot.ProjectID == projectID {
			if len(m.active) >= m.config.MaxConcurrent {
				return fmt.Errorf("no available slot to resume project %s", projectID)
			}
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			m.activate(slot)
			return nil
		}
	}
	return fmt.Errorf("project %s not found", projectID)
}

// Complete 标记项目完成，自动调度队列中下一个。
func (m *MultiProjectManager) Complete(projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	slot, ok := m.active[projectID]
	if !ok {
		return fmt.Errorf("project %s not active", projectID)
	}
	now := time.Now()
	slot.Status = "completed"
	slot.CompletedAt = &now
	delete(m.active, projectID)
	m.history = append(m.history, slot)
	m.scheduleNext()
	return nil
}

// Fail 标记项目失败。
func (m *MultiProjectManager) Fail(projectID string, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	slot, ok := m.active[projectID]
	if !ok {
		return fmt.Errorf("project %s not active", projectID)
	}
	now := time.Now()
	slot.Status = "failed"
	slot.FailReason = reason
	slot.CompletedAt = &now
	delete(m.active, projectID)
	m.history = append(m.history, slot)
	m.scheduleNext()
	return nil
}

// Cancel 取消项目（从 active 或 queue 移除）。
func (m *MultiProjectManager) Cancel(projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if slot, ok := m.active[projectID]; ok {
		now := time.Now()
		slot.Status = "failed"
		slot.FailReason = "cancelled"
		slot.CompletedAt = &now
		delete(m.active, projectID)
		m.history = append(m.history, slot)
		m.scheduleNext()
		return nil
	}
	for i, slot := range m.queue {
		if slot.ProjectID == projectID {
			slot.Status = "failed"
			slot.FailReason = "cancelled"
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			m.history = append(m.history, slot)
			return nil
		}
	}
	return fmt.Errorf("project %s not found", projectID)
}

// GetSlot 获取项目槽位信息。
func (m *MultiProjectManager) GetSlot(projectID string) (*ProjectSlot, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if s, ok := m.active[projectID]; ok {
		return s, true
	}
	for _, s := range m.queue {
		if s.ProjectID == projectID {
			return s, true
		}
	}
	for _, s := range m.history {
		if s.ProjectID == projectID {
			return s, true
		}
	}
	return nil, false
}

// ActiveProjects 返回所有活跃项目的快照。
func (m *MultiProjectManager) ActiveProjects() []*ProjectSlot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*ProjectSlot, 0, len(m.active))
	for _, s := range m.active {
		out = append(out, s)
	}
	return out
}

// QueuedProjects 返回等待队列中所有项目的快照。
func (m *MultiProjectManager) QueuedProjects() []*ProjectSlot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*ProjectSlot, len(m.queue))
	copy(out, m.queue)
	return out
}

// AllocateTurns 消耗指定项目的 turn 配额。
func (m *MultiProjectManager) AllocateTurns(projectID string, turns int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	slot, ok := m.active[projectID]
	if !ok {
		return fmt.Errorf("project %s not active", projectID)
	}
	if slot.Quota.MaxTurns > 0 && slot.Quota.TurnsUsed+turns > slot.Quota.MaxTurns {
		return fmt.Errorf("turn quota exceeded for project %s (%d+%d > %d)",
			projectID, slot.Quota.TurnsUsed, turns, slot.Quota.MaxTurns)
	}
	slot.Quota.TurnsUsed += turns
	return nil
}

// CheckQuota 检查项目配额是否充足。
func (m *MultiProjectManager) CheckQuota(projectID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	slot, ok := m.active[projectID]
	if !ok {
		return false
	}
	q := slot.Quota
	if q.MaxTurns > 0 && q.TurnsUsed >= q.MaxTurns {
		return false
	}
	if q.MaxBrains > 0 && q.BrainsActive >= q.MaxBrains {
		return false
	}
	if q.MaxDuration > 0 && slot.StartedAt != nil {
		if time.Since(*slot.StartedAt) >= q.MaxDuration {
			return false
		}
	}
	return true
}

// Stats 返回管理器统计信息。
func (m *MultiProjectManager) Stats() MultiProjectStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var completed, failed int
	for _, s := range m.history {
		switch s.Status {
		case "completed":
			completed++
		case "failed":
			failed++
		}
	}
	return MultiProjectStats{
		ActiveCount:    len(m.active),
		QueuedCount:    len(m.queue),
		CompletedCount: completed,
		FailedCount:    failed,
		TotalSubmitted: len(m.active) + len(m.queue) + len(m.history),
	}
}

// ---------------------------------------------------------------------------
// 内部方法
// ---------------------------------------------------------------------------

// activate 将槽位设为 running 并放入 active map。
func (m *MultiProjectManager) activate(slot *ProjectSlot) {
	now := time.Now()
	slot.Status = "running"
	slot.StartedAt = &now
	m.active[slot.ProjectID] = slot
}

// scheduleNext 从队列调度下一个项目。
func (m *MultiProjectManager) scheduleNext() {
	for len(m.queue) > 0 && len(m.active) < m.config.MaxConcurrent {
		next := m.queue[0]
		m.queue = m.queue[1:]
		m.activate(next)
	}
}

// sortQueue 按优先级排序队列（数值越小优先级越高）。
func (m *MultiProjectManager) sortQueue() {
	sort.SliceStable(m.queue, func(i, j int) bool {
		return m.queue[i].Priority < m.queue[j].Priority
	})
}
