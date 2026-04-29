package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leef-l/brain/sdk/llm"
)

// ProjectStore persists and retrieves project-level conversation history.
// This enables the Central Brain to retain the entire project dialogue across
// multiple Runs, satisfying the requirement that "the central brain saves the
// whole project conversation".
type ProjectStore interface {
	// SaveMessages appends messages to the project's conversation history.
	SaveMessages(ctx context.Context, projectID string, messages []llm.Message) error
	// LoadMessages retrieves the most recent N messages for a project.
	LoadMessages(ctx context.Context, projectID string, limit int) ([]llm.Message, error)
	// DeleteMessages removes all messages for a project (e.g. on project deletion).
	DeleteMessages(ctx context.Context, projectID string) error
}

// ---------------------------------------------------------------------------
// SQLite implementation
// ---------------------------------------------------------------------------

// sqliteProjectStore implements ProjectStore on top of the SQLite driver.
type sqliteProjectStore struct {
	c *sqliteCore
}

func newSQLiteProjectStore(c *sqliteCore) *sqliteProjectStore {
	return &sqliteProjectStore{c: c}
}

func (s *sqliteProjectStore) ensureSchema() error {
	_, err := s.c.db.Exec(`
		CREATE TABLE IF NOT EXISTS project_conversations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_project_conv_project ON project_conversations(project_id);
		CREATE INDEX IF NOT EXISTS idx_project_conv_created ON project_conversations(project_id, created_at);
	`)
	return err
}

func (s *sqliteProjectStore) SaveMessages(ctx context.Context, projectID string, messages []llm.Message) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, m := range messages {
		content, err := json.Marshal(m.Content)
		if err != nil {
			return fmt.Errorf("marshal message content: %w", err)
		}
		_, err = s.c.db.ExecContext(ctx,
			`INSERT INTO project_conversations (project_id, role, content_json, created_at)
			 VALUES (?, ?, ?, ?)`,
			projectID, m.Role, string(content), now)
		if err != nil {
			return fmt.Errorf("insert project conversation: %w", err)
		}
	}
	return nil
}

func (s *sqliteProjectStore) LoadMessages(ctx context.Context, projectID string, limit int) ([]llm.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	rows, err := s.c.db.QueryContext(ctx,
		`SELECT role, content_json FROM project_conversations
		 WHERE project_id = ?
		 ORDER BY created_at ASC, id ASC
		 LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []llm.Message
	for rows.Next() {
		var role string
		var contentJSON string
		if err := rows.Scan(&role, &contentJSON); err != nil {
			continue
		}
		var content []llm.ContentBlock
		if err := json.Unmarshal([]byte(contentJSON), &content); err != nil {
			// Best-effort: if unmarshal fails, store raw text block.
			content = []llm.ContentBlock{{Type: "text", Text: contentJSON}}
		}
		out = append(out, llm.Message{Role: role, Content: content})
	}
	return out, nil
}

func (s *sqliteProjectStore) DeleteMessages(ctx context.Context, projectID string) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`DELETE FROM project_conversations WHERE project_id = ?`, projectID)
	return err
}

// ---------------------------------------------------------------------------
// In-memory implementation (for tests / file driver fallback)
// ---------------------------------------------------------------------------

type memProjectStore struct {
	messages map[string][]llm.Message
}

func newMemProjectStore() *memProjectStore {
	return &memProjectStore{messages: make(map[string][]llm.Message)}
}

func (s *memProjectStore) SaveMessages(_ context.Context, projectID string, messages []llm.Message) error {
	s.messages[projectID] = append(s.messages[projectID], messages...)
	return nil
}

func (s *memProjectStore) LoadMessages(_ context.Context, projectID string, limit int) ([]llm.Message, error) {
	msgs := s.messages[projectID]
	if len(msgs) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit >= len(msgs) {
		return append([]llm.Message(nil), msgs...), nil
	}
	return append([]llm.Message(nil), msgs[len(msgs)-limit:]...), nil
}

func (s *memProjectStore) DeleteMessages(_ context.Context, projectID string) error {
	delete(s.messages, projectID)
	return nil
}
