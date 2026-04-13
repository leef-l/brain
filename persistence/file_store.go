// Package persistence — file_store.go implements JSON-file-backed persistence
// for all stores (PlanStore, RunCheckpointStore, UsageLedger, ArtifactMetaStore).
//
// This is the zero-dependency persistent backend: no SQLite driver, no CGo,
// no third-party imports. Data is stored as a single JSON file that is loaded
// into memory on open and atomically rewritten on every mutation (tmpfile +
// rename). This is suitable for solo-mode development and small workloads.
//
// Because PlanStore.Get, RunCheckpointStore.Get, and ArtifactMetaStore.Get
// have different signatures, FileStore cannot directly implement all three.
// Instead it provides typed accessor wrappers: FileStore.PlanStore(),
// FileStore.CheckpointStore(), FileStore.Ledger(), FileStore.MetaStore().
package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	brainerrors "github.com/leef-l/brain/errors"
)

// fileDB is the on-disk JSON structure.
type fileDB struct {
	Plans        []*BrainPlan      `json:"plans"`
	PlanDeltas   []*BrainPlanDelta `json:"plan_deltas"`
	Checkpoints  []*Checkpoint     `json:"checkpoints"`
	Usage        []*UsageRecord    `json:"usage"`
	ArtifactMeta []*ArtifactMeta   `json:"artifact_meta"`
}

// FileStore is a JSON-file-backed storage engine.
type FileStore struct {
	path    string
	mu      sync.Mutex
	db      *fileDB
	nextID  int64
	nowFunc func() time.Time
}

// FileStoreOption configures a FileStore.
type FileStoreOption func(*FileStore)

// WithFileStoreNow sets a custom time function for deterministic tests.
func WithFileStoreNow(fn func() time.Time) FileStoreOption {
	return func(fs *FileStore) { fs.nowFunc = fn }
}

// OpenFileStore opens or creates a JSON file store at the given path.
func OpenFileStore(path string, opts ...FileStoreOption) (*FileStore, error) {
	fs := &FileStore{
		path:    path,
		db:      &fileDB{},
		nextID:  1,
		nowFunc: func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(fs)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("filestore: mkdir %s: %w", dir, err)
	}

	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, fs.db); err != nil {
			return nil, fmt.Errorf("filestore: parse %s: %w", path, err)
		}
		for _, p := range fs.db.Plans {
			if p.ID >= fs.nextID {
				fs.nextID = p.ID + 1
			}
		}
		for _, d := range fs.db.PlanDeltas {
			if d.ID >= fs.nextID {
				fs.nextID = d.ID + 1
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("filestore: read %s: %w", path, err)
	}

	return fs, nil
}

// flush atomically writes the in-memory DB to disk.
func (f *FileStore) flush() error {
	data, err := json.MarshalIndent(f.db, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path)
}

// Path returns the file path of the store.
func (f *FileStore) Path() string { return f.path }

// =========================================================================
// PlanStore adapter
// =========================================================================

// FilePlanStore wraps FileStore to implement PlanStore.
type FilePlanStore struct{ f *FileStore }

// PlanStore returns a PlanStore backed by this FileStore.
func (f *FileStore) PlanStore() PlanStore { return &FilePlanStore{f: f} }

func (s *FilePlanStore) Create(ctx context.Context, plan *BrainPlan) (int64, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	now := s.f.nowFunc()
	plan.ID = s.f.nextID
	s.f.nextID++
	plan.CreatedAt = now
	plan.UpdatedAt = now

	s.f.db.Plans = append(s.f.db.Plans, plan)
	if err := s.f.flush(); err != nil {
		return 0, brainerrors.New(brainerrors.CodeInvariantViolated,
			brainerrors.WithMessage(fmt.Sprintf("filestore: flush: %v", err)))
	}
	return plan.ID, nil
}

func (s *FilePlanStore) Get(ctx context.Context, id int64) (*BrainPlan, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, p := range s.f.db.Plans {
		if p.ID == id {
			cp := *p
			return &cp, nil
		}
	}
	return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("plan %d not found", id)))
}

func (s *FilePlanStore) Update(ctx context.Context, id int64, delta *BrainPlanDelta) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	var plan *BrainPlan
	for _, p := range s.f.db.Plans {
		if p.ID == id {
			plan = p
			break
		}
	}
	if plan == nil {
		return brainerrors.New(brainerrors.CodeRecordNotFound,
			brainerrors.WithMessage(fmt.Sprintf("plan %d not found", id)))
	}
	if plan.Archived {
		return brainerrors.New(brainerrors.CodeWorkflowPrecondition,
			brainerrors.WithMessage(fmt.Sprintf("plan %d is archived", id)))
	}

	now := s.f.nowFunc()
	delta.ID = s.f.nextID
	s.f.nextID++
	delta.PlanID = id
	delta.CreatedAt = now

	plan.Version = delta.Version
	if len(delta.Payload) > 0 {
		plan.CurrentState = delta.Payload
	}
	plan.UpdatedAt = now

	s.f.db.PlanDeltas = append(s.f.db.PlanDeltas, delta)
	return s.f.flush()
}

func (s *FilePlanStore) ListByRun(ctx context.Context, runID int64) ([]*BrainPlan, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	var result []*BrainPlan
	for _, p := range s.f.db.Plans {
		if p.RunID == runID {
			cp := *p
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (s *FilePlanStore) Archive(ctx context.Context, id int64) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, p := range s.f.db.Plans {
		if p.ID == id {
			p.Archived = true
			p.UpdatedAt = s.f.nowFunc()
			return s.f.flush()
		}
	}
	return brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("plan %d not found", id)))
}

// =========================================================================
// RunCheckpointStore adapter
// =========================================================================

// FileCheckpointStore wraps FileStore to implement RunCheckpointStore.
type FileCheckpointStore struct{ f *FileStore }

// CheckpointStore returns a RunCheckpointStore backed by this FileStore.
func (f *FileStore) CheckpointStore() RunCheckpointStore { return &FileCheckpointStore{f: f} }

func (s *FileCheckpointStore) Save(ctx context.Context, cp *Checkpoint) error {
	if cp.TurnUUID == "" {
		return brainerrors.New(brainerrors.CodeInvalidParams,
			brainerrors.WithMessage("filestore: TurnUUID is required"))
	}

	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	// Idempotency.
	for _, existing := range s.f.db.Checkpoints {
		if existing.RunID == cp.RunID && existing.TurnUUID == cp.TurnUUID {
			return nil
		}
	}

	cp.UpdatedAt = s.f.nowFunc()

	found := false
	for i, existing := range s.f.db.Checkpoints {
		if existing.RunID == cp.RunID {
			s.f.db.Checkpoints[i] = cp
			found = true
			break
		}
	}
	if !found {
		s.f.db.Checkpoints = append(s.f.db.Checkpoints, cp)
	}
	return s.f.flush()
}

func (s *FileCheckpointStore) Get(ctx context.Context, runID int64) (*Checkpoint, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, cp := range s.f.db.Checkpoints {
		if cp.RunID == runID {
			ret := *cp
			return &ret, nil
		}
	}
	return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("checkpoint for run %d not found", runID)))
}

func (s *FileCheckpointStore) MarkResumeAttempt(ctx context.Context, runID int64) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, cp := range s.f.db.Checkpoints {
		if cp.RunID == runID {
			cp.ResumeAttempts++
			cp.UpdatedAt = s.f.nowFunc()
			return s.f.flush()
		}
	}
	return brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("checkpoint for run %d not found", runID)))
}

// =========================================================================
// UsageLedger adapter
// =========================================================================

// FileUsageLedger wraps FileStore to implement UsageLedger.
type FileUsageLedger struct{ f *FileStore }

// Ledger returns a UsageLedger backed by this FileStore.
func (f *FileStore) Ledger() UsageLedger { return &FileUsageLedger{f: f} }

func (s *FileUsageLedger) Record(ctx context.Context, rec *UsageRecord) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	if rec.IdempotencyKey != "" {
		for _, existing := range s.f.db.Usage {
			if existing.IdempotencyKey == rec.IdempotencyKey {
				return nil
			}
		}
	}

	rec.CreatedAt = s.f.nowFunc()
	s.f.db.Usage = append(s.f.db.Usage, rec)
	return s.f.flush()
}

func (s *FileUsageLedger) Sum(ctx context.Context, runID int64) (*UsageRecord, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	agg := &UsageRecord{RunID: runID, TurnIndex: -1}
	for _, r := range s.f.db.Usage {
		if r.RunID == runID {
			agg.InputTokens += r.InputTokens
			agg.OutputTokens += r.OutputTokens
			agg.CacheRead += r.CacheRead
			agg.CacheCreation += r.CacheCreation
			agg.CostUSD += r.CostUSD
		}
	}
	return agg, nil
}

// =========================================================================
// ArtifactMetaStore adapter
// =========================================================================

// FileArtifactMetaStore wraps FileStore to implement ArtifactMetaStore.
type FileArtifactMetaStore struct{ f *FileStore }

// MetaStore returns an ArtifactMetaStore backed by this FileStore.
func (f *FileStore) MetaStore() ArtifactMetaStore { return &FileArtifactMetaStore{f: f} }

func (s *FileArtifactMetaStore) Put(ctx context.Context, meta *ArtifactMeta) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	now := s.f.nowFunc()
	for i, existing := range s.f.db.ArtifactMeta {
		if existing.Ref == meta.Ref {
			meta.RefCount = existing.RefCount
			meta.UpdatedAt = now
			s.f.db.ArtifactMeta[i] = meta
			return s.f.flush()
		}
	}

	meta.CreatedAt = now
	meta.UpdatedAt = now
	if meta.RefCount == 0 {
		meta.RefCount = 1
	}
	s.f.db.ArtifactMeta = append(s.f.db.ArtifactMeta, meta)
	return s.f.flush()
}

func (s *FileArtifactMetaStore) Get(ctx context.Context, ref Ref) (*ArtifactMeta, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, m := range s.f.db.ArtifactMeta {
		if m.Ref == ref {
			cp := *m
			return &cp, nil
		}
	}
	return nil, brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("artifact meta %s not found", ref)))
}

func (s *FileArtifactMetaStore) IncRefCount(ctx context.Context, ref Ref) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, m := range s.f.db.ArtifactMeta {
		if m.Ref == ref {
			m.RefCount++
			m.UpdatedAt = s.f.nowFunc()
			return s.f.flush()
		}
	}
	return brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("artifact meta %s not found", ref)))
}

func (s *FileArtifactMetaStore) DecRefCount(ctx context.Context, ref Ref) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()

	for _, m := range s.f.db.ArtifactMeta {
		if m.Ref == ref {
			if m.RefCount <= 0 {
				return brainerrors.New(brainerrors.CodeInvariantViolated,
					brainerrors.WithMessage(fmt.Sprintf("artifact meta %s: refcount already 0", ref)))
			}
			m.RefCount--
			m.UpdatedAt = s.f.nowFunc()
			return s.f.flush()
		}
	}
	return brainerrors.New(brainerrors.CodeRecordNotFound,
		brainerrors.WithMessage(fmt.Sprintf("artifact meta %s not found", ref)))
}

// --- Interface assertions ---
var (
	_ PlanStore          = (*FilePlanStore)(nil)
	_ RunCheckpointStore = (*FileCheckpointStore)(nil)
	_ UsageLedger        = (*FileUsageLedger)(nil)
	_ ArtifactMetaStore  = (*FileArtifactMetaStore)(nil)
)
