package tool

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SemanticAnnotation is a per-element semantic label used by the high-level
// browser semantic cache API.
type SemanticAnnotation struct {
	ElementKey    string  `json:"element_key"`
	ActionIntent  string  `json:"action_intent"`
	Reversibility string  `json:"reversibility"`
	RiskLevel     string  `json:"risk_level"`
	FlowRole      string  `json:"flow_role"`
	Confidence    float64 `json:"confidence"`
	Source        string  `json:"source"`
	Quality       string  `json:"quality"`
}

// BrowserSemanticCache is a SQLite-backed cache for page semantics with TTL.
// It provides URL-centric Save / Load / Invalidate APIs and can be used
// standalone or as a companion to the lower-level SemanticCache.
type BrowserSemanticCache struct {
	db  *sql.DB
	mu  sync.Mutex
	dsn string
	ttl time.Duration
}

const browserSemanticCacheSchema = `
CREATE TABLE IF NOT EXISTS page_semantics (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	url_pattern TEXT    NOT NULL,
	dom_hash    TEXT    NOT NULL DEFAULT '',
	element_key TEXT    NOT NULL DEFAULT '',
	annotation  BLOB    NOT NULL,
	created_at  TEXT    NOT NULL,
	expires_at  TEXT    NOT NULL,
	UNIQUE(url_pattern, dom_hash, element_key)
);
CREATE INDEX IF NOT EXISTS idx_page_semantics_url    ON page_semantics(url_pattern);
CREATE INDEX IF NOT EXISTS idx_page_semantics_expiry ON page_semantics(expires_at);
`

// NewBrowserSemanticCache opens the cache. If dsn is empty, defaults to
// ~/.brain/browser_semantic_cache.db. If ttl is zero, defaults to 7 days.
func NewBrowserSemanticCache(dsn string, ttl time.Duration) (*BrowserSemanticCache, error) {
	if dsn == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.TempDir()
		}
		dsn = filepath.Join(home, ".brain", "browser_semantic_cache.db")
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir cache dir: %w", err)
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(browserSemanticCacheSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &BrowserSemanticCache{db: db, dsn: dsn, ttl: ttl}, nil
}

// Close releases the DB handle.
func (c *BrowserSemanticCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Save persists annotations for a page. Any existing entries for the same
// (urlPattern, domHash) are replaced atomically.
func (c *BrowserSemanticCache) Save(ctx context.Context, urlPattern, domHash string, annotations []SemanticAnnotation) error {
	if c == nil || c.db == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM page_semantics WHERE url_pattern = ? AND dom_hash = ?`,
		urlPattern, domHash); err != nil {
		return fmt.Errorf("delete old: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO page_semantics(url_pattern, dom_hash, element_key, annotation, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC()
	expires := now.Add(c.ttl)
	nowStr := now.Format(time.RFC3339Nano)
	expStr := expires.Format(time.RFC3339Nano)

	for _, a := range annotations {
		key := a.ElementKey
		if key == "" {
			key = a.ActionIntent
		}
		data, _ := json.Marshal(a)
		if _, err := stmt.ExecContext(ctx, urlPattern, domHash, key, data, nowStr, expStr); err != nil {
			return fmt.Errorf("exec: %w", err)
		}
	}
	return tx.Commit()
}

// Load retrieves non-expired annotations for a page.
func (c *BrowserSemanticCache) Load(ctx context.Context, urlPattern, domHash string) ([]SemanticAnnotation, error) {
	if c == nil || c.db == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := c.db.QueryContext(ctx, `
		SELECT annotation FROM page_semantics
		WHERE url_pattern = ? AND dom_hash = ? AND expires_at > ?
	`, urlPattern, domHash, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []SemanticAnnotation
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			continue
		}
		var a SemanticAnnotation
		if err := json.Unmarshal(data, &a); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Invalidate removes all cached entries for a URL pattern regardless of
// DOM hash or TTL.
func (c *BrowserSemanticCache) Invalidate(ctx context.Context, urlPattern string) error {
	if c == nil || c.db == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.db.ExecContext(ctx,
		`DELETE FROM page_semantics WHERE url_pattern = ?`, urlPattern)
	return err
}

// CleanupExpired deletes entries whose TTL has passed. Returns the number of
// rows removed.
func (c *BrowserSemanticCache) CleanupExpired(ctx context.Context) (int64, error) {
	if c == nil || c.db == nil {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := c.db.ExecContext(ctx,
		`DELETE FROM page_semantics WHERE expires_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
