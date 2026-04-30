package kernel

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ProjectPhaseType — 七阶段项目生命周期枚举
type ProjectPhaseType string

const (
	PhaseRequirement ProjectPhaseType = "requirement"   // 需求解析
	PhaseDesign      ProjectPhaseType = "design"        // 方案设计
	PhaseReview      ProjectPhaseType = "review"        // 方案审核
	PhaseExecution   ProjectPhaseType = "execution"     // 任务执行
	PhaseAcceptance  ProjectPhaseType = "acceptance"    // 验收测试
	PhaseDelivery    ProjectPhaseType = "delivery"      // 交付生成
	PhaseRetrospect  ProjectPhaseType = "retrospective" // 复盘学习
)

// phaseOrder 定义阶段的严格执行顺序；phaseIndex 为顺序索引。
var phaseOrder = []ProjectPhaseType{
	PhaseRequirement, PhaseDesign, PhaseReview,
	PhaseExecution, PhaseAcceptance, PhaseDelivery, PhaseRetrospect,
}
var phaseIndex = func() map[ProjectPhaseType]int {
	m := make(map[ProjectPhaseType]int, len(phaseOrder))
	for i, p := range phaseOrder {
		m[p] = i
	}
	return m
}()

// PhaseRecord 记录单个阶段的执行状态、时间跨度、产出物及扩展数据。
type PhaseRecord struct {
	Phase     ProjectPhaseType       `json:"phase"`
	Status    string                 `json:"status"` // pending/running/completed/failed/skipped
	StartedAt *time.Time             `json:"started_at,omitempty"`
	EndedAt   *time.Time             `json:"ended_at,omitempty"`
	Artifacts []string               `json:"artifacts,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ProjectSession 是七阶段项目生命周期的 Session 容器，为 MACCS Wave 3
// EasyMVP 闭环工作流提供阶段状态机、跨阶段上下文共享和产出物追踪。
type ProjectSession struct {
	mu           sync.RWMutex                      `json:"-"`
	SessionID    string                            `json:"session_id"`
	ProjectID    string                            `json:"project_id"`
	ProjectName  string                            `json:"project_name"`
	Goal         string                            `json:"goal"`
	CurrentPhase ProjectPhaseType                  `json:"current_phase"`
	Phases       map[ProjectPhaseType]*PhaseRecord `json:"phases"`
	CreatedAt    time.Time                         `json:"created_at"`
	UpdatedAt    time.Time                         `json:"updated_at"`
	Status       string                            `json:"status"` // active/completed/failed/aborted
	Context      map[string]interface{}            `json:"context,omitempty"`
}

// NewProjectSession 创建项目 Session，自动初始化 7 个阶段为 pending。
func NewProjectSession(projectID, projectName, goal string) *ProjectSession {
	now := time.Now()
	phases := make(map[ProjectPhaseType]*PhaseRecord, len(phaseOrder))
	for _, p := range phaseOrder {
		phases[p] = &PhaseRecord{Phase: p, Status: "pending"}
	}
	return &ProjectSession{
		SessionID: fmt.Sprintf("sess-%d", now.UnixNano()), ProjectID: projectID,
		ProjectName: projectName, Goal: goal, CurrentPhase: PhaseRequirement,
		Phases: phases, CreatedAt: now, UpdatedAt: now, Status: "active",
		Context: make(map[string]interface{}),
	}
}

func (s *ProjectSession) phaseRec(phase ProjectPhaseType) (*PhaseRecord, error) {
	rec, ok := s.Phases[phase]
	if !ok {
		return nil, fmt.Errorf("未知阶段: %s", phase)
	}
	return rec, nil
}

// StartPhase 标记阶段开始。校验前置阶段必须已完成或跳过。
func (s *ProjectSession) StartPhase(phase ProjectPhaseType) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.phaseRec(phase)
	if err != nil {
		return err
	}
	if rec.Status != "pending" {
		return fmt.Errorf("阶段 %s 状态为 %s，无法启动", phase, rec.Status)
	}
	if idx := phaseIndex[phase]; idx > 0 {
		prev := s.Phases[phaseOrder[idx-1]]
		if prev.Status != "completed" && prev.Status != "skipped" {
			return fmt.Errorf("前置阶段 %s 未完成（%s），无法启动 %s", phaseOrder[idx-1], prev.Status, phase)
		}
	}
	now := time.Now()
	rec.Status = "running"
	rec.StartedAt = &now
	s.CurrentPhase = phase
	s.UpdatedAt = now
	return nil
}

// CompletePhase 标记阶段完成，附带产出物列表。
func (s *ProjectSession) CompletePhase(phase ProjectPhaseType, artifacts []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.phaseRec(phase)
	if err != nil {
		return err
	}
	if rec.Status != "running" {
		return fmt.Errorf("阶段 %s 状态为 %s，无法完成", phase, rec.Status)
	}
	now := time.Now()
	rec.Status = "completed"
	rec.EndedAt = &now
	rec.Artifacts = artifacts
	s.UpdatedAt = now
	if s.allPhasesTerminal() {
		s.Status = "completed"
	}
	return nil
}

// FailPhase 标记阶段失败。
func (s *ProjectSession) FailPhase(phase ProjectPhaseType, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.phaseRec(phase)
	if err != nil {
		return err
	}
	if rec.Status != "running" {
		return fmt.Errorf("阶段 %s 状态为 %s，无法标记失败", phase, rec.Status)
	}
	now := time.Now()
	rec.Status = "failed"
	rec.EndedAt = &now
	rec.Error = errMsg
	s.UpdatedAt = now
	s.Status = "failed"
	return nil
}

// SkipPhase 跳过指定阶段，记录跳过原因到 Metadata。
func (s *ProjectSession) SkipPhase(phase ProjectPhaseType, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.phaseRec(phase)
	if err != nil {
		return err
	}
	if rec.Status != "pending" {
		return fmt.Errorf("阶段 %s 状态为 %s，只有 pending 可跳过", phase, rec.Status)
	}
	now := time.Now()
	rec.Status = "skipped"
	rec.EndedAt = &now
	if rec.Metadata == nil {
		rec.Metadata = make(map[string]interface{})
	}
	rec.Metadata["skip_reason"] = reason
	s.UpdatedAt = now
	if s.allPhasesTerminal() {
		s.Status = "completed"
	}
	return nil
}

// RetryPhase 重置失败阶段为 pending，允许重试。
func (s *ProjectSession) RetryPhase(phase ProjectPhaseType) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.phaseRec(phase)
	if err != nil {
		return err
	}
	if rec.Status != "failed" {
		return fmt.Errorf("阶段 %s 状态为 %s，只有 failed 可重试", phase, rec.Status)
	}
	rec.Status = "pending"
	rec.StartedAt = nil
	rec.EndedAt = nil
	rec.Error = ""
	s.Status = "active"
	s.UpdatedAt = time.Now()
	return nil
}

// GetPhaseRecord 返回指定阶段记录的副本。
func (s *ProjectSession) GetPhaseRecord(phase ProjectPhaseType) *PhaseRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.Phases[phase]
	if !ok {
		return nil
	}
	cp := copyPhaseRecord(rec)
	return &cp
}

func copyPhaseRecord(r *PhaseRecord) PhaseRecord {
	cp := *r
	if r.Artifacts != nil {
		cp.Artifacts = make([]string, len(r.Artifacts))
		copy(cp.Artifacts, r.Artifacts)
	}
	if r.Metadata != nil {
		cp.Metadata = make(map[string]interface{}, len(r.Metadata))
		for k, v := range r.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
}

// SetContext 设置跨阶段共享上下文键值。
func (s *ProjectSession) SetContext(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Context[key] = value
	s.UpdatedAt = time.Now()
}

// GetContext 获取共享上下文值。
func (s *ProjectSession) GetContext(key string) (interface{}, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.Context[key]
	return v, ok
}

// IsCompleted 判断全部阶段是否已终结（completed 或 skipped）。
func (s *ProjectSession) IsCompleted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allPhasesTerminal()
}

// Snapshot 返回不含 mutex 的深拷贝，可安全用于 JSON 序列化。
func (s *ProjectSession) Snapshot() ProjectSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap := ProjectSession{
		SessionID: s.SessionID, ProjectID: s.ProjectID, ProjectName: s.ProjectName,
		Goal: s.Goal, CurrentPhase: s.CurrentPhase, CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt, Status: s.Status,
	}
	snap.Phases = make(map[ProjectPhaseType]*PhaseRecord, len(s.Phases))
	for k, v := range s.Phases {
		cp := copyPhaseRecord(v)
		snap.Phases[k] = &cp
	}
	snap.Context = make(map[string]interface{}, len(s.Context))
	for k, v := range s.Context {
		snap.Context[k] = v
	}
	return snap
}

// Duration 返回 Session 持续时长。若最后阶段已结束则到结束时间，否则到当前时间。
func (s *ProjectSession) Duration() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest time.Time
	for _, rec := range s.Phases {
		if rec.EndedAt != nil && rec.EndedAt.After(latest) {
			latest = *rec.EndedAt
		}
	}
	if latest.IsZero() {
		return time.Since(s.CreatedAt)
	}
	return latest.Sub(s.CreatedAt)
}

func (s *ProjectSession) allPhasesTerminal() bool {
	for _, r := range s.Phases {
		if r.Status != "completed" && r.Status != "skipped" {
			return false
		}
	}
	return true
}
// ProjectSessionStore 定义项目 Session 的持久化操作。
type ProjectSessionStore interface {
	SaveSession(ctx context.Context, session *ProjectSession) error
	LoadSession(ctx context.Context, sessionID string) (*ProjectSession, error)
	ListSessions(ctx context.Context, projectID string) ([]*ProjectSession, error)
	DeleteSession(ctx context.Context, sessionID string) error
}

// MemProjectSessionStore 基于 sync.RWMutex + map 的内存实现。
type MemProjectSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*ProjectSession
}
func NewMemProjectSessionStore() *MemProjectSessionStore {
	return &MemProjectSessionStore{sessions: make(map[string]*ProjectSession)}
}

func (m *MemProjectSessionStore) SaveSession(_ context.Context, sess *ProjectSession) error {
	snap := sess.Snapshot()
	m.mu.Lock()
	m.sessions[snap.SessionID] = &snap
	m.mu.Unlock()
	return nil
}

func (m *MemProjectSessionStore) LoadSession(_ context.Context, id string) (*ProjectSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %s 不存在", id)
	}
	snap := s.Snapshot()
	return &snap, nil
}

func (m *MemProjectSessionStore) ListSessions(_ context.Context, projectID string) ([]*ProjectSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*ProjectSession
	for _, s := range m.sessions {
		if s.ProjectID == projectID {
			snap := s.Snapshot()
			out = append(out, &snap)
		}
	}
	return out, nil
}

func (m *MemProjectSessionStore) DeleteSession(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return fmt.Errorf("session %s 不存在", id)
	}
	delete(m.sessions, id)
	return nil
}
