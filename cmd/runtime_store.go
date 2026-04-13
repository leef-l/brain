package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type persistedRunEvent struct {
	At      time.Time       `json:"at"`
	Type    string          `json:"type"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type persistedRunRecord struct {
	ID         string              `json:"run_id"`
	StoreRunID int64               `json:"store_run_id"`
	BrainID    string              `json:"brain_id"`
	Prompt     string              `json:"prompt,omitempty"`
	Status     string              `json:"status"`
	Mode       string              `json:"mode,omitempty"`
	Workdir    string              `json:"workdir,omitempty"`
	TurnUUID   string              `json:"turn_uuid,omitempty"`
	PlanID     int64               `json:"plan_id,omitempty"`
	Result     json.RawMessage     `json:"result,omitempty"`
	Error      string              `json:"error,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
	Events     []persistedRunEvent `json:"events,omitempty"`
}

func (r *persistedRunRecord) clone() *persistedRunRecord {
	if r == nil {
		return nil
	}
	out := *r
	out.Result = append(json.RawMessage(nil), r.Result...)
	if len(r.Events) > 0 {
		out.Events = make([]persistedRunEvent, len(r.Events))
		copy(out.Events, r.Events)
	}
	return &out
}

type runtimeDB struct {
	NextSeq        int64                 `json:"next_seq"`
	NextStoreRunID int64                 `json:"next_store_run_id"`
	Runs           []*persistedRunRecord `json:"runs"`
}

type runtimeStore struct {
	path string
	now  func() time.Time

	mu    sync.Mutex
	db    *runtimeDB
	index map[string]*persistedRunRecord
}

func openRuntimeStore(path string) (*runtimeStore, error) {
	rs := &runtimeStore{
		path: path,
		now:  func() time.Time { return time.Now().UTC() },
		db: &runtimeDB{
			NextSeq:        1,
			NextStoreRunID: 1,
		},
		index: make(map[string]*persistedRunRecord),
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

func (s *runtimeStore) flushLocked() error {
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

func (s *runtimeStore) create(brainID, prompt, mode, workdir string) (*persistedRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	id := fmt.Sprintf("run-%d-%s", s.db.NextSeq, now.Format("20060102T150405Z"))
	rec := &persistedRunRecord{
		ID:         id,
		StoreRunID: s.db.NextStoreRunID,
		BrainID:    brainID,
		Prompt:     prompt,
		Status:     "running",
		Mode:       mode,
		Workdir:    workdir,
		CreatedAt:  now,
		UpdatedAt:  now,
		Events: []persistedRunEvent{{
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
	return rec.clone(), nil
}

func (s *runtimeStore) get(id string) (*persistedRunRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.index[id]
	if !ok {
		return nil, false
	}
	return rec.clone(), true
}

func (s *runtimeStore) update(id string, mutate func(*persistedRunRecord)) (*persistedRunRecord, error) {
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
	return rec.clone(), nil
}

func (s *runtimeStore) appendEvent(id, eventType, message string, data json.RawMessage) error {
	_, err := s.update(id, func(rec *persistedRunRecord) {
		rec.Events = append(rec.Events, persistedRunEvent{
			At:      s.now(),
			Type:    eventType,
			Message: message,
			Data:    append(json.RawMessage(nil), data...),
		})
	})
	return err
}

func (s *runtimeStore) setCheckpoint(id, turnUUID string) error {
	_, err := s.update(id, func(rec *persistedRunRecord) {
		rec.TurnUUID = turnUUID
	})
	return err
}

func (s *runtimeStore) setPlanID(id string, planID int64) error {
	_, err := s.update(id, func(rec *persistedRunRecord) {
		rec.PlanID = planID
	})
	return err
}

func (s *runtimeStore) finish(id, status string, result json.RawMessage, errText string) (*persistedRunRecord, error) {
	return s.update(id, func(rec *persistedRunRecord) {
		rec.Status = status
		rec.Result = append(json.RawMessage(nil), result...)
		rec.Error = errText
		rec.Events = append(rec.Events, persistedRunEvent{
			At:      s.now(),
			Type:    "run." + status,
			Message: status,
			Data:    append(json.RawMessage(nil), result...),
		})
	})
}

func (s *runtimeStore) list(limit int, state string) []*persistedRunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []*persistedRunRecord
	for _, rec := range s.db.Runs {
		if rec == nil {
			continue
		}
		if state != "" && state != "all" && rec.Status != state {
			continue
		}
		out = append(out, rec.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
