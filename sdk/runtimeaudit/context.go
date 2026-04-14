package runtimeaudit

import (
	"context"
	"encoding/json"
)

type Event struct {
	Type    string
	Message string
	Data    json.RawMessage
}

type Sink interface {
	AppendEvent(ctx context.Context, ev Event)
}

type SinkFunc func(context.Context, Event)

func (f SinkFunc) AppendEvent(ctx context.Context, ev Event) {
	if f != nil {
		f(ctx, ev)
	}
}

type sinkKey struct{}

func WithSink(ctx context.Context, sink Sink) context.Context {
	if ctx == nil || sink == nil {
		return ctx
	}
	return context.WithValue(ctx, sinkKey{}, sink)
}

func Emit(ctx context.Context, ev Event) {
	if ctx == nil {
		return
	}
	sink, _ := ctx.Value(sinkKey{}).(Sink)
	if sink == nil {
		return
	}
	sink.AppendEvent(ctx, ev)
}
