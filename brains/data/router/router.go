// Package router distributes DataEvents to priority-based channels.
package router

import (
	"sync/atomic"

	"github.com/leef-l/brain/brains/data/provider"
)

// EventRouter implements provider.DataSink and splits events into
// a realtime (unbuffered) channel and a near-real-time (buffered) channel.
type EventRouter struct {
	RealtimeCh chan provider.DataEvent // unbuffered
	NearRTCh   chan provider.DataEvent // buffered 1024
	metrics    struct {
		realtimeDropped atomic.Int64
	}
}

// New creates an EventRouter with the default channel capacities.
func New() *EventRouter {
	return &EventRouter{
		RealtimeCh: make(chan provider.DataEvent),
		NearRTCh:   make(chan provider.DataEvent, 1024),
	}
}

// OnEvent implements provider.DataSink. Realtime events are sent
// non-blocking to RealtimeCh; if the channel is full the event is dropped
// and the drop counter is incremented. NearRT events go to the buffered channel.
func (r *EventRouter) OnEvent(event provider.DataEvent) {
	switch event.Priority {
	case provider.PriorityRealtime:
		select {
		case r.RealtimeCh <- event:
		default:
			r.metrics.realtimeDropped.Add(1)
		}
	default:
		select {
		case r.NearRTCh <- event:
		default:
			// near-RT channel full — drop silently
		}
	}
}

// DroppedCount returns the number of realtime events dropped so far.
func (r *EventRouter) DroppedCount() int64 {
	return r.metrics.realtimeDropped.Load()
}
