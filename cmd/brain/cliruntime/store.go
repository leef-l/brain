package cliruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type RunEvent struct {
	At      time.Time       `json:"at"`
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type RunRecord struct {
	ID         string          `json:"run_id"`
	StoreRunID int64           `json:"store_run_id"`
	BrainID    string          `json:"brain_id"`
	Prompt     string          `json:"prompt,omitempty"`
	Status     string          `json:"status"`
	Mode       string          `json:"mode,omitempty"`
	Workdir    string          `json:"workdir,omitempty"`
	TurnUUID   string          `json:"turn_uuid,omitempty"`
	PlanID     int64           `json:"plan_id,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Events     []RunEvent      `json:"events,omitempty"`
}

func (r *RunRecord) Clone() *RunRecord {
	if r == nil {
		return nil
	}
	out := *r
	out.Result = append(json.RawMessage(nil), r.Result...)
	if len(r.Events) > 0 {
		out.Events = make([]RunEvent, len(r.Events))
		copy(out.Events, r.Events)
	}
	return &out
}

type runtimeDB struct {
	NextSeq        int64        `json:"next_seq"`
	NextStoreRunID int64        `json:"next_store_run_id"`
	Runs           []*RunRecord `json:"runs"`
}

type Store struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	db    *runtimeDB
	index map[string]*RunRecord
}

func OpenStore(path string) (*Store, error) {
	rs := &Store{
		path: path,
		now:  func() time.Time { return time.Now().UTC() },
		db: &runtimeDB{
			NextSeq:        1,
			NextStoreRunID: 1,
		},
		index: make(map[string]*RunRecord),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rs, nil
		}
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, rs.db); err != nil {
			return nil, fmt.Errorf("parse runtime store %s: %w", path, err)
		}
	}
	if rs.db.NextSeq <= 0 {
		rs.db.NextSeq = 1
	}
	if rs.db.NextStoreRunID <= 0 {
		rs.db.NextStoreRunID = 1
	}
	for _, run := range rs.db.Runs {
		if run == nil || run.ID == "" {
			continue
		}
		rs.index[run.ID] = run
		if run.StoreRunID >= rs.db.NextStoreRunID {
			rs.db.NextStoreRunID = run.StoreRunID + 1
		}
	}
	return rs, nil
}

func (s *Store) flushLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.db, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Create(brainID, prompt, mode, workdir string) (*RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	id := fmt.Sprintf("run-%d-%s", s.db.NextSeq, now.Format("20060102T150405Z"))
	rec := &RunRecord{
		ID:         id,
		StoreRunID: s.db.NextStoreRunID,
		BrainID:    brainID,
		Prompt:     prompt,
		Status:     "running",
		Mode:       mode,
		Workdir:    workdir,
		CreatedAt:  now,
		UpdatedAt:  now,
		Events: []RunEvent{{
			At:      now,
			Type:    "run.created",
			Message: "run created",
		}},
	}
	s.db.NextSeq++
	s.db.NextStoreRunID++
	s.db.Runs = append(s.db.Runs, rec)
	s.index[rec.ID] = rec
	if err := s.flushLocked(); err != nil {
		return nil, err
	}
	return rec.Clone(), nil
}

func (s *Store) Get(id string) (*RunRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.index[id]
	if !ok {
		return nil, false
	}
	return rec.Clone(), true
}

func (s *Store) Update(id string, mutate func(*RunRecord)) (*RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.index[id]
	if !ok {
		return nil, fmt.Errorf("run %s not found", id)
	}
	mutate(rec)
	rec.UpdatedAt = s.now()
	if err := s.flushLocked(); err != nil {
		return nil, err
	}
	return rec.Clone(), nil
}

func (s *Store) AppendEvent(id, eventType, message string, data json.RawMessage) error {
	_, err := s.Update(id, func(rec *RunRecord) {
		rec.Events = append(rec.Events, RunEvent{
			At:      s.now(),
			Type:    eventType,
			Message: message,
			Data:    append(json.RawMessage(nil), data...),
		})
	})
	return err
}

func (s *Store) SetCheckpoint(id, turnUUID string) error {
	_, err := s.Update(id, func(rec *RunRecord) {
		rec.TurnUUID = turnUUID
	})
	return err
}

func (s *Store) SetPlanID(id string, planID int64) error {
	_, err := s.Update(id, func(rec *RunRecord) {
		rec.PlanID = planID
	})
	return err
}

func (s *Store) Finish(id, status string, result json.RawMessage, errText string) (*RunRecord, error) {
	return s.Update(id, func(rec *RunRecord) {
		rec.Status = status
		rec.Result = append(json.RawMessage(nil), result...)
		rec.Error = errText
		rec.Events = append(rec.Events, RunEvent{
			At:      s.now(),
			Type:    "run." + status,
			Message: status,
			Data:    append(json.RawMessage(nil), result...),
		})
	})
}

func (s *Store) List(limit int, state string) []*RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*RunRecord
	for _, rec := range s.db.Runs {
		if rec == nil {
			continue
		}
		if state != "" && state != "all" && rec.Status != state {
			continue
		}
		out = append(out, rec.Clone())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
