// Package loop — Checkpoint/Resume 机制。
//
// 任务级断点续传：Agent Loop 在每个 Turn 完成后自动保存 checkpoint，
// 崩溃或重启后可以从最后一个 checkpoint 恢复，避免重复计算和重复计费。
// 这是实现 kimi 2.6 超越所需的生产级可靠性能力。
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/llm"
)

// Checkpoint 保存 Agent Loop 在某一时刻的完整状态。
type Checkpoint struct {
	// Version 用于向前/向后兼容。
	Version int `json:"version"`

	// RunID 关联的 Run 标识。
	RunID string `json:"run_id"`

	// RunJSON 是 Run 的序列化状态。
	RunJSON json.RawMessage `json:"run_json"`

	// Messages 是当前对话历史的完整副本。
	Messages []llm.Message `json:"messages"`

	// TurnResults 是已完成的 turn 结果列表。
	TurnResults []*TurnResult `json:"turn_results"`

	// CurrentTurn 是当前 turn 序号（用于快速恢复）。
	CurrentTurn int `json:"current_turn"`

	// PlanID 关联的 TaskPlan 标识（MACCS v2 中断恢复用）。
	PlanID string `json:"plan_id,omitempty"`

	// CurrentTaskID 当前正在执行的 TaskPlan 子任务 ID。
	CurrentTaskID string `json:"current_task_id,omitempty"`

	// ProjectID 关联的项目 ID，用于恢复项目级上下文。
	ProjectID string `json:"project_id,omitempty"`

	// SavedAt 是 checkpoint 保存时间。
	SavedAt time.Time `json:"saved_at"`
}

const checkpointVersion = 1

// CheckpointStore 定义 checkpoint 持久化接口。
type CheckpointStore interface {
	// Save 保存 checkpoint。
	Save(ctx context.Context, cp *Checkpoint) error

	// Load 加载指定 run 的最新 checkpoint。
	Load(ctx context.Context, runID string) (*Checkpoint, error)

	// Delete 删除指定 run 的所有 checkpoint。
	Delete(ctx context.Context, runID string) error
}

// FileCheckpointStore 基于文件系统的 checkpoint 存储。
type FileCheckpointStore struct {
	// Dir 是 checkpoint 文件存放目录。
	Dir string

	mu sync.Mutex
}

// NewFileCheckpointStore 创建文件系统 checkpoint 存储。
func NewFileCheckpointStore(dir string) (*FileCheckpointStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("checkpoint dir: %w", err)
	}
	return &FileCheckpointStore{Dir: dir}, nil
}

// Save 将 checkpoint 写入文件。
func (s *FileCheckpointStore) Save(_ context.Context, cp *Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	path := s.path(cp.RunID)
	tmp := path + ".tmp"

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write checkpoint tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename checkpoint: %w", err)
	}
	return nil
}

// Load 从文件加载 checkpoint。
func (s *FileCheckpointStore) Load(_ context.Context, runID string) (*Checkpoint, error) {
	path := s.path(runID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("checkpoint not found for run %s", runID)
		}
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("unmarshal checkpoint: %w", err)
	}
	return &cp, nil
}

// Delete 删除 checkpoint 文件。
func (s *FileCheckpointStore) Delete(_ context.Context, runID string) error {
	path := s.path(runID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove checkpoint: %w", err)
	}
	return nil
}

func (s *FileCheckpointStore) path(runID string) string {
	return filepath.Join(s.Dir, fmt.Sprintf("checkpoint-%s.json", runID))
}

// MemoryCheckpointStore 内存 checkpoint 存储（用于测试）。
type MemoryCheckpointStore struct {
	mu   sync.RWMutex
	data map[string]*Checkpoint
}

// NewMemoryCheckpointStore 创建内存 checkpoint 存储。
func NewMemoryCheckpointStore() *MemoryCheckpointStore {
	return &MemoryCheckpointStore{data: make(map[string]*Checkpoint)}
}

func (s *MemoryCheckpointStore) Save(_ context.Context, cp *Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[cp.RunID] = cp
	return nil
}

func (s *MemoryCheckpointStore) Load(_ context.Context, runID string) (*Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp, ok := s.data[runID]
	if !ok {
		return nil, fmt.Errorf("checkpoint not found for run %s", runID)
	}
	return cp, nil
}

func (s *MemoryCheckpointStore) Delete(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, runID)
	return nil
}

// ---------------------------------------------------------------------------
// Runner 集成
// ---------------------------------------------------------------------------

// CheckpointAfterTurn 在单个 turn 完成后保存 checkpoint。
// 如果 store 为 nil 或序列化失败，错误会被忽略（checkpoint 是 best-effort）。
func CheckpointAfterTurn(store CheckpointStore, run *Run, messages []llm.Message, turns []*TurnResult) {
	if store == nil {
		return
	}

	runJSON, err := json.Marshal(run)
	if err != nil {
		return
	}

	cp := &Checkpoint{
		Version:     checkpointVersion,
		RunID:       run.ID,
		RunJSON:     runJSON,
		Messages:    append([]llm.Message(nil), messages...),
		TurnResults: append([]*TurnResult(nil), turns...),
		CurrentTurn: run.CurrentTurn,
		SavedAt:     time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Save(ctx, cp); err != nil {
		// checkpoint 保存失败不阻塞主流程
		fmt.Fprintf(os.Stderr, "checkpoint: save failed for run %s: %v\n", run.ID, err)
	}
}

// RestoreFromCheckpoint 从 checkpoint 恢复 Run 状态。
// 返回恢复的 messages、turns 和 bool（true 表示成功恢复）。
func RestoreFromCheckpoint(store CheckpointStore, run *Run) ([]llm.Message, []*TurnResult, bool) {
	if store == nil || run == nil || run.ID == "" {
		return nil, nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cp, err := store.Load(ctx, run.ID)
	if err != nil {
		return nil, nil, false
	}

	// 反序列化 Run 状态
	if len(cp.RunJSON) > 0 {
		var restoredRun Run
		if err := json.Unmarshal(cp.RunJSON, &restoredRun); err == nil {
			// 只恢复关键字段，保留当前 Run 的引用
			run.CurrentTurn = restoredRun.CurrentTurn
			run.State = StateRunning // 恢复后设置为运行中
			run.Budget = restoredRun.Budget
			if restoredRun.StartedAt.IsZero() {
				run.StartedAt = time.Now()
			}
		}
	}

	messages := make([]llm.Message, len(cp.Messages))
	copy(messages, cp.Messages)

	turns := make([]*TurnResult, len(cp.TurnResults))
	copy(turns, cp.TurnResults)

	fmt.Fprintf(os.Stderr, "checkpoint: restored run %s from turn %d\n", run.ID, cp.CurrentTurn)
	return messages, turns, true
}
