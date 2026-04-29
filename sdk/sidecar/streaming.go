package sidecar

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/leef-l/brain/sdk/llm"
)

var (
	streamMu    sync.RWMutex
	streamChans = make(map[string]chan<- llm.StreamEvent)
	streamSeq   int64
)

// generateStreamID returns a unique stream identifier.
func generateStreamID() string {
	n := atomic.AddInt64(&streamSeq, 1)
	return fmt.Sprintf("stream-%d", n)
}

// registerStreamChan registers a channel to receive delta events for a stream.
func registerStreamChan(id string, ch chan<- llm.StreamEvent) {
	streamMu.Lock()
	defer streamMu.Unlock()
	streamChans[id] = ch
}

// unregisterStreamChan removes a stream channel from the registry.
func unregisterStreamChan(id string) {
	streamMu.Lock()
	defer streamMu.Unlock()
	delete(streamChans, id)
}

// pushStreamEvent delivers a delta event to the registered stream channel.
// It is best-effort: if the channel is full the event is dropped.
func pushStreamEvent(id string, ev llm.StreamEvent) {
	streamMu.RLock()
	ch, ok := streamChans[id]
	streamMu.RUnlock()
	if !ok {
		return
	}
	select {
	case ch <- ev:
	default:
		// channel full — drop event to avoid blocking the host read loop
	}
}

// channelStreamReader implements llm.StreamReader backed by a Go channel.
// Events are pushed in real time from the host via llm/stream/delta notifications.
type channelStreamReader struct {
	ch     <-chan llm.StreamEvent
	closed bool
}

func (r *channelStreamReader) Next(ctx context.Context) (llm.StreamEvent, error) {
	if r.closed {
		return llm.StreamEvent{}, fmt.Errorf("stream closed")
	}
	select {
	case ev, ok := <-r.ch:
		if !ok {
			return llm.StreamEvent{}, io.EOF
		}
		return ev, nil
	case <-ctx.Done():
		return llm.StreamEvent{}, ctx.Err()
	}
}

func (r *channelStreamReader) Close() error {
	r.closed = true
	return nil
}
