package runtimeaudit

import (
	"context"
	"encoding/json"

	"github.com/leef-l/brain/sdk/events"
)

// EventBusSink bridges runtimeaudit events into the global EventBus.
// When attached via WithSink, every Emit() call also publishes to the EventBus.
type EventBusSink struct {
	Bus         events.Publisher
	ExecutionID string
}

func (s *EventBusSink) AppendEvent(ctx context.Context, ev Event) {
	if s == nil || s.Bus == nil {
		return
	}
	s.Bus.Publish(ctx, events.Event{
		ExecutionID: s.ExecutionID,
		Type:        ev.Type,
		Data:        append(json.RawMessage(nil), ev.Data...),
	})
}
