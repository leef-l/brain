package kernel

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryEntry 是记忆存储的基本单元。
type MemoryEntry struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Type       MemoryType `json:"type"`                  // decision/conversation/artifact/lesson
	Content    string     `json:"content"`
	Summary    string     `json:"summary"`               // 摘要（用于快速检索）
	Tags       []string   `json:"tags"`
	Importance float64    `json:"importance"`             // 0-1，重要程度
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`   // nil = 永不过期
}

// MemoryType 记忆类型枚举。
type MemoryType string

const (
	MemoryDecision     MemoryType = "decision"     // 关键决策
	MemoryConversation MemoryType = "conversation"  // 对话摘要
	MemoryArtifact     MemoryType = "artifact"      // 产出物记录
	MemoryLesson       MemoryType = "lesson"        // 经验教训
	MemoryPreference   MemoryType = "preference"    // 用户偏好
	MemoryPattern      MemoryType = "pattern"       // 识别到的模式
)

// MemoryQuery 记忆查询请求。
type MemoryQuery struct {
	ProjectID     string       `json:"project_id"`
	Types         []MemoryType `json:"types,omitempty"`       // 过滤类型
	Tags          []string     `json:"tags,omitempty"`         // 标签过滤
	Keywords      []string     `json:"keywords,omitempty"`     // 关键词搜索
	MinImportance float64      `json:"min_importance"`         // 最低重要度
	Limit         int          `json:"limit"`                  // 返回数量上限
	Since         *time.Time   `json:"since,omitempty"`        // 时间下限
}

// ProjectMemory 项目级记忆存储接口。
type ProjectMemory interface {
	// Store 存储一条记忆。
	Store(ctx context.Context, entry MemoryEntry) error

	// Query 按条件检索记忆。
	Query(ctx context.Context, query MemoryQuery) ([]MemoryEntry, error)

	// Get 按 ID 获取单条记忆。
	Get(ctx context.Context, id string) (*MemoryEntry, error)

	// Delete 删除记忆。
	Delete(ctx context.Context, id string) error

	// Summarize 获取项目的记忆摘要（用于注入 prompt）。
	Summarize(ctx context.Context, projectID string, maxTokens int) (string, error)
}

// MemProjectMemory 基于内存的项目记忆实现。
type MemProjectMemory struct {
	mu      sync.RWMutex
	entries map[string][]MemoryEntry // projectID -> entries
}

// NewMemProjectMemory 创建基于内存的项目记忆实例。
func NewMemProjectMemory() *MemProjectMemory {
	return &MemProjectMemory{
		entries: make(map[string][]MemoryEntry),
	}
}

// Store 存储一条记忆。如果 ID 为空则自动生成。
func (m *MemProjectMemory) Store(_ context.Context, entry MemoryEntry) error {
	if entry.ProjectID == "" {
		return fmt.Errorf("project_id is required")
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[entry.ProjectID] = append(m.entries[entry.ProjectID], entry)
	return nil
}

// Query 按条件检索记忆。
// 过滤顺序：ProjectID → Types → Tags（交集）→ Keywords（Content/Summary 包含任一）→ MinImportance → Since
// 排序：按 Importance 降序
// 截取：Limit
func (m *MemProjectMemory) Query(_ context.Context, query MemoryQuery) ([]MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 按 ProjectID 获取
	all, ok := m.entries[query.ProjectID]
	if !ok || len(all) == 0 {
		return nil, nil
	}

	var results []MemoryEntry
	for _, e := range all {
		// 按 Types 过滤
		if len(query.Types) > 0 && !containsType(query.Types, e.Type) {
			continue
		}
		// 按 Tags 过滤（交集：entry 必须包含查询中的所有 tag）
		if len(query.Tags) > 0 && !hasAllTags(e.Tags, query.Tags) {
			continue
		}
		// 按 Keywords 过滤（Content/Summary 包含任一关键词）
		if len(query.Keywords) > 0 && !matchesAnyKeyword(e, query.Keywords) {
			continue
		}
		// 按 MinImportance 过滤
		if e.Importance < query.MinImportance {
			continue
		}
		// 按 Since 过滤
		if query.Since != nil && e.CreatedAt.Before(*query.Since) {
			continue
		}
		results = append(results, e)
	}

	// 按 Importance 降序排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Importance > results[j].Importance
	})

	// 截取 Limit
	if query.Limit > 0 && len(results) > query.Limit {
		results = results[:query.Limit]
	}

	return results, nil
}

// Get 按 ID 获取单条记忆。
func (m *MemProjectMemory) Get(_ context.Context, id string) (*MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, entries := range m.entries {
		for i := range entries {
			if entries[i].ID == id {
				cp := entries[i]
				return &cp, nil
			}
		}
	}
	return nil, fmt.Errorf("memory entry not found: %s", id)
}

// Delete 删除记忆。
func (m *MemProjectMemory) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for pid, entries := range m.entries {
		for i := range entries {
			if entries[i].ID == id {
				m.entries[pid] = append(entries[:i], entries[i+1:]...)
				return nil
			}
		}
	}
	return fmt.Errorf("memory entry not found: %s", id)
}

// Summarize 获取项目的记忆摘要（用于注入 prompt）。
// 取 importance >= 0.5 的 entries，按重要度降序排列，
// 拼接 Summary 字段，粗略按 1 token ≈ 4 chars 估算，不超过 maxTokens。
func (m *MemProjectMemory) Summarize(_ context.Context, projectID string, maxTokens int) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entries, ok := m.entries[projectID]
	if !ok || len(entries) == 0 {
		return "", nil
	}

	// 筛选 importance >= 0.5
	var important []MemoryEntry
	for _, e := range entries {
		if e.Importance >= 0.5 {
			important = append(important, e)
		}
	}
	if len(important) == 0 {
		return "", nil
	}

	// 按重要度降序
	sort.Slice(important, func(i, j int) bool {
		return important[i].Importance > important[j].Importance
	})

	maxChars := maxTokens * 4
	var sb strings.Builder
	for _, e := range important {
		line := fmt.Sprintf("[%s] %s\n", e.Type, e.Summary)
		if sb.Len()+len(line) > maxChars {
			break
		}
		sb.WriteString(line)
	}

	return sb.String(), nil
}

// containsType 检查类型列表中是否包含指定类型。
func containsType(types []MemoryType, t MemoryType) bool {
	for _, mt := range types {
		if mt == t {
			return true
		}
	}
	return false
}

// hasAllTags 检查 entryTags 是否包含 queryTags 中的所有标签。
func hasAllTags(entryTags, queryTags []string) bool {
	tagSet := make(map[string]struct{}, len(entryTags))
	for _, t := range entryTags {
		tagSet[t] = struct{}{}
	}
	for _, qt := range queryTags {
		if _, ok := tagSet[qt]; !ok {
			return false
		}
	}
	return true
}

// matchesAnyKeyword 检查 entry 的 Content 或 Summary 是否包含任一关键词。
func matchesAnyKeyword(e MemoryEntry, keywords []string) bool {
	contentLower := strings.ToLower(e.Content)
	summaryLower := strings.ToLower(e.Summary)
	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)
		if strings.Contains(contentLower, kwLower) || strings.Contains(summaryLower, kwLower) {
			return true
		}
	}
	return false
}
