// project_memory_persistent.go — kernel.ProjectMemory 的持久化实现
//
// 把 persistence.ProjectMemoryStore(SQLite 或内存版)包装成
// kernel.ProjectMemory 接口,使 PlanOrchestrator / ContextEngine
// 可以无感切换到持久化记忆。

package kernel

import (
	"context"

	"github.com/leef-l/brain/sdk/persistence"
)

// PersistentProjectMemory 把 persistence.ProjectMemoryStore 适配为 ProjectMemory。
type PersistentProjectMemory struct {
	store persistence.ProjectMemoryStore
}

// NewPersistentProjectMemory 创建持久化版项目记忆。
// store 通常来自 persistence.Stores.ProjectMemoryStore(SQLite driver 提供)。
func NewPersistentProjectMemory(store persistence.ProjectMemoryStore) *PersistentProjectMemory {
	return &PersistentProjectMemory{store: store}
}

func (m *PersistentProjectMemory) Store(ctx context.Context, entry MemoryEntry) error {
	return m.store.StoreEntry(ctx, persistence.MemoryEntryRecord{
		ID:         entry.ID,
		ProjectID:  entry.ProjectID,
		Type:       string(entry.Type),
		Content:    entry.Content,
		Summary:    entry.Summary,
		Tags:       entry.Tags,
		Importance: entry.Importance,
		CreatedAt:  entry.CreatedAt,
		ExpiresAt:  entry.ExpiresAt,
	})
}

func (m *PersistentProjectMemory) Query(ctx context.Context, q MemoryQuery) ([]MemoryEntry, error) {
	types := make([]string, 0, len(q.Types))
	for _, t := range q.Types {
		types = append(types, string(t))
	}
	records, err := m.store.QueryEntries(ctx, persistence.MemoryQueryRecord{
		ProjectID:     q.ProjectID,
		Types:         types,
		Tags:          q.Tags,
		Keywords:      q.Keywords,
		MinImportance: q.MinImportance,
		Limit:         q.Limit,
		Since:         q.Since,
	})
	if err != nil {
		return nil, err
	}
	out := make([]MemoryEntry, 0, len(records))
	for _, r := range records {
		out = append(out, MemoryEntry{
			ID:         r.ID,
			ProjectID:  r.ProjectID,
			Type:       MemoryType(r.Type),
			Content:    r.Content,
			Summary:    r.Summary,
			Tags:       r.Tags,
			Importance: r.Importance,
			CreatedAt:  r.CreatedAt,
			ExpiresAt:  r.ExpiresAt,
		})
	}
	return out, nil
}

func (m *PersistentProjectMemory) Get(ctx context.Context, id string) (*MemoryEntry, error) {
	r, err := m.store.GetEntry(ctx, id)
	if err != nil || r == nil {
		return nil, err
	}
	return &MemoryEntry{
		ID:         r.ID,
		ProjectID:  r.ProjectID,
		Type:       MemoryType(r.Type),
		Content:    r.Content,
		Summary:    r.Summary,
		Tags:       r.Tags,
		Importance: r.Importance,
		CreatedAt:  r.CreatedAt,
		ExpiresAt:  r.ExpiresAt,
	}, nil
}

func (m *PersistentProjectMemory) Delete(ctx context.Context, id string) error {
	return m.store.DeleteEntry(ctx, id)
}

func (m *PersistentProjectMemory) Summarize(ctx context.Context, projectID string, maxTokens int) (string, error) {
	return m.store.SummarizeEntries(ctx, projectID, maxTokens)
}
