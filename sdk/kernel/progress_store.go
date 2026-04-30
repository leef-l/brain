package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ProgressStore 定义项目进度持久化接口。
type ProgressStore interface {
	// SaveProgress 保存项目进度快照。
	SaveProgress(ctx context.Context, progress *ProjectProgress) error

	// LoadProgress 加载指定项目的进度。
	LoadProgress(ctx context.Context, projectID string) (*ProjectProgress, error)

	// DeleteProgress 删除指定项目的进度数据。
	DeleteProgress(ctx context.Context, projectID string) error

	// ListProjects 列出所有有进度记录的项目 ID。
	ListProjects(ctx context.Context) ([]string, error)
}

// MemoryProgressStore 基于内存的进度存储（用于测试和单次运行）。
type MemoryProgressStore struct {
	mu   sync.RWMutex
	data map[string]*ProjectProgress
}

func NewMemoryProgressStore() *MemoryProgressStore {
	return &MemoryProgressStore{
		data: make(map[string]*ProjectProgress),
	}
}

func (s *MemoryProgressStore) SaveProgress(_ context.Context, progress *ProjectProgress) error {
	if progress == nil {
		return fmt.Errorf("progress is nil")
	}
	snap := progress.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[snap.ProjectID] = &snap
	return nil
}

func (s *MemoryProgressStore) LoadProgress(_ context.Context, projectID string) (*ProjectProgress, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.data[projectID]
	if !ok {
		return nil, fmt.Errorf("progress not found for project %s", projectID)
	}
	snap := *p
	return &snap, nil
}

func (s *MemoryProgressStore) DeleteProgress(_ context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, projectID)
	return nil
}

func (s *MemoryProgressStore) ListProjects(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.data))
	for id := range s.data {
		ids = append(ids, id)
	}
	return ids, nil
}

// FileProgressStore 基于文件系统的进度存储。
type FileProgressStore struct {
	dir string
	mu  sync.Mutex
}

func NewFileProgressStore(dir string) (*FileProgressStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("progress store dir: %w", err)
	}
	return &FileProgressStore{dir: dir}, nil
}

func (s *FileProgressStore) SaveProgress(_ context.Context, progress *ProjectProgress) error {
	if progress == nil {
		return fmt.Errorf("progress is nil")
	}
	snap := progress.Snapshot()
	data, err := json.MarshalIndent(&snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal progress: %w", err)
	}

	path := s.path(snap.ProjectID)
	tmp := path + ".tmp"

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write progress tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename progress: %w", err)
	}
	return nil
}

func (s *FileProgressStore) LoadProgress(_ context.Context, projectID string) (*ProjectProgress, error) {
	path := s.path(projectID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("progress not found for project %s", projectID)
		}
		return nil, fmt.Errorf("read progress: %w", err)
	}

	var p ProjectProgress
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("unmarshal progress: %w", err)
	}
	return &p, nil
}

func (s *FileProgressStore) DeleteProgress(_ context.Context, projectID string) error {
	path := s.path(projectID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove progress: %w", err)
	}
	return nil
}

func (s *FileProgressStore) ListProjects(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read progress dir: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > 14 && name[:9] == "progress-" && name[len(name)-5:] == ".json" {
			ids = append(ids, name[9:len(name)-5])
		}
	}
	return ids, nil
}

func (s *FileProgressStore) path(projectID string) string {
	return filepath.Join(s.dir, fmt.Sprintf("progress-%s.json", projectID))
}
