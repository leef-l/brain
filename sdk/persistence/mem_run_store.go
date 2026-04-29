package persistence

import (
	"context"
	"encoding/json"
	"sync"
)

type memRunStore struct {
	mu     sync.RWMutex
	nextID int64
	runs   map[int64]*Run
	events map[int64][]RunEvent
	byKey  map[string]int64
}

func NewMemRunStore() RunStore {
	return &memRunStore{
		nextID: 1,
		runs:   make(map[int64]*Run),
		events: make(map[int64][]RunEvent),
		byKey:  make(map[string]int64),
	}
}

func (s *memRunStore) Create(ctx context.Context, run *Run) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	run.ID = id
	s.runs[id] = run
	if run.RunKey != "" {
		s.byKey[run.RunKey] = id
	}
	return id, nil
}

func (s *memRunStore) Get(ctx context.Context, id int64) (*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runs[id], nil
}

func (s *memRunStore) GetByKey(ctx context.Context, runKey string) (*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id, ok := s.byKey[runKey]; ok {
		return s.runs[id], nil
	}
	return nil, nil
}

func (s *memRunStore) Update(ctx context.Context, id int64, mutate func(*Run)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[id]; ok {
		mutate(run)
	}
	return nil
}

func (s *memRunStore) AppendEvent(ctx context.Context, id int64, ev RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[id] = append(s.events[id], ev)
	return nil
}

func (s *memRunStore) Finish(ctx context.Context, id int64, status string, result json.RawMessage, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[id]; ok {
		run.Status = status
		run.Result = result
		run.Error = errText
	}
	return nil
}

func (s *memRunStore) List(ctx context.Context, limit int, status string) ([]*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Run
	for _, r := range s.runs {
		if status == "" || r.Status == status {
			out = append(out, r)
			if len(out) >= limit && limit > 0 {
				break
			}
		}
	}
	return out, nil
}
