package tool

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Semantic cache for browser.understand — SQLite-backed L4-L7 semantic labels.
//
// See sdk/docs/40-Browser-Brain语义理解架构.md §3.2.
//
// Keyed by (url_pattern, dom_hash_prefix) so minor URL/DOM variations still
// hit the same entry. Phase 0 experiment showed cheap models hit 82-85% on
// A/C combos, so we prefer batch LLM pre-annotation + cache reuse over
// runtime LLM calls.

const semanticSchema = `
CREATE TABLE IF NOT EXISTS semantic_entries (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	url_pattern     TEXT    NOT NULL,
	dom_hash        TEXT    NOT NULL,
	element_key     TEXT    NOT NULL,
	action_intent   TEXT    NOT NULL DEFAULT '',
	reversibility   TEXT    NOT NULL DEFAULT '',
	risk_level      TEXT    NOT NULL DEFAULT '',
	flow_role       TEXT    NOT NULL DEFAULT '',
	source          TEXT    NOT NULL DEFAULT '',
	quality         TEXT    NOT NULL DEFAULT '',
	confidence      REAL    NOT NULL DEFAULT 0,
	created_at      TEXT    NOT NULL,
	last_used_at    TEXT    NOT NULL,
	hit_count       INTEGER NOT NULL DEFAULT 0,
	UNIQUE(url_pattern, dom_hash, element_key)
);

CREATE INDEX IF NOT EXISTS idx_semantic_lookup ON semantic_entries(url_pattern, dom_hash);
CREATE INDEX IF NOT EXISTS idx_semantic_lru    ON semantic_entries(last_used_at);
`

// SemanticEntry mirrors one row.
type SemanticEntry struct {
	URLPattern   string    `json:"url_pattern"`
	DOMHash      string    `json:"dom_hash"`
	ElementKey   string    `json:"element_key"`
	ActionIntent string    `json:"action_intent"`
	Reversibility string   `json:"reversibility"`
	RiskLevel    string    `json:"risk_level"`
	FlowRole     string    `json:"flow_role"`
	Source       string    `json:"source"`    // "rules" | "llm" | "hybrid"
	Quality      string    `json:"quality"`   // "full" | "structural_only" | "low_confidence"
	Confidence   float64   `json:"confidence"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
	HitCount     int       `json:"hit_count"`
}

// SemanticCache is the SQLite-backed store.
type SemanticCache struct {
	db  *sql.DB
	mu  sync.Mutex
	dsn string
}

// defaultSemanticDSN returns ~/.brain/browser_semantics.db.
func defaultSemanticDSN() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".brain", "browser_semantics.db")
}

// NewSemanticCache opens (creating as needed) the SQLite semantic store.
// If dsn is empty, defaults to ~/.brain/browser_semantics.db.
func NewSemanticCache(dsn string) (*SemanticCache, error) {
	if dsn == "" {
		dsn = defaultSemanticDSN()
	}
	if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir cache dir: %w", err)
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // single writer
	if _, err := db.Exec(semanticSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &SemanticCache{db: db, dsn: dsn}, nil
}

// Close releases DB handle.
func (c *SemanticCache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// Lookup retrieves cached entries for a page/element bundle.
// Returns map keyed by element_key. Missing keys are not populated.
func (c *SemanticCache) Lookup(ctx context.Context, urlPattern, domHash string, elementKeys []string) (map[string]*SemanticEntry, error) {
	if c == nil || c.db == nil || len(elementKeys) == 0 {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Build query with IN clause.
	args := make([]interface{}, 0, len(elementKeys)+2)
	args = append(args, urlPattern, domHash)
	placeholders := ""
	for i, k := range elementKeys {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, k)
	}
	query := fmt.Sprintf(`
SELECT url_pattern, dom_hash, element_key, action_intent, reversibility, risk_level, flow_role,
       source, quality, confidence, created_at, last_used_at, hit_count
FROM semantic_entries
WHERE url_pattern = ? AND dom_hash = ? AND element_key IN (%s)
`, placeholders)

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	out := map[string]*SemanticEntry{}
	hitIDs := make([]string, 0, len(elementKeys))
	for rows.Next() {
		var e SemanticEntry
		var createdAt, lastUsedAt string
		if err := rows.Scan(&e.URLPattern, &e.DOMHash, &e.ElementKey, &e.ActionIntent,
			&e.Reversibility, &e.RiskLevel, &e.FlowRole, &e.Source, &e.Quality,
			&e.Confidence, &createdAt, &lastUsedAt, &e.HitCount); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		e.LastUsedAt, _ = time.Parse(time.RFC3339Nano, lastUsedAt)
		out[e.ElementKey] = &e
		hitIDs = append(hitIDs, e.ElementKey)
	}

	// Bump hit_count & last_used_at in a single statement.
	if len(hitIDs) > 0 {
		bumpArgs := []interface{}{time.Now().UTC().Format(time.RFC3339Nano), urlPattern, domHash}
		ph := ""
		for i, k := range hitIDs {
			if i > 0 {
				ph += ","
			}
			ph += "?"
			bumpArgs = append(bumpArgs, k)
		}
		bump := fmt.Sprintf(`UPDATE semantic_entries SET hit_count = hit_count + 1, last_used_at = ? WHERE url_pattern = ? AND dom_hash = ? AND element_key IN (%s)`, ph)
		_, _ = c.db.ExecContext(ctx, bump, bumpArgs...)
	}
	return out, nil
}

// Upsert saves or updates an entry.
func (c *SemanticCache) Upsert(ctx context.Context, e *SemanticEntry) error {
	if c == nil || c.db == nil || e == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	e.LastUsedAt = time.Now().UTC()

	_, err := c.db.ExecContext(ctx, `
INSERT INTO semantic_entries(
	url_pattern, dom_hash, element_key, action_intent, reversibility, risk_level, flow_role,
	source, quality, confidence, created_at, last_used_at, hit_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
ON CONFLICT(url_pattern, dom_hash, element_key) DO UPDATE SET
	action_intent = excluded.action_intent,
	reversibility = excluded.reversibility,
	risk_level    = excluded.risk_level,
	flow_role     = excluded.flow_role,
	source        = excluded.source,
	quality       = excluded.quality,
	confidence    = excluded.confidence,
	last_used_at  = excluded.last_used_at
`,
		e.URLPattern, e.DOMHash, e.ElementKey,
		e.ActionIntent, e.Reversibility, e.RiskLevel, e.FlowRole,
		e.Source, e.Quality, e.Confidence,
		e.CreatedAt.Format(time.RFC3339Nano), now)
	return err
}

// BatchUpsert writes many entries in a single transaction for LLM batch results.
func (c *SemanticCache) BatchUpsert(ctx context.Context, entries []*SemanticEntry) error {
	if c == nil || c.db == nil || len(entries) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO semantic_entries(
	url_pattern, dom_hash, element_key, action_intent, reversibility, risk_level, flow_role,
	source, quality, confidence, created_at, last_used_at, hit_count
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
ON CONFLICT(url_pattern, dom_hash, element_key) DO UPDATE SET
	action_intent = excluded.action_intent,
	reversibility = excluded.reversibility,
	risk_level    = excluded.risk_level,
	flow_role     = excluded.flow_role,
	source        = excluded.source,
	quality       = excluded.quality,
	confidence    = excluded.confidence,
	last_used_at  = excluded.last_used_at
`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	for _, e := range entries {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		if _, err := stmt.ExecContext(ctx,
			e.URLPattern, e.DOMHash, e.ElementKey,
			e.ActionIntent, e.Reversibility, e.RiskLevel, e.FlowRole,
			e.Source, e.Quality, e.Confidence,
			e.CreatedAt.Format(time.RFC3339Nano), nowStr); err != nil {
			tx.Rollback()
			return fmt.Errorf("exec: %w", err)
		}
	}
	return tx.Commit()
}

// Stats returns basic cache counters.
type CacheStats struct {
	TotalEntries    int       `json:"total_entries"`
	BySource        map[string]int `json:"by_source"`
	OldestCreatedAt time.Time `json:"oldest_created_at"`
	TotalHits       int       `json:"total_hits"`
}

func (c *SemanticCache) Stats(ctx context.Context) (*CacheStats, error) {
	if c == nil || c.db == nil {
		return &CacheStats{BySource: map[string]int{}}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	stats := &CacheStats{BySource: map[string]int{}}
	row := c.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(hit_count), 0) FROM semantic_entries`)
	_ = row.Scan(&stats.TotalEntries, &stats.TotalHits)

	rows, err := c.db.QueryContext(ctx, `SELECT source, COUNT(*) FROM semantic_entries GROUP BY source`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var src string
			var n int
			_ = rows.Scan(&src, &n)
			stats.BySource[src] = n
		}
	}

	var oldestStr sql.NullString
	if err := c.db.QueryRowContext(ctx, `SELECT MIN(created_at) FROM semantic_entries`).Scan(&oldestStr); err == nil && oldestStr.Valid {
		stats.OldestCreatedAt, _ = time.Parse(time.RFC3339Nano, oldestStr.String)
	}
	return stats, nil
}

// Purge deletes entries older than maxAge (LRU based on last_used_at).
func (c *SemanticCache) Purge(ctx context.Context, maxAge time.Duration) (int64, error) {
	if c == nil || c.db == nil {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339Nano)
	res, err := c.db.ExecContext(ctx, `DELETE FROM semantic_entries WHERE last_used_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Invalidate removes cached entries for a URL pattern. If domHash is empty,
// all entries for the URL pattern are deleted; otherwise only the specific
// DOM hash bucket is removed.
func (c *SemanticCache) Invalidate(ctx context.Context, urlPattern, domHash string) error {
	if c == nil || c.db == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if domHash != "" {
		_, err := c.db.ExecContext(ctx, `DELETE FROM semantic_entries WHERE url_pattern = ? AND dom_hash = ?`, urlPattern, domHash)
		return err
	}
	_, err := c.db.ExecContext(ctx, `DELETE FROM semantic_entries WHERE url_pattern = ?`, urlPattern)
	return err
}

// ---------------------------------------------------------------------------
// Helpers: URL pattern + DOM hash derivation
// ---------------------------------------------------------------------------

// urlPattern normalizes a URL into a reuse-friendly template.
// Examples:
//   https://example.com/users/42              → https://example.com/users/:id
//   https://example.com/posts/abc-slug        → https://example.com/posts/:slug
// Queries and fragments are stripped.
func urlPattern(u string) string {
	// Trim query + fragment
	if i := indexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	if i := indexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	// Find origin end (://host/)
	// Very cheap heuristic — no full URL parsing needed.
	schemeEnd := indexString(u, "://")
	pathStart := -1
	if schemeEnd >= 0 {
		pathStart = indexByteFrom(u, '/', schemeEnd+3)
	} else {
		pathStart = indexByte(u, '/')
	}
	if pathStart < 0 {
		return u
	}
	origin := u[:pathStart]
	path := u[pathStart:]
	return origin + paramizePath(path)
}

func paramizePath(p string) string {
	// split by '/'
	out := make([]byte, 0, len(p)+16)
	seg := ""
	flush := func() {
		if seg != "" {
			out = append(out, []byte(paramize(seg))...)
		}
		seg = ""
	}
	for _, ch := range []byte(p) {
		if ch == '/' {
			flush()
			out = append(out, '/')
			continue
		}
		seg += string(ch)
	}
	flush()
	return string(out)
}

func paramize(seg string) string {
	// numeric id
	if isAllDigits(seg) {
		return ":id"
	}
	// UUID
	if len(seg) == 36 && seg[8] == '-' && seg[13] == '-' && seg[18] == '-' && seg[23] == '-' {
		return ":uuid"
	}
	// 40-hex hash (git sha)
	if len(seg) == 40 && isHex(seg) {
		return ":hash"
	}
	// date YYYY-MM-DD
	if len(seg) == 10 && seg[4] == '-' && seg[7] == '-' {
		return ":date"
	}
	// slug heuristic: contains dash + letters, length > 5.
	// Digits optional (hello-world / goodbye-earth are slugs even without numbers).
	if len(seg) > 5 && containsByte(seg, '-') && hasLetter(seg) {
		return ":slug"
	}
	return seg
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func hasLetter(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func indexByteFrom(s string, b byte, start int) int {
	for i := start; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func indexString(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// domHash computes a stable 16-char hash of an element identity tuple.
// Used as cache key so minor DOM mutations don't thrash the cache.
func domHash(elements []brainElement) string {
	h := sha256.New()
	for _, el := range elements {
		fmt.Fprintf(h, "%s|%s|%s|%s|", el.Tag, el.Role, el.Type, el.Name)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// elementKey builds a stable per-element cache key.
func elementKey(el brainElement) string {
	// id is per-snapshot and not stable; use tag+role+name.
	key := el.Tag + "|" + el.Role + "|" + el.Type + "|" + el.Name
	if el.Href != "" {
		key += "|h:" + el.Href
	}
	// Clamp length for index.
	if len(key) > 200 {
		key = key[:200]
	}
	return key
}

// ensure json import stays alive for any future JSON helpers.
var _ = json.RawMessage{}
