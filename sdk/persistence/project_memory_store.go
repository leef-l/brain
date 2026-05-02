// project_memory_store.go — 项目级记忆 SQLite 持久化(MACCS Wave 7+)
//
// 实现 sdk/kernel.ProjectMemory 接口的 SQLite 版本。当前 sdk/kernel
// 默认用 MemProjectMemory(内存,重启丢)。本文件提供持久化版本,
// 落 SQLite project_memory 表,跨会话保留 lessons / decisions / patterns 等。
//
// ⚠️ 依赖说明:本文件不导入 sdk/kernel(避免循环依赖)。
// 类型签名用 generic 字段,在 cmd/brain 装配层做适配封装。
// 也即 — kernel.ProjectMemory 接口实现侧通过适配器(见 chat 装配处)
// 把 *SQLiteProjectMemoryStore 包成 kernel.ProjectMemory。

package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// MemoryEntryRecord 是数据库层项目记忆条目(与 kernel.MemoryEntry 字段一致)。
type MemoryEntryRecord struct {
	ID         string
	ProjectID  string
	Type       string  // decision / conversation / artifact / lesson / preference / pattern
	Content    string
	Summary    string
	Tags       []string
	Importance float64
	CreatedAt  time.Time
	ExpiresAt  *time.Time
}

// MemoryQueryRecord 描述记忆检索条件。
type MemoryQueryRecord struct {
	ProjectID     string
	Types         []string
	Tags          []string
	Keywords      []string
	MinImportance float64
	Limit         int
	Since         *time.Time
}

// ProjectMemoryStore 项目级记忆持久化接口。
//
// 与 sdk/kernel.ProjectMemory 接口形状一致,但使用 persistence 包内部
// 类型(避免循环依赖)。kernel 端通过 adapter 将本接口包装为 ProjectMemory。
type ProjectMemoryStore interface {
	StoreEntry(ctx context.Context, entry MemoryEntryRecord) error
	QueryEntries(ctx context.Context, q MemoryQueryRecord) ([]MemoryEntryRecord, error)
	GetEntry(ctx context.Context, id string) (*MemoryEntryRecord, error)
	DeleteEntry(ctx context.Context, id string) error
	// SummarizeEntries 取项目最近 N 条 importance > 0.3 的记忆,
	// 拼成一段供 prompt 注入的文本。token 限额按 4 char ≈ 1 token 估算。
	SummarizeEntries(ctx context.Context, projectID string, maxTokens int) (string, error)
}

// ---------------------------------------------------------------------------
// SQLite implementation
// ---------------------------------------------------------------------------

type sqliteProjectMemoryStore struct {
	c *sqliteCore
}

func newSQLiteProjectMemoryStore(c *sqliteCore) *sqliteProjectMemoryStore {
	return &sqliteProjectMemoryStore{c: c}
}

func (s *sqliteProjectMemoryStore) ensureSchema() error {
	_, err := s.c.db.Exec(`
		CREATE TABLE IF NOT EXISTS project_memory (
			id          TEXT PRIMARY KEY,
			project_id  TEXT NOT NULL,
			type        TEXT NOT NULL,
			content     TEXT NOT NULL,
			summary     TEXT,
			tags_json   TEXT,
			importance  REAL,
			created_at  TEXT NOT NULL,
			expires_at  TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_pm_project ON project_memory(project_id);
		CREATE INDEX IF NOT EXISTS idx_pm_project_imp
			ON project_memory(project_id, importance DESC);
	`)
	return err
}

func (s *sqliteProjectMemoryStore) StoreEntry(ctx context.Context, entry MemoryEntryRecord) error {
	if entry.ProjectID == "" {
		return fmt.Errorf("project_memory.Store: project_id required")
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	tagsJSON := ""
	if len(entry.Tags) > 0 {
		b, err := json.Marshal(entry.Tags)
		if err != nil {
			return fmt.Errorf("marshal tags: %w", err)
		}
		tagsJSON = string(b)
	}
	expiresAt := ""
	if entry.ExpiresAt != nil {
		expiresAt = entry.ExpiresAt.Format(sqliteTimeLayout)
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO project_memory
		 (id, project_id, type, content, summary, tags_json, importance, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.ProjectID, entry.Type, entry.Content, entry.Summary,
		tagsJSON, entry.Importance,
		entry.CreatedAt.Format(sqliteTimeLayout),
		expiresAt,
	)
	if err != nil {
		return fmt.Errorf("project_memory.Store: %w", err)
	}
	return nil
}

func (s *sqliteProjectMemoryStore) QueryEntries(ctx context.Context, q MemoryQueryRecord) ([]MemoryEntryRecord, error) {
	if q.ProjectID == "" {
		return nil, fmt.Errorf("project_memory.Query: project_id required")
	}

	sb := strings.Builder{}
	sb.WriteString(`SELECT id, project_id, type, content, summary, tags_json, importance, created_at, expires_at
	                  FROM project_memory WHERE project_id = ?`)
	args := []interface{}{q.ProjectID}

	if q.MinImportance > 0 {
		sb.WriteString(" AND importance >= ?")
		args = append(args, q.MinImportance)
	}
	if q.Since != nil && !q.Since.IsZero() {
		sb.WriteString(" AND created_at >= ?")
		args = append(args, q.Since.Format(sqliteTimeLayout))
	}
	if len(q.Types) > 0 {
		sb.WriteString(" AND type IN (")
		for i, t := range q.Types {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, t)
		}
		sb.WriteString(")")
	}

	sb.WriteString(" ORDER BY importance DESC, created_at DESC")

	if q.Limit > 0 {
		sb.WriteString(" LIMIT ?")
		args = append(args, q.Limit)
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	rows, err := s.c.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("project_memory.Query: %w", err)
	}
	defer rows.Close()

	var out []MemoryEntryRecord
	for rows.Next() {
		entry, err := scanMemoryRow(rows)
		if err != nil {
			continue
		}
		// 关键词过滤(应用层做,SQLite FTS 后续优化)
		if len(q.Keywords) > 0 && !matchKeywords(entry, q.Keywords) {
			continue
		}
		// tags 子集过滤(应用层做)
		if len(q.Tags) > 0 && !hasTagsIntersect(entry.Tags, q.Tags) {
			continue
		}
		out = append(out, *entry)
	}
	return out, nil
}

func (s *sqliteProjectMemoryStore) GetEntry(ctx context.Context, id string) (*MemoryEntryRecord, error) {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	row := s.c.db.QueryRowContext(ctx,
		`SELECT id, project_id, type, content, summary, tags_json, importance, created_at, expires_at
		   FROM project_memory WHERE id = ?`, id)
	entry, err := scanMemoryRowSingle(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return entry, err
}

func (s *sqliteProjectMemoryStore) DeleteEntry(ctx context.Context, id string) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`DELETE FROM project_memory WHERE id = ?`, id)
	return err
}

func (s *sqliteProjectMemoryStore) SummarizeEntries(ctx context.Context, projectID string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 1000
	}
	maxChars := maxTokens * 4

	entries, err := s.QueryEntries(ctx, MemoryQueryRecord{
		ProjectID:     projectID,
		MinImportance: 0.3,
		Limit:         50,
	})
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}

	var sb strings.Builder
	for _, e := range entries {
		line := fmt.Sprintf("- [%s] %s", e.Type, firstNonEmpty(e.Summary, e.Content))
		if sb.Len()+len(line)+1 > maxChars {
			break
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String()), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type scannable interface {
	Scan(dest ...interface{}) error
}

func scanMemoryRow(r scannable) (*MemoryEntryRecord, error) {
	return scanMemoryRowSingle(r)
}

func scanMemoryRowSingle(r scannable) (*MemoryEntryRecord, error) {
	var e MemoryEntryRecord
	var tagsJSON, createdAt, expiresAt string
	err := r.Scan(&e.ID, &e.ProjectID, &e.Type, &e.Content, &e.Summary, &tagsJSON, &e.Importance, &createdAt, &expiresAt)
	if err != nil {
		return nil, err
	}
	if tagsJSON != "" {
		_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
	}
	e.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdAt)
	if expiresAt != "" {
		t, err := time.Parse(sqliteTimeLayout, expiresAt)
		if err == nil {
			e.ExpiresAt = &t
		}
	}
	return &e, nil
}

func matchKeywords(e *MemoryEntryRecord, keywords []string) bool {
	hay := strings.ToLower(e.Content + " " + e.Summary)
	for _, k := range keywords {
		if strings.Contains(hay, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

func hasTagsIntersect(a, b []string) bool {
	set := map[string]struct{}{}
	for _, t := range a {
		set[t] = struct{}{}
	}
	for _, t := range b {
		if _, ok := set[t]; ok {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// In-memory implementation(测试 + mem/file driver 用)
// ---------------------------------------------------------------------------

type memProjectMemoryStore struct {
	entries map[string][]MemoryEntryRecord // projectID → entries
}

// NewMemProjectMemoryStore 创建内存版项目记忆存储。
func NewMemProjectMemoryStore() ProjectMemoryStore {
	return &memProjectMemoryStore{entries: make(map[string][]MemoryEntryRecord)}
}

func (s *memProjectMemoryStore) StoreEntry(_ context.Context, e MemoryEntryRecord) error {
	if e.ProjectID == "" {
		return fmt.Errorf("project_id required")
	}
	if e.ID == "" {
		e.ID = fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	// 同 ID 替换
	list := s.entries[e.ProjectID]
	for i := range list {
		if list[i].ID == e.ID {
			list[i] = e
			s.entries[e.ProjectID] = list
			return nil
		}
	}
	s.entries[e.ProjectID] = append(list, e)
	return nil
}

func (s *memProjectMemoryStore) QueryEntries(_ context.Context, q MemoryQueryRecord) ([]MemoryEntryRecord, error) {
	src := s.entries[q.ProjectID]
	var out []MemoryEntryRecord
	for _, e := range src {
		if q.MinImportance > 0 && e.Importance < q.MinImportance {
			continue
		}
		if q.Since != nil && e.CreatedAt.Before(*q.Since) {
			continue
		}
		if len(q.Types) > 0 {
			matched := false
			for _, t := range q.Types {
				if e.Type == t {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(q.Keywords) > 0 && !matchKeywords(&e, q.Keywords) {
			continue
		}
		if len(q.Tags) > 0 && !hasTagsIntersect(e.Tags, q.Tags) {
			continue
		}
		out = append(out, e)
	}
	// 按 importance DESC, created_at DESC
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Importance > out[i].Importance ||
				(out[j].Importance == out[i].Importance && out[j].CreatedAt.After(out[i].CreatedAt)) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (s *memProjectMemoryStore) GetEntry(_ context.Context, id string) (*MemoryEntryRecord, error) {
	for _, list := range s.entries {
		for i := range list {
			if list[i].ID == id {
				cp := list[i]
				return &cp, nil
			}
		}
	}
	return nil, nil
}

func (s *memProjectMemoryStore) DeleteEntry(_ context.Context, id string) error {
	for pid, list := range s.entries {
		filtered := list[:0]
		for _, e := range list {
			if e.ID != id {
				filtered = append(filtered, e)
			}
		}
		s.entries[pid] = filtered
	}
	return nil
}

func (s *memProjectMemoryStore) SummarizeEntries(ctx context.Context, projectID string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 1000
	}
	maxChars := maxTokens * 4

	entries, _ := s.QueryEntries(ctx, MemoryQueryRecord{
		ProjectID:     projectID,
		MinImportance: 0.3,
		Limit:         50,
	})
	var sb strings.Builder
	for _, e := range entries {
		line := fmt.Sprintf("- [%s] %s", e.Type, firstNonEmpty(e.Summary, e.Content))
		if sb.Len()+len(line)+1 > maxChars {
			break
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String()), nil
}
