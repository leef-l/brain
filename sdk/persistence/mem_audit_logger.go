package persistence

import (
	"context"
	"sync"
	"time"
)

// memAuditLogger implements AuditLogger in memory.
type memAuditLogger struct {
	mu     sync.RWMutex
	events []*AuditEvent
	nextID int64
}

func (s *memAuditLogger) Log(ctx context.Context, ev *AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	ev.ID = s.nextID
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	// Store a copy
	stored := *ev
	s.events = append(s.events, &stored)
	return nil
}

func (s *memAuditLogger) Query(ctx context.Context, filter AuditFilter) ([]*AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*AuditEvent
	for _, ev := range s.events {
		if filter.ExecutionID != "" && ev.ExecutionID != filter.ExecutionID {
			continue
		}
		if filter.EventType != "" && ev.EventType != filter.EventType {
			continue
		}
		if filter.Actor != "" && ev.Actor != filter.Actor {
			continue
		}
		if !filter.Since.IsZero() && ev.Timestamp.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && ev.Timestamp.After(filter.Until) {
			continue
		}
		copied := *ev
		out = append(out, &copied)
	}

	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *memAuditLogger) Purge(ctx context.Context, olderThanDays int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().AddDate(0, 0, -olderThanDays)
	var kept []*AuditEvent
	var deleted int64
	for _, ev := range s.events {
		if ev.Timestamp.Before(cutoff) {
			deleted++
		} else {
			kept = append(kept, ev)
		}
	}
	s.events = kept
	return deleted, nil
}

// NewMemAuditLogger returns an in-memory AuditLogger.
func NewMemAuditLogger() AuditLogger {
	return &memAuditLogger{
		events: make([]*AuditEvent, 0),
		nextID: 0,
	}
}
