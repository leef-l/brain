// projects_store.go — 多项目元数据管理(MACCS Wave 7+ 项目级持久化)
//
// 一个 workdir 下可以有多个项目;每个项目有独立 ID 和对话历史 + 项目记忆。
// chat 模式启动时调 ListByWorkdir 列出当前目录所有项目供用户选择。
//
// 配套表:
//   - projects             本表,项目元数据
//   - project_conversations 项目对话历史(project_store.go,已有)
//   - project_memory       项目级记忆 entries(project_memory_store.go,新增)

package persistence

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ProjectMeta 描述一个项目的元数据。
type ProjectMeta struct {
	ID           string            `json:"id"`            // "abc123def456"(随机 12 位 hex)
	Workdir      string            `json:"workdir"`       // 工作目录绝对路径
	Name         string            `json:"name"`          // 用户可读名,workdir 内唯一
	CreatedAt    time.Time         `json:"created_at"`
	LastActiveAt time.Time         `json:"last_active_at"`
	Metadata     map[string]string `json:"metadata,omitempty"` // 自定义元数据
}

// ProjectsStore 管理项目元数据。
type ProjectsStore interface {
	// Create 创建新项目。如果 (workdir, name) 已存在,返回错误。
	// 不传 ID 时自动生成。
	Create(ctx context.Context, p *ProjectMeta) error

	// Get 按 ID 查项目。不存在返回 nil, nil(不报错,调用方判 nil)。
	Get(ctx context.Context, id string) (*ProjectMeta, error)

	// FindByName 按 (workdir, name) 查项目。不存在返回 nil, nil。
	FindByName(ctx context.Context, workdir, name string) (*ProjectMeta, error)

	// ListByWorkdir 列出指定 workdir 下所有项目,按 last_active_at DESC。
	ListByWorkdir(ctx context.Context, workdir string) ([]*ProjectMeta, error)

	// UpdateLastActive 更新项目活动时间(每次对话后调)。
	UpdateLastActive(ctx context.Context, id string, t time.Time) error

	// Rename 重命名项目。新名在同 workdir 内必须唯一。
	Rename(ctx context.Context, id, newName string) error

	// Delete 删除项目元数据。
	// 注意:本方法不级联删除 project_conversations / project_memory;
	// 调用方需配合 ProjectStore.DeleteMessages + project_memory 清理。
	Delete(ctx context.Context, id string) error
}

// GenerateProjectID 生成 12 位 hex 随机字符串作为项目 ID。
func GenerateProjectID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极端兜底:用纳秒时间戳
		return fmt.Sprintf("p%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// SQLite implementation
// ---------------------------------------------------------------------------

type sqliteProjectsStore struct {
	c *sqliteCore
}

func newSQLiteProjectsStore(c *sqliteCore) *sqliteProjectsStore {
	return &sqliteProjectsStore{c: c}
}

func (s *sqliteProjectsStore) ensureSchema() error {
	_, err := s.c.db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			id              TEXT PRIMARY KEY,
			workdir         TEXT NOT NULL,
			name            TEXT NOT NULL,
			created_at      TEXT NOT NULL,
			last_active_at  TEXT NOT NULL,
			metadata_json   TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_projects_workdir ON projects(workdir);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_workdir_name
			ON projects(workdir, name);
	`)
	return err
}

func (s *sqliteProjectsStore) Create(ctx context.Context, p *ProjectMeta) error {
	if p == nil {
		return fmt.Errorf("projects.Create: nil meta")
	}
	if strings.TrimSpace(p.Workdir) == "" {
		return fmt.Errorf("projects.Create: workdir required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("projects.Create: name required")
	}
	if p.ID == "" {
		p.ID = GenerateProjectID()
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.LastActiveAt.IsZero() {
		p.LastActiveAt = now
	}
	metaJSON := ""
	if len(p.Metadata) > 0 {
		b, err := json.Marshal(p.Metadata)
		if err != nil {
			return fmt.Errorf("projects.Create: marshal metadata: %w", err)
		}
		metaJSON = string(b)
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`INSERT INTO projects (id, workdir, name, created_at, last_active_at, metadata_json)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.Workdir, p.Name,
		p.CreatedAt.Format(sqliteTimeLayout),
		p.LastActiveAt.Format(sqliteTimeLayout),
		metaJSON,
	)
	if err != nil {
		// SQLite UNIQUE 冲突信息含 "UNIQUE constraint failed"。
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return fmt.Errorf("projects.Create: project %q already exists in workdir %q",
				p.Name, p.Workdir)
		}
		return fmt.Errorf("projects.Create: insert: %w", err)
	}
	return nil
}

func (s *sqliteProjectsStore) Get(ctx context.Context, id string) (*ProjectMeta, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	row := s.c.db.QueryRowContext(ctx,
		`SELECT id, workdir, name, created_at, last_active_at, metadata_json
		   FROM projects WHERE id = ?`, id)
	return s.scanRow(row)
}

func (s *sqliteProjectsStore) FindByName(ctx context.Context, workdir, name string) (*ProjectMeta, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	row := s.c.db.QueryRowContext(ctx,
		`SELECT id, workdir, name, created_at, last_active_at, metadata_json
		   FROM projects WHERE workdir = ? AND name = ?`, workdir, name)
	return s.scanRow(row)
}

func (s *sqliteProjectsStore) ListByWorkdir(ctx context.Context, workdir string) ([]*ProjectMeta, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	rows, err := s.c.db.QueryContext(ctx,
		`SELECT id, workdir, name, created_at, last_active_at, metadata_json
		   FROM projects WHERE workdir = ?
		   ORDER BY last_active_at DESC`, workdir)
	if err != nil {
		return nil, fmt.Errorf("projects.ListByWorkdir: query: %w", err)
	}
	defer rows.Close()
	var out []*ProjectMeta
	for rows.Next() {
		var p ProjectMeta
		var createdAt, lastActiveAt, metaJSON string
		if err := rows.Scan(&p.ID, &p.Workdir, &p.Name, &createdAt, &lastActiveAt, &metaJSON); err != nil {
			continue
		}
		p.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdAt)
		p.LastActiveAt, _ = time.Parse(sqliteTimeLayout, lastActiveAt)
		if metaJSON != "" {
			_ = json.Unmarshal([]byte(metaJSON), &p.Metadata)
		}
		out = append(out, &p)
	}
	return out, nil
}

func (s *sqliteProjectsStore) UpdateLastActive(ctx context.Context, id string, t time.Time) error {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`UPDATE projects SET last_active_at = ? WHERE id = ?`,
		t.UTC().Format(sqliteTimeLayout), id)
	if err != nil {
		return fmt.Errorf("projects.UpdateLastActive: %w", err)
	}
	return nil
}

func (s *sqliteProjectsStore) Rename(ctx context.Context, id, newName string) error {
	if strings.TrimSpace(newName) == "" {
		return fmt.Errorf("projects.Rename: empty name")
	}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`UPDATE projects SET name = ? WHERE id = ?`, newName, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return fmt.Errorf("projects.Rename: name %q already used in same workdir", newName)
		}
		return fmt.Errorf("projects.Rename: %w", err)
	}
	return nil
}

func (s *sqliteProjectsStore) Delete(ctx context.Context, id string) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("projects.Delete: %w", err)
	}
	return nil
}

func (s *sqliteProjectsStore) scanRow(row *sql.Row) (*ProjectMeta, error) {
	var p ProjectMeta
	var createdAt, lastActiveAt, metaJSON string
	err := row.Scan(&p.ID, &p.Workdir, &p.Name, &createdAt, &lastActiveAt, &metaJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdAt)
	p.LastActiveAt, _ = time.Parse(sqliteTimeLayout, lastActiveAt)
	if metaJSON != "" {
		_ = json.Unmarshal([]byte(metaJSON), &p.Metadata)
	}
	return &p, nil
}

// ---------------------------------------------------------------------------
// In-memory implementation(测试 + 无持久化场景)
// ---------------------------------------------------------------------------

type memProjectsStore struct {
	mu       sync.Mutex
	projects map[string]*ProjectMeta // id → project
}

// NewMemProjectsStore 创建内存版项目元数据存储(测试 / mem driver 用)。
func NewMemProjectsStore() ProjectsStore {
	return &memProjectsStore{projects: make(map[string]*ProjectMeta)}
}

func (s *memProjectsStore) Create(_ context.Context, p *ProjectMeta) error {
	if p == nil || p.Workdir == "" || p.Name == "" {
		return fmt.Errorf("invalid project meta")
	}
	if p.ID == "" {
		p.ID = GenerateProjectID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.LastActiveAt.IsZero() {
		p.LastActiveAt = p.CreatedAt
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.projects {
		if existing.Workdir == p.Workdir && existing.Name == p.Name {
			return fmt.Errorf("project %q already exists in workdir %q", p.Name, p.Workdir)
		}
	}
	s.projects[p.ID] = p
	return nil
}

func (s *memProjectsStore) Get(_ context.Context, id string) (*ProjectMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[id]; ok {
		cp := *p
		return &cp, nil
	}
	return nil, nil
}

func (s *memProjectsStore) FindByName(_ context.Context, workdir, name string) (*ProjectMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.projects {
		if p.Workdir == workdir && p.Name == name {
			cp := *p
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *memProjectsStore) ListByWorkdir(_ context.Context, workdir string) ([]*ProjectMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*ProjectMeta
	for _, p := range s.projects {
		if p.Workdir == workdir {
			cp := *p
			out = append(out, &cp)
		}
	}
	// 按 LastActiveAt DESC 排序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].LastActiveAt.After(out[i].LastActiveAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

func (s *memProjectsStore) UpdateLastActive(_ context.Context, id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[id]; ok {
		if t.IsZero() {
			t = time.Now().UTC()
		}
		p.LastActiveAt = t
	}
	return nil
}

func (s *memProjectsStore) Rename(_ context.Context, id, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, ok := s.projects[id]
	if !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	for _, p := range s.projects {
		if p.ID != id && p.Workdir == target.Workdir && p.Name == newName {
			return fmt.Errorf("name %q already used in workdir", newName)
		}
	}
	target.Name = newName
	return nil
}

func (s *memProjectsStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.projects, id)
	return nil
}
