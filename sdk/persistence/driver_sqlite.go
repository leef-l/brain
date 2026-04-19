// Package persistence — driver_sqlite.go implements the SQLite WAL persistence
// driver using modernc.org/sqlite (pure Go, no CGo).
//
// The SQLite driver provides a production-grade standalone backend that sits
// between the lightweight "file" driver (JSON full-rewrite) and the cluster-tier
// MySQL/PostgreSQL drivers. It uses WAL mode for concurrent readers and
// serialised writers, matching the Brain SDK's single-process deployment model.
//
// DSN is the path to the database file. If empty, defaults to ~/.brain/brain.db.
// The directory is created automatically if it does not exist.
package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/sdk/errors"

	// Pure-Go SQLite driver — no CGo dependency.
	_ "modernc.org/sqlite"
)

// ── Schema DDL ──────────────────────────────────────────────────────────

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS plans (
	id            INTEGER PRIMARY KEY,
	run_id        INTEGER NOT NULL DEFAULT 0,
	brain_id      TEXT    NOT NULL DEFAULT '',
	version       INTEGER NOT NULL DEFAULT 1,
	current_state BLOB,
	archived      INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT    NOT NULL,
	updated_at    TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS plan_deltas (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	plan_id    INTEGER NOT NULL,
	version    INTEGER NOT NULL,
	op_type    TEXT    NOT NULL DEFAULT '',
	payload    BLOB,
	actor      TEXT    NOT NULL DEFAULT '',
	created_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS checkpoints (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id          INTEGER NOT NULL UNIQUE,
	brain_id        TEXT    NOT NULL DEFAULT '',
	state           TEXT    NOT NULL DEFAULT '',
	turn_index      INTEGER NOT NULL DEFAULT 0,
	turn_uuid       TEXT    NOT NULL DEFAULT '',
	messages_ref    TEXT    NOT NULL DEFAULT '',
	system_ref      TEXT    NOT NULL DEFAULT '',
	tools_ref       TEXT    NOT NULL DEFAULT '',
	cost_snapshot   BLOB,
	token_snapshot  BLOB,
	budget_remain   BLOB,
	trace_parent    TEXT    NOT NULL DEFAULT '',
	resume_attempts INTEGER NOT NULL DEFAULT 0,
	updated_at      TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS usage_records (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id          INTEGER NOT NULL,
	turn_index      INTEGER NOT NULL DEFAULT 0,
	provider        TEXT    NOT NULL DEFAULT '',
	model           TEXT    NOT NULL DEFAULT '',
	input_tokens    INTEGER NOT NULL DEFAULT 0,
	output_tokens   INTEGER NOT NULL DEFAULT 0,
	cache_read      INTEGER NOT NULL DEFAULT 0,
	cache_creation  INTEGER NOT NULL DEFAULT 0,
	cost_usd        REAL    NOT NULL DEFAULT 0,
	idempotency_key TEXT    NOT NULL DEFAULT '',
	created_at      TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_idempotency ON usage_records(idempotency_key);

CREATE TABLE IF NOT EXISTS artifact_meta (
	ref        TEXT PRIMARY KEY,
	mime_type  TEXT    NOT NULL DEFAULT '',
	size_bytes INTEGER NOT NULL DEFAULT 0,
	run_id     INTEGER,
	turn_index INTEGER,
	caption    TEXT    NOT NULL DEFAULT '',
	ref_count  INTEGER NOT NULL DEFAULT 0,
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	run_key    TEXT    NOT NULL UNIQUE,
	brain_id   TEXT    NOT NULL DEFAULT '',
	prompt     TEXT    NOT NULL DEFAULT '',
	status     TEXT    NOT NULL DEFAULT 'running',
	mode       TEXT    NOT NULL DEFAULT '',
	workdir    TEXT    NOT NULL DEFAULT '',
	turn_uuid  TEXT    NOT NULL DEFAULT '',
	plan_id    INTEGER NOT NULL DEFAULT 0,
	result     BLOB,
	error      TEXT    NOT NULL DEFAULT '',
	created_at TEXT    NOT NULL,
	updated_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status);

CREATE TABLE IF NOT EXISTS run_events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id     INTEGER NOT NULL,
	event_type TEXT    NOT NULL DEFAULT '',
	message    TEXT    NOT NULL DEFAULT '',
	data       BLOB,
	created_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_run_events_run_id ON run_events(run_id);

CREATE TABLE IF NOT EXISTS audit_logs (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id     TEXT    NOT NULL UNIQUE,
	execution_id TEXT    NOT NULL DEFAULT '',
	event_type   TEXT    NOT NULL DEFAULT '',
	actor        TEXT    NOT NULL DEFAULT '',
	timestamp    TEXT    NOT NULL,
	data         BLOB,
	status_code  TEXT    NOT NULL DEFAULT '',
	details      TEXT    NOT NULL DEFAULT '',
	created_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_execution ON audit_logs(execution_id);
CREATE INDEX IF NOT EXISTS idx_audit_type ON audit_logs(event_type);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp DESC);

CREATE TABLE IF NOT EXISTS learning_profiles (
	brain_kind TEXT PRIMARY KEY,
	cold_start INTEGER NOT NULL DEFAULT 1,
	updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS learning_task_scores (
	brain_kind      TEXT    NOT NULL,
	task_type       TEXT    NOT NULL,
	sample_count    INTEGER NOT NULL DEFAULT 0,
	accuracy_value  REAL    NOT NULL DEFAULT 0,
	accuracy_alpha  REAL    NOT NULL DEFAULT 0.2,
	speed_value     REAL    NOT NULL DEFAULT 0,
	speed_alpha     REAL    NOT NULL DEFAULT 0.2,
	cost_value      REAL    NOT NULL DEFAULT 0,
	cost_alpha      REAL    NOT NULL DEFAULT 0.2,
	stability_value REAL    NOT NULL DEFAULT 0,
	stability_alpha REAL    NOT NULL DEFAULT 0.2,
	PRIMARY KEY (brain_kind, task_type)
);

CREATE TABLE IF NOT EXISTS learning_sequences (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	sequence_id TEXT    NOT NULL DEFAULT '',
	total_score REAL    NOT NULL DEFAULT 0,
	recorded_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS learning_seq_steps (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	sequence_id INTEGER NOT NULL,
	brain_kind  TEXT    NOT NULL DEFAULT '',
	task_type   TEXT    NOT NULL DEFAULT '',
	duration_ms INTEGER NOT NULL DEFAULT 0,
	score       REAL    NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_seq_steps_seq ON learning_seq_steps(sequence_id);

CREATE TABLE IF NOT EXISTS learning_preferences (
	category   TEXT PRIMARY KEY,
	value      TEXT    NOT NULL DEFAULT '',
	weight     REAL    NOT NULL DEFAULT 0,
	updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS shared_messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	from_brain TEXT    NOT NULL DEFAULT '',
	to_brain   TEXT    NOT NULL DEFAULT '',
	messages   BLOB,
	count      INTEGER NOT NULL DEFAULT 0,
	created_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_shared_messages_brains ON shared_messages(from_brain, to_brain);
`

// ── sqliteDriver ────────────────────────────────────────────────────────

type sqliteDriver struct{}

func (sqliteDriver) Open(dsn string) (*Stores, error) {
	if dsn == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("sqlite driver: cannot determine home dir: %w", err)
		}
		dir := filepath.Join(home, ".brain")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("sqlite driver: mkdir ~/.brain: %w", err)
		}
		dsn = filepath.Join(dir, "brain.db")
	}

	// Ensure parent directory exists.
	if dir := filepath.Dir(dsn); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("sqlite driver: mkdir %s: %w", dir, err)
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite driver: open: %w", err)
	}

	// WAL mode for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite driver: WAL pragma: %w", err)
	}
	// Recommended pragmas for performance.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite driver: busy_timeout pragma: %w", err)
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite driver: synchronous pragma: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite driver: foreign_keys pragma: %w", err)
	}

	// Create schema.
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite driver: create schema: %w", err)
	}

	core := &sqliteCore{db: db}
	metaStore := &sqliteMetaStore{c: core}
	planStore := &sqlitePlanStore{c: core}
	checkpointStore := &sqliteCheckpointStore{c: core}
	usageLedger := &sqliteUsageLedger{c: core}
	runStore := &sqliteRunStore{c: core}
	auditLogger := &sqliteAuditLogger{c: core}
	learningStore := &sqliteLearningStore{c: core}
	sharedMsgStore := &sqliteSharedMessageStore{c: core}

	// ArtifactStore uses filesystem CAS alongside SQLite metadata.
	artifactDir := filepath.Join(filepath.Dir(dsn), "artifacts")
	if err := os.MkdirAll(artifactDir, 0700); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite driver: mkdir artifacts: %w", err)
	}
	artifactStore := NewFSArtifactStore(artifactDir, metaStore, nil)
	resume := NewMemResumeCoordinator(checkpointStore)

	return &Stores{
		PlanStore:          planStore,
		ArtifactStore:      artifactStore,
		ArtifactMeta:       metaStore,
		RunCheckpointStore: checkpointStore,
		UsageLedger:        usageLedger,
		ResumeCoordinator:  resume,
		RunStore:           runStore,
		AuditLogger:        auditLogger,
		LearningStore:      learningStore,
		SharedMessageStore: sharedMsgStore,
		CloseFunc:          db.Close,
	}, nil
}

func init() {
	Register("sqlite", sqliteDriver{})
}

// ── sqliteCore ──────────────────────────────────────────────────────────
//
// sqliteCore holds the shared *sql.DB and write mutex. The individual
// interface wrappers (sqlitePlanStore, sqliteCheckpointStore, etc.) embed
// a pointer to the core so they share the same database connection and
// serialisation lock.

type sqliteCore struct {
	db *sql.DB
	// mu serialises write operations. SQLite WAL allows concurrent reads
	// but only one writer; the mutex prevents "database is locked" errors
	// when multiple goroutines attempt writes.
	mu sync.Mutex
}

const sqliteTimeLayout = time.RFC3339Nano

// ── sqlitePlanStore — implements PlanStore ──────────────────────────────

type sqlitePlanStore struct{ c *sqliteCore }

func (s *sqlitePlanStore) Create(ctx context.Context, plan *BrainPlan) (int64, error) {
	if plan == nil {
		return 0, brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqlitePlanStore.Create: plan is nil"),
		)
	}

	now := time.Now().UTC()
	state := plan.CurrentState
	if len(state) == 0 {
		state = json.RawMessage("{}")
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	var id int64
	if plan.ID != 0 {
		_, err := s.c.db.ExecContext(ctx,
			`INSERT INTO plans (id, run_id, brain_id, version, current_state, archived, created_at, updated_at)
			 VALUES (?, ?, ?, 1, ?, 0, ?, ?)`,
			plan.ID, plan.RunID, plan.BrainID, []byte(state),
			now.Format(sqliteTimeLayout), now.Format(sqliteTimeLayout),
		)
		if err != nil {
			return 0, fmt.Errorf("sqlitePlanStore.Create: %w", err)
		}
		id = plan.ID
	} else {
		res, err := s.c.db.ExecContext(ctx,
			`INSERT INTO plans (run_id, brain_id, version, current_state, archived, created_at, updated_at)
			 VALUES (?, ?, 1, ?, 0, ?, ?)`,
			plan.RunID, plan.BrainID, []byte(state),
			now.Format(sqliteTimeLayout), now.Format(sqliteTimeLayout),
		)
		if err != nil {
			return 0, fmt.Errorf("sqlitePlanStore.Create: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, fmt.Errorf("sqlitePlanStore.Create: lastInsertId: %w", err)
		}
	}
	return id, nil
}

func (s *sqlitePlanStore) Get(ctx context.Context, id int64) (*BrainPlan, error) {
	row := s.c.db.QueryRowContext(ctx,
		`SELECT id, run_id, brain_id, version, current_state, archived, created_at, updated_at
		 FROM plans WHERE id = ?`, id)

	p := &BrainPlan{}
	var archivedInt int
	var createdStr, updatedStr string
	var stateBytes []byte

	err := row.Scan(&p.ID, &p.RunID, &p.BrainID, &p.Version, &stateBytes,
		&archivedInt, &createdStr, &updatedStr)
	if err == sql.ErrNoRows {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqlitePlanStore.Get: plan id %d not found", id)),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitePlanStore.Get: %w", err)
	}

	p.Archived = archivedInt != 0
	p.CurrentState = json.RawMessage(stateBytes)
	p.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdStr)
	p.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updatedStr)
	return p, nil
}

func (s *sqlitePlanStore) Update(ctx context.Context, id int64, delta *BrainPlanDelta) error {
	if delta == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqlitePlanStore.Update: delta is nil"),
		)
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	tx, err := s.c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlitePlanStore.Update: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Read current plan inside the transaction.
	var version int64
	var archivedInt int
	err = tx.QueryRowContext(ctx,
		`SELECT version, archived FROM plans WHERE id = ?`, id,
	).Scan(&version, &archivedInt)
	if err == sql.ErrNoRows {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqlitePlanStore.Update: plan id %d not found", id)),
		)
	}
	if err != nil {
		return fmt.Errorf("sqlitePlanStore.Update: read plan: %w", err)
	}

	if archivedInt != 0 {
		return brainerrors.New(brainerrors.CodeWorkflowPrecondition,
			brainerrors.WithMessage(fmt.Sprintf("sqlitePlanStore.Update: plan %d is archived", id)),
		)
	}

	wantVersion := version + 1
	if delta.Version != wantVersion {
		return brainerrors.New(brainerrors.CodeDBDeadlock,
			brainerrors.WithMessage(fmt.Sprintf(
				"sqlitePlanStore.Update: optimistic-lock mismatch plan=%d have=%d delta=%d",
				id, version, delta.Version,
			)),
		)
	}

	now := time.Now().UTC()

	// Insert delta.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO plan_deltas (plan_id, version, op_type, payload, actor, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, wantVersion, delta.OpType, []byte(delta.Payload), delta.Actor,
		now.Format(sqliteTimeLayout),
	)
	if err != nil {
		return fmt.Errorf("sqlitePlanStore.Update: insert delta: %w", err)
	}

	// Update snapshot.
	if len(delta.Payload) > 0 {
		_, err = tx.ExecContext(ctx,
			`UPDATE plans SET version = ?, current_state = ?, updated_at = ? WHERE id = ?`,
			wantVersion, []byte(delta.Payload), now.Format(sqliteTimeLayout), id,
		)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE plans SET version = ?, updated_at = ? WHERE id = ?`,
			wantVersion, now.Format(sqliteTimeLayout), id,
		)
	}
	if err != nil {
		return fmt.Errorf("sqlitePlanStore.Update: update plan: %w", err)
	}

	return tx.Commit()
}

func (s *sqlitePlanStore) ListByRun(ctx context.Context, runID int64) ([]*BrainPlan, error) {
	rows, err := s.c.db.QueryContext(ctx,
		`SELECT id, run_id, brain_id, version, current_state, archived, created_at, updated_at
		 FROM plans WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, fmt.Errorf("sqlitePlanStore.ListByRun: %w", err)
	}
	defer rows.Close()

	var out []*BrainPlan
	for rows.Next() {
		p := &BrainPlan{}
		var archivedInt int
		var createdStr, updatedStr string
		var stateBytes []byte
		if err := rows.Scan(&p.ID, &p.RunID, &p.BrainID, &p.Version, &stateBytes,
			&archivedInt, &createdStr, &updatedStr); err != nil {
			return nil, fmt.Errorf("sqlitePlanStore.ListByRun: scan: %w", err)
		}
		p.Archived = archivedInt != 0
		p.CurrentState = json.RawMessage(stateBytes)
		p.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdStr)
		p.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updatedStr)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *sqlitePlanStore) Archive(ctx context.Context, id int64) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	now := time.Now().UTC()
	res, err := s.c.db.ExecContext(ctx,
		`UPDATE plans SET archived = 1, updated_at = ? WHERE id = ?`,
		now.Format(sqliteTimeLayout), id)
	if err != nil {
		return fmt.Errorf("sqlitePlanStore.Archive: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqlitePlanStore.Archive: plan id %d not found", id)),
		)
	}
	return nil
}

// ── sqliteCheckpointStore — implements RunCheckpointStore ───────────────

type sqliteCheckpointStore struct{ c *sqliteCore }

func (s *sqliteCheckpointStore) Save(ctx context.Context, cp *Checkpoint) error {
	if cp == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteCheckpointStore.Save: checkpoint is nil"),
		)
	}
	if cp.TurnUUID == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteCheckpointStore.Save: TurnUUID is required for idempotency"),
		)
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	// Check for idempotent replay: same RunID + same TurnUUID -> no-op.
	var existingUUID string
	err := s.c.db.QueryRowContext(ctx,
		`SELECT turn_uuid FROM checkpoints WHERE run_id = ?`, cp.RunID,
	).Scan(&existingUUID)
	if err == nil && existingUUID == cp.TurnUUID {
		return nil // idempotent no-op
	}

	now := time.Now().UTC()
	_, err = s.c.db.ExecContext(ctx,
		`INSERT INTO checkpoints (run_id, brain_id, state, turn_index, turn_uuid,
			messages_ref, system_ref, tools_ref, cost_snapshot, token_snapshot,
			budget_remain, trace_parent, resume_attempts, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
			brain_id = excluded.brain_id,
			state = excluded.state,
			turn_index = excluded.turn_index,
			turn_uuid = excluded.turn_uuid,
			messages_ref = excluded.messages_ref,
			system_ref = excluded.system_ref,
			tools_ref = excluded.tools_ref,
			cost_snapshot = excluded.cost_snapshot,
			token_snapshot = excluded.token_snapshot,
			budget_remain = excluded.budget_remain,
			trace_parent = excluded.trace_parent,
			updated_at = excluded.updated_at`,
		cp.RunID, cp.BrainID, cp.State, cp.TurnIndex, cp.TurnUUID,
		string(cp.MessagesRef), string(cp.SystemRef), string(cp.ToolsRef),
		[]byte(cp.CostSnapshot), []byte(cp.TokenSnapshot), []byte(cp.BudgetRemain),
		cp.TraceParent, now.Format(sqliteTimeLayout),
	)
	if err != nil {
		return fmt.Errorf("sqliteCheckpointStore.Save: %w", err)
	}
	return nil
}

func (s *sqliteCheckpointStore) Get(ctx context.Context, runID int64) (*Checkpoint, error) {
	row := s.c.db.QueryRowContext(ctx,
		`SELECT run_id, brain_id, state, turn_index, turn_uuid,
			messages_ref, system_ref, tools_ref, cost_snapshot, token_snapshot,
			budget_remain, trace_parent, resume_attempts, updated_at
		 FROM checkpoints WHERE run_id = ?`, runID)

	cp := &Checkpoint{}
	var messagesRef, systemRef, toolsRef string
	var costSnap, tokenSnap, budgetSnap []byte
	var updatedStr string

	err := row.Scan(&cp.RunID, &cp.BrainID, &cp.State, &cp.TurnIndex, &cp.TurnUUID,
		&messagesRef, &systemRef, &toolsRef, &costSnap, &tokenSnap,
		&budgetSnap, &cp.TraceParent, &cp.ResumeAttempts, &updatedStr)
	if err == sql.ErrNoRows {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqliteCheckpointStore.Get: run %d has no checkpoint", runID)),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqliteCheckpointStore.Get: %w", err)
	}

	cp.MessagesRef = Ref(messagesRef)
	cp.SystemRef = Ref(systemRef)
	cp.ToolsRef = Ref(toolsRef)
	if len(costSnap) > 0 {
		cp.CostSnapshot = json.RawMessage(costSnap)
	}
	if len(tokenSnap) > 0 {
		cp.TokenSnapshot = json.RawMessage(tokenSnap)
	}
	if len(budgetSnap) > 0 {
		cp.BudgetRemain = json.RawMessage(budgetSnap)
	}
	cp.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updatedStr)
	return cp, nil
}

func (s *sqliteCheckpointStore) MarkResumeAttempt(ctx context.Context, runID int64) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	now := time.Now().UTC()
	res, err := s.c.db.ExecContext(ctx,
		`UPDATE checkpoints SET resume_attempts = resume_attempts + 1, updated_at = ? WHERE run_id = ?`,
		now.Format(sqliteTimeLayout), runID)
	if err != nil {
		return fmt.Errorf("sqliteCheckpointStore.MarkResumeAttempt: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqliteCheckpointStore.MarkResumeAttempt: run %d has no checkpoint", runID)),
		)
	}
	return nil
}

// ── sqliteUsageLedger — implements UsageLedger ─────────────────────────

type sqliteUsageLedger struct{ c *sqliteCore }

func (s *sqliteUsageLedger) Record(ctx context.Context, rec *UsageRecord) error {
	if rec == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteUsageLedger.Record: rec is nil"),
		)
	}
	if rec.IdempotencyKey == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteUsageLedger.Record: IdempotencyKey is required"),
		)
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	now := rec.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	// INSERT OR IGNORE: if idempotency_key already exists, this is a no-op.
	_, err := s.c.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO usage_records
			(run_id, turn_index, provider, model, input_tokens, output_tokens,
			 cache_read, cache_creation, cost_usd, idempotency_key, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RunID, rec.TurnIndex, rec.Provider, rec.Model,
		rec.InputTokens, rec.OutputTokens, rec.CacheRead, rec.CacheCreation,
		rec.CostUSD, rec.IdempotencyKey, now.Format(sqliteTimeLayout),
	)
	if err != nil {
		return fmt.Errorf("sqliteUsageLedger.Record: %w", err)
	}
	return nil
}

func (s *sqliteUsageLedger) Sum(ctx context.Context, runID int64) (*UsageRecord, error) {
	row := s.c.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(input_tokens), 0),
				COALESCE(SUM(output_tokens), 0),
				COALESCE(SUM(cache_read), 0),
				COALESCE(SUM(cache_creation), 0),
				COALESCE(SUM(cost_usd), 0)
		 FROM usage_records WHERE run_id = ?`, runID)

	agg := &UsageRecord{
		RunID:     runID,
		TurnIndex: -1,
	}
	err := row.Scan(&agg.InputTokens, &agg.OutputTokens,
		&agg.CacheRead, &agg.CacheCreation, &agg.CostUSD)
	if err != nil {
		return nil, fmt.Errorf("sqliteUsageLedger.Sum: %w", err)
	}
	return agg, nil
}

// ── sqliteMetaStore — implements ArtifactMetaStore ──────────────────────

type sqliteMetaStore struct{ c *sqliteCore }

func (s *sqliteMetaStore) Put(ctx context.Context, meta *ArtifactMeta) error {
	if meta == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteMetaStore.Put: meta is nil"),
		)
	}
	if meta.Ref == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteMetaStore.Put: Ref is empty"),
		)
	}

	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	now := time.Now().UTC()

	// Check if row exists.
	var existingRef string
	err := s.c.db.QueryRowContext(ctx,
		`SELECT ref FROM artifact_meta WHERE ref = ?`, string(meta.Ref),
	).Scan(&existingRef)

	if err == sql.ErrNoRows {
		// Insert new row.
		refCount := meta.RefCount
		if refCount < 0 {
			refCount = 0
		}
		createdAt := meta.CreatedAt
		if createdAt.IsZero() {
			createdAt = now
		}
		var runID, turnIndex sql.NullInt64
		if meta.RunID != nil {
			runID = sql.NullInt64{Int64: *meta.RunID, Valid: true}
		}
		if meta.TurnIndex != nil {
			turnIndex = sql.NullInt64{Int64: int64(*meta.TurnIndex), Valid: true}
		}
		_, err = s.c.db.ExecContext(ctx,
			`INSERT INTO artifact_meta (ref, mime_type, size_bytes, run_id, turn_index, caption, ref_count, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(meta.Ref), meta.MimeType, meta.SizeBytes,
			runID, turnIndex, meta.Caption, refCount,
			createdAt.Format(sqliteTimeLayout), now.Format(sqliteTimeLayout),
		)
		if err != nil {
			return fmt.Errorf("sqliteMetaStore.Put: insert: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("sqliteMetaStore.Put: check: %w", err)
	}

	// Upsert path — merge non-zero metadata fields, preserve RefCount.
	updates := "updated_at = ?"
	args := []any{now.Format(sqliteTimeLayout)}

	if meta.MimeType != "" {
		updates += ", mime_type = ?"
		args = append(args, meta.MimeType)
	}
	if meta.SizeBytes != 0 {
		updates += ", size_bytes = ?"
		args = append(args, meta.SizeBytes)
	}
	if meta.Caption != "" {
		updates += ", caption = ?"
		args = append(args, meta.Caption)
	}
	if meta.RunID != nil {
		updates += ", run_id = ?"
		args = append(args, *meta.RunID)
	}
	if meta.TurnIndex != nil {
		updates += ", turn_index = ?"
		args = append(args, int64(*meta.TurnIndex))
	}
	args = append(args, string(meta.Ref))

	_, err = s.c.db.ExecContext(ctx,
		`UPDATE artifact_meta SET `+updates+` WHERE ref = ?`, args...)
	if err != nil {
		return fmt.Errorf("sqliteMetaStore.Put: update: %w", err)
	}
	return nil
}

func (s *sqliteMetaStore) Get(ctx context.Context, ref Ref) (*ArtifactMeta, error) {
	row := s.c.db.QueryRowContext(ctx,
		`SELECT ref, mime_type, size_bytes, run_id, turn_index, caption, ref_count, created_at, updated_at
		 FROM artifact_meta WHERE ref = ?`, string(ref))

	m := &ArtifactMeta{}
	var refStr string
	var runID, turnIndex sql.NullInt64
	var createdStr, updatedStr string

	err := row.Scan(&refStr, &m.MimeType, &m.SizeBytes,
		&runID, &turnIndex, &m.Caption, &m.RefCount,
		&createdStr, &updatedStr)
	if err == sql.ErrNoRows {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqliteMetaStore.Get: ref %q not found", ref)),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqliteMetaStore.Get: %w", err)
	}

	m.Ref = Ref(refStr)
	if runID.Valid {
		v := runID.Int64
		m.RunID = &v
	}
	if turnIndex.Valid {
		v := int(turnIndex.Int64)
		m.TurnIndex = &v
	}
	m.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdStr)
	m.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updatedStr)
	return m, nil
}

func (s *sqliteMetaStore) IncRefCount(ctx context.Context, ref Ref) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	res, err := s.c.db.ExecContext(ctx,
		`UPDATE artifact_meta SET ref_count = ref_count + 1, updated_at = ? WHERE ref = ?`,
		time.Now().UTC().Format(sqliteTimeLayout), string(ref))
	if err != nil {
		return fmt.Errorf("sqliteMetaStore.IncRefCount: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqliteMetaStore.IncRefCount: ref %q not found", ref)),
		)
	}
	return nil
}

func (s *sqliteMetaStore) DecRefCount(ctx context.Context, ref Ref) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	// Check current refcount first.
	var refCount int64
	err := s.c.db.QueryRowContext(ctx,
		`SELECT ref_count FROM artifact_meta WHERE ref = ?`, string(ref),
	).Scan(&refCount)
	if err == sql.ErrNoRows {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("sqliteMetaStore.DecRefCount: ref %q not found", ref)),
		)
	}
	if err != nil {
		return fmt.Errorf("sqliteMetaStore.DecRefCount: read: %w", err)
	}
	if refCount <= 0 {
		return brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf("sqliteMetaStore.DecRefCount: ref %q already at zero", ref)),
		)
	}

	_, err = s.c.db.ExecContext(ctx,
		`UPDATE artifact_meta SET ref_count = ref_count - 1, updated_at = ? WHERE ref = ?`,
		time.Now().UTC().Format(sqliteTimeLayout), string(ref))
	if err != nil {
		return fmt.Errorf("sqliteMetaStore.DecRefCount: update: %w", err)
	}
	return nil
}

// ── sqliteRunStore — implements RunStore ───────────────────────────────

type sqliteRunStore struct{ c *sqliteCore }

func (s *sqliteRunStore) Create(ctx context.Context, run *Run) (int64, error) {
	if run == nil {
		return 0, brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteRunStore.Create: run is nil"))
	}
	now := time.Now().UTC()
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	res, err := s.c.db.ExecContext(ctx,
		`INSERT INTO runs (run_key, brain_id, prompt, status, mode, workdir, turn_uuid, plan_id, result, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, '', 0, NULL, '', ?, ?)`,
		run.RunKey, run.BrainID, run.Prompt, run.Status, run.Mode, run.Workdir,
		now.Format(sqliteTimeLayout), now.Format(sqliteTimeLayout))
	if err != nil {
		return 0, fmt.Errorf("sqliteRunStore.Create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("sqliteRunStore.Create: lastInsertId: %w", err)
	}

	if len(run.Events) > 0 {
		for _, ev := range run.Events {
			at := ev.At
			if at.IsZero() {
				at = now
			}
			s.c.db.ExecContext(ctx,
				`INSERT INTO run_events (run_id, event_type, message, data, created_at) VALUES (?, ?, ?, ?, ?)`,
				id, ev.Type, ev.Message, []byte(ev.Data), at.Format(sqliteTimeLayout))
		}
	}
	return id, nil
}

func (s *sqliteRunStore) Get(ctx context.Context, id int64) (*Run, error) {
	run, err := s.scanRun(ctx, `SELECT id, run_key, brain_id, prompt, status, mode, workdir, turn_uuid, plan_id, result, error, created_at, updated_at FROM runs WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	run.Events, _ = s.loadEvents(ctx, id)
	return run, nil
}

func (s *sqliteRunStore) GetByKey(ctx context.Context, runKey string) (*Run, error) {
	run, err := s.scanRun(ctx, `SELECT id, run_key, brain_id, prompt, status, mode, workdir, turn_uuid, plan_id, result, error, created_at, updated_at FROM runs WHERE run_key = ?`, runKey)
	if err != nil {
		return nil, err
	}
	run.Events, _ = s.loadEvents(ctx, run.ID)
	return run, nil
}

func (s *sqliteRunStore) scanRun(ctx context.Context, query string, args ...interface{}) (*Run, error) {
	row := s.c.db.QueryRowContext(ctx, query, args...)
	r := &Run{}
	var resultBytes []byte
	var createdStr, updatedStr string
	err := row.Scan(&r.ID, &r.RunKey, &r.BrainID, &r.Prompt, &r.Status, &r.Mode, &r.Workdir,
		&r.TurnUUID, &r.PlanID, &resultBytes, &r.Error, &createdStr, &updatedStr)
	if err == sql.ErrNoRows {
		return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage("sqliteRunStore: run not found"))
	}
	if err != nil {
		return nil, fmt.Errorf("sqliteRunStore.scanRun: %w", err)
	}
	r.Result = json.RawMessage(resultBytes)
	r.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdStr)
	r.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updatedStr)
	return r, nil
}

func (s *sqliteRunStore) loadEvents(ctx context.Context, runID int64) ([]RunEvent, error) {
	rows, err := s.c.db.QueryContext(ctx,
		`SELECT event_type, message, data, created_at FROM run_events WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []RunEvent
	for rows.Next() {
		var ev RunEvent
		var dataBytes []byte
		var atStr string
		if err := rows.Scan(&ev.Type, &ev.Message, &dataBytes, &atStr); err != nil {
			continue
		}
		ev.Data = json.RawMessage(dataBytes)
		ev.At, _ = time.Parse(sqliteTimeLayout, atStr)
		events = append(events, ev)
	}
	return events, nil
}

func (s *sqliteRunStore) Update(ctx context.Context, id int64, mutate func(*Run)) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()

	run, err := s.scanRun(ctx,
		`SELECT id, run_key, brain_id, prompt, status, mode, workdir, turn_uuid, plan_id, result, error, created_at, updated_at FROM runs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	mutate(run)
	now := time.Now().UTC()
	_, err = s.c.db.ExecContext(ctx,
		`UPDATE runs SET brain_id=?, prompt=?, status=?, mode=?, workdir=?, turn_uuid=?, plan_id=?, result=?, error=?, updated_at=? WHERE id=?`,
		run.BrainID, run.Prompt, run.Status, run.Mode, run.Workdir, run.TurnUUID, run.PlanID,
		[]byte(run.Result), run.Error, now.Format(sqliteTimeLayout), id)
	return err
}

func (s *sqliteRunStore) AppendEvent(ctx context.Context, id int64, ev RunEvent) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	at := ev.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.c.db.ExecContext(ctx,
		`INSERT INTO run_events (run_id, event_type, message, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, ev.Type, ev.Message, []byte(ev.Data), at.Format(sqliteTimeLayout))
	return err
}

func (s *sqliteRunStore) Finish(ctx context.Context, id int64, status string, result json.RawMessage, errText string) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	now := time.Now().UTC()

	tx, err := s.c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqliteRunStore.Finish: begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET status=?, result=?, error=?, updated_at=? WHERE id=?`,
		status, []byte(result), errText, now.Format(sqliteTimeLayout), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run_events (run_id, event_type, message, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, "run."+status, status, []byte(result), now.Format(sqliteTimeLayout)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteRunStore) List(ctx context.Context, limit int, status string) ([]*Run, error) {
	var query string
	var args []interface{}
	if status != "" && status != "all" {
		query = `SELECT id, run_key, brain_id, prompt, status, mode, workdir, turn_uuid, plan_id, result, error, created_at, updated_at FROM runs WHERE status = ? ORDER BY id DESC`
		args = append(args, status)
	} else {
		query = `SELECT id, run_key, brain_id, prompt, status, mode, workdir, turn_uuid, plan_id, result, error, created_at, updated_at FROM runs ORDER BY id DESC`
	}
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqliteRunStore.List: %w", err)
	}
	defer rows.Close()

	var out []*Run
	for rows.Next() {
		r := &Run{}
		var resultBytes []byte
		var createdStr, updatedStr string
		if err := rows.Scan(&r.ID, &r.RunKey, &r.BrainID, &r.Prompt, &r.Status, &r.Mode, &r.Workdir,
			&r.TurnUUID, &r.PlanID, &resultBytes, &r.Error, &createdStr, &updatedStr); err != nil {
			continue
		}
		r.Result = json.RawMessage(resultBytes)
		r.CreatedAt, _ = time.Parse(sqliteTimeLayout, createdStr)
		r.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updatedStr)
		out = append(out, r)
	}
	return out, nil
}

// ── sqliteAuditLogger — implements AuditLogger ────────────────────────

type sqliteAuditLogger struct{ c *sqliteCore }

func (s *sqliteAuditLogger) Log(ctx context.Context, ev *AuditEvent) error {
	if ev == nil {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("sqliteAuditLogger.Log: event is nil"))
	}
	now := time.Now().UTC()
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = now
	}
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`INSERT INTO audit_logs (event_id, execution_id, event_type, actor, timestamp, data, status_code, details, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.EventID, ev.ExecutionID, ev.EventType, ev.Actor,
		ts.Format(sqliteTimeLayout), []byte(ev.Data), ev.StatusCode, ev.Details,
		now.Format(sqliteTimeLayout))
	return err
}

func (s *sqliteAuditLogger) Query(ctx context.Context, filter AuditFilter) ([]*AuditEvent, error) {
	where := "1=1"
	var args []interface{}
	if filter.ExecutionID != "" {
		where += " AND execution_id = ?"
		args = append(args, filter.ExecutionID)
	}
	if filter.EventType != "" {
		where += " AND event_type = ?"
		args = append(args, filter.EventType)
	}
	if filter.Actor != "" {
		where += " AND actor = ?"
		args = append(args, filter.Actor)
	}
	if !filter.Since.IsZero() {
		where += " AND timestamp >= ?"
		args = append(args, filter.Since.Format(sqliteTimeLayout))
	}
	if !filter.Until.IsZero() {
		where += " AND timestamp <= ?"
		args = append(args, filter.Until.Format(sqliteTimeLayout))
	}
	query := fmt.Sprintf(
		`SELECT id, event_id, execution_id, event_type, actor, timestamp, data, status_code, details
		 FROM audit_logs WHERE %s ORDER BY timestamp DESC`, where)
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	rows, err := s.c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqliteAuditLogger.Query: %w", err)
	}
	defer rows.Close()

	var out []*AuditEvent
	for rows.Next() {
		ev := &AuditEvent{}
		var dataBytes []byte
		var tsStr string
		if err := rows.Scan(&ev.ID, &ev.EventID, &ev.ExecutionID, &ev.EventType,
			&ev.Actor, &tsStr, &dataBytes, &ev.StatusCode, &ev.Details); err != nil {
			continue
		}
		ev.Data = json.RawMessage(dataBytes)
		ev.Timestamp, _ = time.Parse(sqliteTimeLayout, tsStr)
		out = append(out, ev)
	}
	return out, nil
}

func (s *sqliteAuditLogger) Purge(ctx context.Context, olderThanDays int) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -olderThanDays)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	res, err := s.c.db.ExecContext(ctx,
		`DELETE FROM audit_logs WHERE timestamp < ?`, cutoff.Format(sqliteTimeLayout))
	if err != nil {
		return 0, fmt.Errorf("sqliteAuditLogger.Purge: %w", err)
	}
	return res.RowsAffected()
}

// ── sqliteLearningStore — implements LearningStore ─────────────────────

type sqliteLearningStore struct{ c *sqliteCore }

// L1

func (s *sqliteLearningStore) SaveProfile(ctx context.Context, p *LearningProfile) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	now := p.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	coldStart := 0
	if p.ColdStart {
		coldStart = 1
	}
	_, err := s.c.db.ExecContext(ctx,
		`INSERT INTO learning_profiles (brain_kind, cold_start, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(brain_kind) DO UPDATE SET cold_start=excluded.cold_start, updated_at=excluded.updated_at`,
		p.BrainKind, coldStart, now.Format(sqliteTimeLayout))
	return err
}

func (s *sqliteLearningStore) SaveTaskScore(ctx context.Context, ts *LearningTaskScore) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	_, err := s.c.db.ExecContext(ctx,
		`INSERT INTO learning_task_scores
		 (brain_kind, task_type, sample_count, accuracy_value, accuracy_alpha, speed_value, speed_alpha, cost_value, cost_alpha, stability_value, stability_alpha)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(brain_kind, task_type) DO UPDATE SET
		 sample_count=excluded.sample_count,
		 accuracy_value=excluded.accuracy_value, accuracy_alpha=excluded.accuracy_alpha,
		 speed_value=excluded.speed_value, speed_alpha=excluded.speed_alpha,
		 cost_value=excluded.cost_value, cost_alpha=excluded.cost_alpha,
		 stability_value=excluded.stability_value, stability_alpha=excluded.stability_alpha`,
		ts.BrainKind, ts.TaskType, ts.SampleCount,
		ts.AccuracyValue, ts.AccuracyAlpha,
		ts.SpeedValue, ts.SpeedAlpha,
		ts.CostValue, ts.CostAlpha,
		ts.StabilityValue, ts.StabilityAlpha)
	return err
}

func (s *sqliteLearningStore) ListProfiles(ctx context.Context) ([]*LearningProfile, error) {
	rows, err := s.c.db.QueryContext(ctx,
		`SELECT brain_kind, cold_start, updated_at FROM learning_profiles ORDER BY brain_kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LearningProfile
	for rows.Next() {
		p := &LearningProfile{}
		var coldInt int
		var updStr string
		if err := rows.Scan(&p.BrainKind, &coldInt, &updStr); err != nil {
			continue
		}
		p.ColdStart = coldInt != 0
		p.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updStr)
		out = append(out, p)
	}
	return out, nil
}

func (s *sqliteLearningStore) ListTaskScores(ctx context.Context, brainKind string) ([]*LearningTaskScore, error) {
	rows, err := s.c.db.QueryContext(ctx,
		`SELECT brain_kind, task_type, sample_count,
		        accuracy_value, accuracy_alpha, speed_value, speed_alpha,
		        cost_value, cost_alpha, stability_value, stability_alpha
		 FROM learning_task_scores WHERE brain_kind = ? ORDER BY task_type`, brainKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LearningTaskScore
	for rows.Next() {
		ts := &LearningTaskScore{}
		if err := rows.Scan(&ts.BrainKind, &ts.TaskType, &ts.SampleCount,
			&ts.AccuracyValue, &ts.AccuracyAlpha,
			&ts.SpeedValue, &ts.SpeedAlpha,
			&ts.CostValue, &ts.CostAlpha,
			&ts.StabilityValue, &ts.StabilityAlpha); err != nil {
			continue
		}
		out = append(out, ts)
	}
	return out, nil
}

// L2

func (s *sqliteLearningStore) SaveSequence(ctx context.Context, seq *LearningSequence) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	at := seq.RecordedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := s.c.db.ExecContext(ctx,
		`INSERT INTO learning_sequences (sequence_id, total_score, recorded_at) VALUES (?, ?, ?)`,
		seq.SequenceID, seq.TotalScore, at.Format(sqliteTimeLayout))
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	for _, step := range seq.Steps {
		s.c.db.ExecContext(ctx,
			`INSERT INTO learning_seq_steps (sequence_id, brain_kind, task_type, duration_ms, score) VALUES (?, ?, ?, ?, ?)`,
			id, step.BrainKind, step.TaskType, step.DurationMs, step.Score)
	}
	return nil
}

func (s *sqliteLearningStore) ListSequences(ctx context.Context, limit int) ([]*LearningSequence, error) {
	query := `SELECT id, sequence_id, total_score, recorded_at FROM learning_sequences ORDER BY id DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LearningSequence
	for rows.Next() {
		seq := &LearningSequence{}
		var atStr string
		if err := rows.Scan(&seq.ID, &seq.SequenceID, &seq.TotalScore, &atStr); err != nil {
			continue
		}
		seq.RecordedAt, _ = time.Parse(sqliteTimeLayout, atStr)
		out = append(out, seq)
	}
	// load steps
	for _, seq := range out {
		stepRows, err := s.c.db.QueryContext(ctx,
			`SELECT brain_kind, task_type, duration_ms, score FROM learning_seq_steps WHERE sequence_id = ? ORDER BY id`, seq.ID)
		if err != nil {
			continue
		}
		for stepRows.Next() {
			var step LearningSeqStep
			if err := stepRows.Scan(&step.BrainKind, &step.TaskType, &step.DurationMs, &step.Score); err != nil {
				continue
			}
			seq.Steps = append(seq.Steps, step)
		}
		stepRows.Close()
	}
	return out, nil
}

// L3

func (s *sqliteLearningStore) SavePreference(ctx context.Context, pref *LearningPreference) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	now := pref.UpdatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.c.db.ExecContext(ctx,
		`INSERT INTO learning_preferences (category, value, weight, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(category) DO UPDATE SET value=excluded.value, weight=excluded.weight, updated_at=excluded.updated_at`,
		pref.Category, pref.Value, pref.Weight, now.Format(sqliteTimeLayout))
	return err
}

func (s *sqliteLearningStore) GetPreference(ctx context.Context, category string) (*LearningPreference, error) {
	row := s.c.db.QueryRowContext(ctx,
		`SELECT category, value, weight, updated_at FROM learning_preferences WHERE category = ?`, category)
	p := &LearningPreference{}
	var updStr string
	err := row.Scan(&p.Category, &p.Value, &p.Weight, &updStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updStr)
	return p, nil
}

func (s *sqliteLearningStore) ListPreferences(ctx context.Context) ([]*LearningPreference, error) {
	rows, err := s.c.db.QueryContext(ctx,
		`SELECT category, value, weight, updated_at FROM learning_preferences ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LearningPreference
	for rows.Next() {
		p := &LearningPreference{}
		var updStr string
		if err := rows.Scan(&p.Category, &p.Value, &p.Weight, &updStr); err != nil {
			continue
		}
		p.UpdatedAt, _ = time.Parse(sqliteTimeLayout, updStr)
		out = append(out, p)
	}
	return out, nil
}

// ── sqliteSharedMessageStore — implements SharedMessageStore ───────────

type sqliteSharedMessageStore struct{ c *sqliteCore }

func (s *sqliteSharedMessageStore) Save(ctx context.Context, msg *SharedMessage) error {
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	now := msg.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	_, err := s.c.db.ExecContext(ctx,
		`INSERT INTO shared_messages (from_brain, to_brain, messages, count, created_at) VALUES (?, ?, ?, ?, ?)`,
		msg.FromBrain, msg.ToBrain, []byte(msg.Messages), msg.Count, now.Format(sqliteTimeLayout))
	return err
}

func (s *sqliteSharedMessageStore) ListByBrains(ctx context.Context, from, to string, limit int) ([]*SharedMessage, error) {
	query := `SELECT id, from_brain, to_brain, messages, count, created_at FROM shared_messages WHERE from_brain = ? AND to_brain = ? ORDER BY id DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	return s.queryMessages(ctx, query, from, to)
}

func (s *sqliteSharedMessageStore) ListRecent(ctx context.Context, limit int) ([]*SharedMessage, error) {
	query := `SELECT id, from_brain, to_brain, messages, count, created_at FROM shared_messages ORDER BY id DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	return s.queryMessages(ctx, query)
}

func (s *sqliteSharedMessageStore) queryMessages(ctx context.Context, query string, args ...interface{}) ([]*SharedMessage, error) {
	rows, err := s.c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SharedMessage
	for rows.Next() {
		m := &SharedMessage{}
		var dataBytes []byte
		var atStr string
		if err := rows.Scan(&m.ID, &m.FromBrain, &m.ToBrain, &dataBytes, &m.Count, &atStr); err != nil {
			continue
		}
		m.Messages = json.RawMessage(dataBytes)
		m.CreatedAt, _ = time.Parse(sqliteTimeLayout, atStr)
		out = append(out, m)
	}
	return out, nil
}
