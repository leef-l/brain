package persistence

import (
	"context"
	"sort"
	"sync"
	"time"
)

// memSharedMessageStore implements SharedMessageStore in memory.
type memSharedMessageStore struct {
	mu      sync.RWMutex
	messages []*SharedMessage
	nextID  int64
}

func (s *memSharedMessageStore) Save(ctx context.Context, msg *SharedMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	msg.ID = s.nextID
	msg.CreatedAt = time.Now()

	stored := *msg
	s.messages = append(s.messages, &stored)
	return nil
}

func (s *memSharedMessageStore) ListByBrains(ctx context.Context, from, to string, limit int) ([]*SharedMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*SharedMessage
	for _, msg := range s.messages {
		if msg.FromBrain == from && msg.ToBrain == to {
			copied := *msg
			out = append(out, &copied)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *memSharedMessageStore) ListRecent(ctx context.Context, limit int) ([]*SharedMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*SharedMessage, len(s.messages))
	for i, msg := range s.messages {
		copied := *msg
		out[i] = &copied
	}

	// Sort by CreatedAt descending
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// NewMemSharedMessageStore returns an in-memory SharedMessageStore.
func NewMemSharedMessageStore() SharedMessageStore {
	return &memSharedMessageStore{
		messages: make([]*SharedMessage, 0),
	}
}
