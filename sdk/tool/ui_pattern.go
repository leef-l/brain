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
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// UI Pattern library — structured templates for common web interactions.
//
// See sdk/docs/40-Browser-Brain语义理解架构.md §3.3 (Phase 2).
//
// A UIPattern captures "how to do X on a page that looks like Y" as a
// deterministic recipe:
//   - AppliesWhen : cheap match conditions (selectors, URL pattern, required keywords)
//   - ElementRoles: logical role → ElementDescriptor (multi-signal for self-healing)
//   - ActionSequence: ordered tool invocations with placeholders
//   - PostConditions: hard checks after execution
//   - OnAnomaly: what to do if anomaly detected during execution
//
// Patterns are persisted in ~/.brain/ui_patterns.db (SQLite WAL) and cached
// in-memory for fast matching. Stats (hit / success / failure) are updated
// atomically after each execution.

// UIPattern is the core structure. JSON-serialized for storage.
type UIPattern struct {
	ID             string                       `json:"id"`
	Category       string                       `json:"category"` // auth / search / commerce / admin / form / nav
	Description    string                       `json:"description,omitempty"`
	AppliesWhen    MatchCondition               `json:"applies_when"`
	ElementRoles   map[string]ElementDescriptor `json:"element_roles"`
	ActionSequence []ActionStep                 `json:"action_sequence"`
	PostConditions []PostCondition              `json:"post_conditions,omitempty"`
	OnAnomaly      map[string]AnomalyHandler    `json:"on_anomaly,omitempty"`
	Stats          PatternStats                 `json:"stats"`
	Source         string                       `json:"source"` // "seed" / "learned" / "user"
	CreatedAt      time.Time                    `json:"created_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`

	// Enabled 控制模式是否对外生效。默认 true。
	// M3 自动停用:RecordExecution 检测到 FailureCount≥5 且 SuccessRate<0.3
	// 时会把 Enabled 置 false,PatternLibrary.List/Get 会把 Enabled=false
	// 的条目从对外返回中剔除,browser.pattern_match / pattern_exec 自然
	// 看不到。Ops 可用 SetEnabled(id, true) 手动恢复。
	//
	// 旧数据库 row 没有这个字段时,SQLite 列的 DEFAULT 1 保证加载后为 true;
	// JSON body 里缺失 enabled 字段时,load() 会以列值为准覆盖。
	Enabled bool `json:"enabled"`

	// Pending 标识模式处于"试用期"。P3.2 自分裂生成的变种默认 Pending=true:
	// 仍然可被匹配和执行(Enabled=true),但 ops 面板用它和正式模式区分展示,
	// 避免实验性模式的数据直接混进主统计。
	//
	// 毕业条件:PromoteVariants 扫描 SuccessCount >= 3 的 Pending 模式,
	// 把 Pending 清为 false。
	//
	// 旧数据库 row 没有这个字段时,SQLite 列的 DEFAULT 0 保证加载后为 false
	// (已入库的老模式视为正式模式)。
	Pending bool `json:"pending,omitempty"`
}

// MatchCondition — cheap pre-filter. All conditions AND together.
type MatchCondition struct {
	URLPattern    string   `json:"url_pattern,omitempty"` // regex or ":id"-style pattern
	SiteHost      string   `json:"site_host,omitempty"`   // exact host match, mainly for P3.2 site-specific variants
	Has           []string `json:"has,omitempty"`         // CSS selectors that must exist
	HasNot        []string `json:"has_not,omitempty"`     // selectors that must NOT exist
	TitleContains []string `json:"title_contains,omitempty"`
	TextContains  []string `json:"text_contains,omitempty"` // substrings anywhere in body
}

// ElementDescriptor — multi-signal self-healing locator (RPA Object
// Repository pattern). Matches are attempted in priority order:
//  1. Exact match on role+name
//  2. Role + name fuzzy match
//  3. CSS selector
//  4. XPath
//  5. (future) screenshot_hash
type ElementDescriptor struct {
	Role           string              `json:"role,omitempty"`
	Name           string              `json:"name,omitempty"`        // exact or regex (prefix "~" for regex)
	AnchorText     string              `json:"anchor_text,omitempty"` // text of a nearby landmark
	CSS            string              `json:"css,omitempty"`
	XPath          string              `json:"xpath,omitempty"`
	Tag            string              `json:"tag,omitempty"`             // input / button / a
	Type           string              `json:"type,omitempty"`            // input type: text/password/...
	ScreenshotHash string              `json:"screenshot_hash,omitempty"` // reserved; Stage 2 doesn't capture yet
	Fallback       []ElementDescriptor `json:"fallback,omitempty"`        // alternative descriptors to try
}

// ActionStep is one tool invocation in a sequence.
type ActionStep struct {
	Tool       string                 `json:"tool"`                  // "browser.click" / "browser.type" / ...
	TargetRole string                 `json:"target_role,omitempty"` // refers to ElementRoles key
	Params     map[string]interface{} `json:"params,omitempty"`      // literal params (placeholders "$credentials.email" resolved at runtime)
	WaitAfter  string                 `json:"wait_after,omitempty"`  // "network_idle" | "response:<url_pattern>" | ""
	TimeoutMS  int                    `json:"timeout_ms,omitempty"`
	Optional   bool                   `json:"optional,omitempty"` // skip on failure instead of aborting
}

// PostCondition validates the pattern's success after ActionSequence.
// All conditions AND together (success = all true).
type PostCondition struct {
	Type          string          `json:"type"` // url_changed / url_matches / dom_contains / cookie_set / response_ok / title_contains / any_of
	URLPattern    string          `json:"url_pattern,omitempty"`
	Selector      string          `json:"selector,omitempty"`
	CookieName    string          `json:"cookie_name,omitempty"`
	TitleContains string          `json:"title_contains,omitempty"`
	Any           []PostCondition `json:"any,omitempty"` // for type=any_of
}

// AnomalyHandler is the response to a detected anomaly type during pattern execution.
type AnomalyHandler struct {
	Action     string `json:"action"` // abort / retry / fallback_pattern / human_intervention
	FallbackID string `json:"fallback_id,omitempty"`
	MaxRetries int    `json:"max_retries,omitempty"`
	BackoffMS  int    `json:"backoff_ms,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// PatternStats tracks execution outcomes.
type PatternStats struct {
	HitCount      int       `json:"hit_count"`     // Times this pattern was considered
	MatchCount    int       `json:"match_count"`   // Times AppliesWhen passed
	SuccessCount  int       `json:"success_count"` // PostConditions all true
	FailureCount  int       `json:"failure_count"`
	LastHitAt     time.Time `json:"last_hit_at"`
	LastSuccessAt time.Time `json:"last_success_at"`
	AvgDurationMS float64   `json:"avg_duration_ms"`
}

// SuccessRate is a convenience accessor.
func (s *PatternStats) SuccessRate() float64 {
	total := s.SuccessCount + s.FailureCount
	if total == 0 {
		return 0.0
	}
	return float64(s.SuccessCount) / float64(total)
}

// ---------------------------------------------------------------------------
// Library: load/save/list/stats
// ---------------------------------------------------------------------------

const patternSchema = `
CREATE TABLE IF NOT EXISTS ui_patterns (
	id           TEXT PRIMARY KEY,
	category     TEXT NOT NULL DEFAULT '',
	source       TEXT NOT NULL DEFAULT '',
	body         BLOB NOT NULL,
	hit_count    INTEGER NOT NULL DEFAULT 0,
	match_count  INTEGER NOT NULL DEFAULT 0,
	success_count INTEGER NOT NULL DEFAULT 0,
	failure_count INTEGER NOT NULL DEFAULT 0,
	last_hit_at  TEXT NOT NULL DEFAULT '',
	last_success_at TEXT NOT NULL DEFAULT '',
	avg_duration_ms REAL NOT NULL DEFAULT 0,
	enabled      INTEGER NOT NULL DEFAULT 1,
	pending      INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ui_patterns_category ON ui_patterns(category);
CREATE INDEX IF NOT EXISTS idx_ui_patterns_source   ON ui_patterns(source);
`

// addColumnIfMissing 对 SQLite 执行一次 ALTER TABLE ADD COLUMN,
// 已有该列时(错误里含 "duplicate column")视为幂等成功。M3 的 enabled
// 列 / P3.2 的 pending 列都走这一条路径做在线升级。
func addColumnIfMissing(db *sql.DB, ddl string) error {
	_, err := db.Exec(ddl)
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		return err
	}
	return nil
}

// PatternLibrary is the SQLite-backed store + in-memory cache.
type PatternLibrary struct {
	db    *sql.DB
	dsn   string
	mu    sync.RWMutex
	cache map[string]*UIPattern // id -> pattern

	lastReloadCheck time.Time
	lastSyncToken   string
	reloadInterval  time.Duration
}

const envUIPatternDBPath = "BRAIN_UI_PATTERN_DB_PATH"

func defaultPatternDSN() string {
	if override := strings.TrimSpace(os.Getenv(envUIPatternDBPath)); override != "" {
		return filepath.Clean(override)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".brain", "ui_patterns.db")
}

// NewPatternLibrary opens (creating as needed) the SQLite store and loads
// all patterns into memory. If dsn is empty, defaults to ~/.brain/ui_patterns.db.
// On first use, seeds the library with built-in patterns.
func NewPatternLibrary(dsn string) (*PatternLibrary, error) {
	if dsn == "" {
		dsn = defaultPatternDSN()
	}
	if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir cache dir: %w", err)
	}
	db, err := sql.Open("sqlite", dsn+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(patternSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// M3:向前兼容旧 DB 的增量迁移,给历史 row 补上 enabled 列。
	if err := addColumnIfMissing(db, `ALTER TABLE ui_patterns ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate enabled column: %w", err)
	}
	// P3.2:自分裂变种引入 pending 列。DEFAULT 0 让历史模式默认"非试用",
	// SpawnVariant 显式置 1。
	if err := addColumnIfMissing(db, `ALTER TABLE ui_patterns ADD COLUMN pending INTEGER NOT NULL DEFAULT 0`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate pending column: %w", err)
	}

	lib := &PatternLibrary{
		db:             db,
		dsn:            dsn,
		cache:          make(map[string]*UIPattern),
		reloadInterval: time.Second,
	}
	if err := lib.load(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	// Seed on empty DB
	if len(lib.cache) == 0 {
		if err := lib.Seed(context.Background()); err != nil {
			db.Close()
			return nil, fmt.Errorf("seed: %w", err)
		}
	}
	return lib, nil
}

// Close releases DB handle.
func (lib *PatternLibrary) Close() error {
	if lib == nil || lib.db == nil {
		return nil
	}
	return lib.db.Close()
}

// load hydrates the in-memory cache from the DB.
func (lib *PatternLibrary) load(ctx context.Context) error {
	cache, err := lib.loadSnapshot(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	lib.mu.Lock()
	lib.cache = cache
	lib.lastSyncToken = patternCacheSyncToken(cache)
	lib.lastReloadCheck = now
	lib.mu.Unlock()
	return nil
}

func (lib *PatternLibrary) loadSnapshot(ctx context.Context) (map[string]*UIPattern, error) {
	rows, err := lib.db.QueryContext(ctx, `SELECT id, body, hit_count, match_count, success_count, failure_count, last_hit_at, last_success_at, avg_duration_ms, enabled, pending FROM ui_patterns`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cache := make(map[string]*UIPattern)
	for rows.Next() {
		var id string
		var body []byte
		var hit, match, succ, fail int
		var lastHit, lastSucc string
		var avgDur float64
		var enabled, pending int
		if err := rows.Scan(&id, &body, &hit, &match, &succ, &fail, &lastHit, &lastSucc, &avgDur, &enabled, &pending); err != nil {
			return nil, err
		}
		var p UIPattern
		if err := json.Unmarshal(body, &p); err != nil {
			continue // skip corrupt rows, don't halt startup
		}
		p.Stats.HitCount = hit
		p.Stats.MatchCount = match
		p.Stats.SuccessCount = succ
		p.Stats.FailureCount = fail
		p.Stats.AvgDurationMS = avgDur
		p.Stats.LastHitAt, _ = time.Parse(time.RFC3339Nano, lastHit)
		p.Stats.LastSuccessAt, _ = time.Parse(time.RFC3339Nano, lastSucc)
		// 列是权威:旧 body 里没 enabled 字段(JSON 默认 false)时,
		// 列 DEFAULT 1 保证加载后仍为 true。Pending 同理,列 DEFAULT 0。
		p.Enabled = enabled != 0
		p.Pending = pending != 0
		cache[p.ID] = &p
	}
	return cache, rows.Err()
}

// Reload refreshes the in-memory cache from SQLite so long-running sidecars
// can observe patterns inserted or updated by other processes.
func (lib *PatternLibrary) Reload(ctx context.Context) error {
	if lib == nil {
		return nil
	}
	cache, err := lib.loadSnapshot(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	lib.mu.Lock()
	lib.cache = cache
	lib.lastSyncToken = patternCacheSyncToken(cache)
	lib.lastReloadCheck = now
	lib.mu.Unlock()
	return nil
}

func patternCacheSyncToken(cache map[string]*UIPattern) string {
	maxUpdated := ""
	for _, p := range cache {
		if p == nil {
			continue
		}
		updated := formatTime(p.UpdatedAt)
		if updated > maxUpdated {
			maxUpdated = updated
		}
	}
	return fmt.Sprintf("%d|%s", len(cache), maxUpdated)
}

func (lib *PatternLibrary) storageSyncToken(ctx context.Context) (string, error) {
	if lib == nil || lib.db == nil {
		return "", nil
	}
	var count int
	var maxUpdated string
	if err := lib.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(updated_at), '') FROM ui_patterns`).Scan(&count, &maxUpdated); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d|%s", count, maxUpdated), nil
}

// ReloadIfChanged refreshes the in-memory cache only when the backing SQLite
// rows have changed since the last observed sync token.
func (lib *PatternLibrary) ReloadIfChanged(ctx context.Context) error {
	if lib == nil {
		return nil
	}
	lib.mu.RLock()
	interval := lib.reloadInterval
	lastCheck := lib.lastReloadCheck
	lib.mu.RUnlock()
	now := time.Now().UTC()
	if interval > 0 && !lastCheck.IsZero() && now.Sub(lastCheck) < interval {
		return nil
	}

	token, err := lib.storageSyncToken(ctx)
	if err != nil {
		return err
	}

	lib.mu.Lock()
	if interval > 0 && !lib.lastReloadCheck.IsZero() && now.Sub(lib.lastReloadCheck) < interval {
		lib.mu.Unlock()
		return nil
	}
	if token == lib.lastSyncToken {
		lib.lastReloadCheck = now
		lib.mu.Unlock()
		return nil
	}
	lib.mu.Unlock()
	return lib.Reload(ctx)
}

// List returns all enabled patterns, optionally filtered by category.
// Patterns disabled by the auto-disable rule or by SetEnabled(false) are
// skipped — pattern_match / pattern_exec 因此看不到它们。
// 需要看全部(含禁用)用 ListAll。
func (lib *PatternLibrary) List(category string) []*UIPattern {
	_ = lib.ReloadIfChanged(context.Background())
	return lib.listFiltered(category, false)
}

// ListAll 返回全部模式(含禁用),用于 ops 管理面板 / 调试。
func (lib *PatternLibrary) ListAll(category string) []*UIPattern {
	_ = lib.ReloadIfChanged(context.Background())
	return lib.listFiltered(category, true)
}

func (lib *PatternLibrary) listFiltered(category string, includeDisabled bool) []*UIPattern {
	lib.mu.RLock()
	defer lib.mu.RUnlock()
	out := make([]*UIPattern, 0, len(lib.cache))
	for _, p := range lib.cache {
		if category != "" && p.Category != category {
			continue
		}
		if !includeDisabled && !p.Enabled {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		ri := out[i].Stats.SuccessRate()
		rj := out[j].Stats.SuccessRate()
		if ri != rj {
			return ri > rj
		}
		return out[i].Stats.MatchCount > out[j].Stats.MatchCount
	})
	return out
}

// Get returns one enabled pattern by ID, nil if not found or disabled.
// 用 GetAny(id) 可拿到含禁用的(ops 用)。
func (lib *PatternLibrary) Get(id string) *UIPattern {
	_ = lib.ReloadIfChanged(context.Background())
	lib.mu.RLock()
	defer lib.mu.RUnlock()
	p, ok := lib.cache[id]
	if !ok || !p.Enabled {
		return nil
	}
	cp := *p
	return &cp
}

// GetAny 返回包括禁用在内的模式(ops / SetEnabled 用)。
func (lib *PatternLibrary) GetAny(id string) *UIPattern {
	_ = lib.ReloadIfChanged(context.Background())
	lib.mu.RLock()
	defer lib.mu.RUnlock()
	p, ok := lib.cache[id]
	if !ok {
		return nil
	}
	cp := *p
	return &cp
}

// Upsert persists a new or updated pattern.
// 新 pattern(cache 里不存在、且 Enabled 零值)默认置 Enabled = true,
// 这样老代码路径(seedPatterns / LearnFromSequences)无需改字段构造也
// 能得到正确语义。显式 p.Enabled = false 的调用会被尊重。
func (lib *PatternLibrary) Upsert(ctx context.Context, p *UIPattern) error {
	if p == nil || p.ID == "" {
		return fmt.Errorf("pattern id required")
	}
	lib.mu.Lock()
	defer lib.mu.Unlock()

	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	// 新 pattern 默认 enabled;已存在则保留调用方传入的值(允许显式禁用)。
	if _, existed := lib.cache[p.ID]; !existed && !p.Enabled {
		p.Enabled = true
	}

	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = lib.db.ExecContext(ctx, `
INSERT INTO ui_patterns(id, category, source, body, hit_count, match_count, success_count, failure_count, last_hit_at, last_success_at, avg_duration_ms, enabled, pending, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	category = excluded.category,
	source = excluded.source,
	body = excluded.body,
	enabled = excluded.enabled,
	pending = excluded.pending,
	updated_at = excluded.updated_at
`,
		p.ID, p.Category, p.Source, body,
		p.Stats.HitCount, p.Stats.MatchCount, p.Stats.SuccessCount, p.Stats.FailureCount,
		formatTime(p.Stats.LastHitAt), formatTime(p.Stats.LastSuccessAt), p.Stats.AvgDurationMS,
		boolToInt(p.Enabled), boolToInt(p.Pending),
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if err != nil {
		return err
	}
	lib.cache[p.ID] = p
	lib.lastSyncToken = patternCacheSyncToken(lib.cache)
	lib.lastReloadCheck = now
	bumpPatternIndex() // P3.4: pattern set changed → invalidate URL / category index
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RecordExecution updates stats for a pattern after an execution attempt.
// M3:FailureCount ≥ 5 且 SuccessRate < 0.3 时自动把 pattern 置 disabled,
// 避免低质量模式持续占据匹配排名。Ops 可用 SetEnabled 手动重置。
func (lib *PatternLibrary) RecordExecution(ctx context.Context, id string, success bool, durationMS int64) error {
	lib.mu.Lock()
	defer lib.mu.Unlock()

	p, ok := lib.cache[id]
	if !ok {
		return fmt.Errorf("pattern %q not found", id)
	}
	now := time.Now().UTC()
	p.Stats.HitCount++
	p.Stats.MatchCount++
	p.Stats.LastHitAt = now
	// EWMA avg duration (alpha=0.3)
	if p.Stats.AvgDurationMS == 0 {
		p.Stats.AvgDurationMS = float64(durationMS)
	} else {
		p.Stats.AvgDurationMS = 0.3*float64(durationMS) + 0.7*p.Stats.AvgDurationMS
	}
	if success {
		p.Stats.SuccessCount++
		p.Stats.LastSuccessAt = now
	} else {
		p.Stats.FailureCount++
	}

	// M3 自动停用判定:只在当前启用的模式上触发,避免反复 downgrade 日志。
	flipped := false
	if p.Enabled && p.Stats.FailureCount >= 5 && p.Stats.SuccessRate() < 0.3 {
		p.Enabled = false
		flipped = true
	}

	// P3.2 自毕业:Pending 变种累计 >= 3 次成功,清 Pending。阈值来自
	// 任务描述 §3 "累计 ≥ 3 次成功再取消 Pending"。Pending 翻转不影响
	// 匹配候选集合(Enabled 不变),所以不 bump index。
	if p.Pending && p.Stats.SuccessCount >= 3 {
		p.Pending = false
	}

	_, err := lib.db.ExecContext(ctx, `UPDATE ui_patterns SET
		hit_count = ?, match_count = ?, success_count = ?, failure_count = ?,
		last_hit_at = ?, last_success_at = ?, avg_duration_ms = ?, enabled = ?, pending = ?, updated_at = ?
	WHERE id = ?`,
		p.Stats.HitCount, p.Stats.MatchCount, p.Stats.SuccessCount, p.Stats.FailureCount,
		formatTime(p.Stats.LastHitAt), formatTime(p.Stats.LastSuccessAt),
		p.Stats.AvgDurationMS, boolToInt(p.Enabled), boolToInt(p.Pending), formatTime(now), id)
	// 仅当 Enabled 翻转时重建索引:纯 stats 递增不影响候选集合。
	lib.lastSyncToken = patternCacheSyncToken(lib.cache)
	lib.lastReloadCheck = now
	if flipped {
		bumpPatternIndex()
	}
	return err
}

// SetEnabled 手动开启或停用一个 pattern(ops 命令 / dashboard 用)。
// id 不存在时返回错误;值未变化也会触发一次持久化(幂等)。
func (lib *PatternLibrary) SetEnabled(ctx context.Context, id string, enabled bool) error {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	p, ok := lib.cache[id]
	if !ok {
		return fmt.Errorf("pattern %q not found", id)
	}
	p.Enabled = enabled
	now := time.Now().UTC()
	p.UpdatedAt = now
	_, err := lib.db.ExecContext(ctx, `UPDATE ui_patterns SET enabled = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), formatTime(now), id)
	if err == nil {
		lib.lastSyncToken = patternCacheSyncToken(lib.cache)
		lib.lastReloadCheck = now
		bumpPatternIndex()
	}
	return err
}

// Delete removes a pattern.
func (lib *PatternLibrary) Delete(ctx context.Context, id string) error {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	if _, err := lib.db.ExecContext(ctx, `DELETE FROM ui_patterns WHERE id = ?`, id); err != nil {
		return err
	}
	delete(lib.cache, id)
	lib.lastSyncToken = patternCacheSyncToken(lib.cache)
	lib.lastReloadCheck = time.Now().UTC()
	bumpPatternIndex()
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ---------------------------------------------------------------------------
// Seed patterns — 10 built-ins covering common flows
// ---------------------------------------------------------------------------

// Seed writes the built-in patterns into an empty library.
func (lib *PatternLibrary) Seed(ctx context.Context) error {
	patterns := seedPatterns()
	for _, p := range patterns {
		if err := lib.Upsert(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func seedPatterns() []*UIPattern {
	base := []*UIPattern{
		{
			ID:          "login_username_password",
			Category:    "auth",
			Source:      "seed",
			Description: "Standard username/email + password login form",
			AppliesWhen: MatchCondition{
				URLPattern: `(?i)/(login|signin|sign-in|auth|account/login)\b`,
				Has:        []string{`input[type="password"]`},
			},
			ElementRoles: map[string]ElementDescriptor{
				"email_field": {
					Tag:  "input",
					Name: "~(?i)(email|user|account|用户名|邮箱)",
					CSS:  `input[type="email"], input[name*="email" i], input[name*="user" i], input[type="text"]`,
				},
				"password_field": {
					Tag: "input", Type: "password",
					CSS: `input[type="password"]`,
				},
				"submit_button": {
					Role: "button",
					Name: "~(?i)(sign\\s*in|log\\s*in|login|submit|登录|登陆)",
					CSS:  `button[type="submit"], input[type="submit"]`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.type", TargetRole: "email_field", Params: map[string]interface{}{"text": "$credentials.email", "clear": true}},
				{Tool: "browser.type", TargetRole: "password_field", Params: map[string]interface{}{"text": "$credentials.password", "clear": true}},
				{Tool: "browser.click", TargetRole: "submit_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 8000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "url_changed"},
					{Type: "dom_contains", Selector: `[data-user-profile], .user-menu, .logout, a[href*="logout"]`},
					{Type: "cookie_set", CookieName: "session"},
				}},
			},
			OnAnomaly: map[string]AnomalyHandler{
				"error_message":   {Action: "abort", Reason: "Wrong credentials — do not retry"},
				"captcha":         {Action: "human_intervention"},
				"session_expired": {Action: "retry", MaxRetries: 1, BackoffMS: 500},
			},
		},
		{
			ID:          "search_query",
			Category:    "search",
			Source:      "seed",
			Description: "Enter a query in a search box and submit",
			AppliesWhen: MatchCondition{
				Has: []string{`input[type="search"], input[name*="search" i], input[name="q"], input[placeholder*="search" i]`},
			},
			ElementRoles: map[string]ElementDescriptor{
				"search_input": {
					Tag: "input",
					CSS: `input[type="search"], input[name="q"], input[name*="search" i]`,
				},
				"search_button": {
					Role: "button",
					Name: "~(?i)(search|搜索|查询)",
					CSS:  `button[type="submit"], button[aria-label*="search" i]`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.type", TargetRole: "search_input", Params: map[string]interface{}{"text": "$query", "clear": true}},
				{Tool: "browser.press_key", Params: map[string]interface{}{"key": "Enter"}, Optional: true},
				{Tool: "browser.click", TargetRole: "search_button", Optional: true},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "url_matches", URLPattern: `[?&]q=|search`},
					{Type: "title_contains", TitleContains: "search"},
				}},
			},
		},
		{
			ID:          "add_to_cart",
			Category:    "commerce",
			Source:      "seed",
			Description: "Add current product to cart",
			AppliesWhen: MatchCondition{
				Has:          []string{`[class*="product" i], [itemtype*="Product" i]`},
				TextContains: []string{"add to cart", "加入购物车", "add to bag"},
			},
			ElementRoles: map[string]ElementDescriptor{
				"add_button": {
					Role: "button",
					Name: "~(?i)(add\\s*to\\s*(cart|bag|basket)|加入购物车|立即购买)",
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "add_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "dom_contains", Selector: `[class*="cart-count"], [data-cart-count], .mini-cart, .cart-drawer`},
					{Type: "response_ok", URLPattern: `/cart|/add`},
				}},
			},
			OnAnomaly: map[string]AnomalyHandler{
				"error_message": {Action: "abort", Reason: "Likely out-of-stock or validation error"},
			},
		},
		{
			ID:          "close_modal",
			Category:    "nav",
			Source:      "seed",
			Description: "Dismiss a dialog/modal by clicking its close button",
			AppliesWhen: MatchCondition{
				Has: []string{`[role="dialog"], [role="alertdialog"], .modal.show, .modal.open`},
			},
			ElementRoles: map[string]ElementDescriptor{
				"close_button": {
					Role: "button",
					Name: "~(?i)(close|dismiss|×|取消|关闭)",
					CSS:  `[aria-label*="close" i], [aria-label*="dismiss" i], .modal-close, .close, button.btn-close`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "close_button"},
			},
			PostConditions: []PostCondition{
				{Type: "dom_contains", Selector: "body"}, // weak: just check not crashed
			},
		},
		{
			ID:          "accept_cookies",
			Category:    "nav",
			Source:      "seed",
			Description: "Accept cookie consent banner",
			AppliesWhen: MatchCondition{
				TextContains: []string{"cookie", "Cookie"},
				Has:          []string{`[role="dialog"], [class*="cookie" i], [id*="cookie" i], [class*="consent" i]`},
			},
			ElementRoles: map[string]ElementDescriptor{
				"accept_button": {
					Role: "button",
					Name: "~(?i)(accept|agree|allow|ok|got it|同意|接受|确定)",
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "accept_button"},
			},
			PostConditions: []PostCondition{
				{Type: "dom_contains", Selector: "body"},
			},
		},
		{
			ID:          "pagination_next",
			Category:    "nav",
			Source:      "seed",
			Description: "Advance to next page of results",
			AppliesWhen: MatchCondition{
				Has: []string{`a[rel="next"], .pagination, .pager, nav[aria-label*="pag" i]`},
			},
			ElementRoles: map[string]ElementDescriptor{
				"next_button": {
					Name: "~(?i)^(next|下一页|»|>)$",
					CSS:  `a[rel="next"], .pagination .next, .pager-next`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "next_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "url_changed"},
			},
		},
		{
			ID:          "filter_by_facet",
			Category:    "search",
			Source:      "seed",
			Description: "Toggle a faceted search checkbox (brand/category/price)",
			AppliesWhen: MatchCondition{
				Has: []string{`input[type="checkbox"]`, `[class*="facet" i], [class*="filter" i]`},
			},
			ElementRoles: map[string]ElementDescriptor{
				"facet_checkbox": {
					Tag: "input", Type: "checkbox",
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "facet_checkbox"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "url_changed", URLPattern: `[?&]`},
			},
		},
		{
			ID:          "submit_generic_form",
			Category:    "form",
			Source:      "seed",
			Description: "Submit a form after filling any required fields",
			AppliesWhen: MatchCondition{
				Has:    []string{`form`, `button[type="submit"], input[type="submit"]`},
				HasNot: []string{`input[type="password"]`}, // distinct from login
			},
			ElementRoles: map[string]ElementDescriptor{
				"submit_button": {
					Role: "button",
					Name: "~(?i)(submit|send|proceed|continue|confirm|提交|继续)",
					CSS:  `button[type="submit"], input[type="submit"]`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "submit_button"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "url_changed"},
					{Type: "dom_contains", Selector: `[role="alert"], .success, .alert-success`},
				}},
			},
		},
		{
			ID:          "logout",
			Category:    "auth",
			Source:      "seed",
			Description: "Log out of current session",
			AppliesWhen: MatchCondition{
				Has: []string{`a[href*="logout" i], button[aria-label*="logout" i]`},
			},
			ElementRoles: map[string]ElementDescriptor{
				"logout_link": {
					Name: "~(?i)(log\\s*out|sign\\s*out|注销|退出)",
					CSS:  `a[href*="logout" i], button[aria-label*="logout" i]`,
				},
			},
			ActionSequence: []ActionStep{
				{Tool: "browser.click", TargetRole: "logout_link"},
				{Tool: "wait.network_idle", Params: map[string]interface{}{"timeout_ms": 5000}},
			},
			PostConditions: []PostCondition{
				{Type: "any_of", Any: []PostCondition{
					{Type: "url_matches", URLPattern: `(?i)/(login|home|\$)$`},
					{Type: "dom_contains", Selector: `input[type="password"]`},
				}},
			},
		},
		{
			ID:          "skip_login_already_authed",
			Category:    "auth",
			Source:      "seed",
			Description: "Recognize that a session is already active and skip the login step",
			AppliesWhen: MatchCondition{
				Has:    []string{`[data-user-profile], .user-menu, a[href*="logout" i]`},
				HasNot: []string{`input[type="password"]`},
			},
			ElementRoles:   map[string]ElementDescriptor{},
			ActionSequence: []ActionStep{},
			PostConditions: []PostCondition{
				{Type: "dom_contains", Selector: `[data-user-profile], .user-menu, a[href*="logout" i]`},
			},
		},
	}
	// Scenario-pack extension point: other seed files (ui_patterns_auth.go,
	// ui_patterns_commerce.go, ...) register providers via init() — see
	// ui_patterns_auth.go:extraSeedProviders. Keeps scenario packs self-
	// contained without each modifying this file.
	for _, p := range extraSeedProviders {
		base = append(base, p()...)
	}
	return base
}

// ---------------------------------------------------------------------------
// Hash helpers used by the learning layer
// ---------------------------------------------------------------------------

// goalHash is a stable short hash used to key learned patterns.
func goalHash(goal string) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(goal))))
	return hex.EncodeToString(h.Sum(nil))[:12]
}
