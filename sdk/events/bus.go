// Package events 提供统一的事件总线接口，用于 Dashboard SSE 推送、运行时审计、实时监控等场景。
package events

import (
	"context"
	"encoding/json"
	"time"
)

// Event 表示一个事件。
type Event struct {
	// ID 事件唯一标识，由 EventBus 实现自动生成
	ID string `json:"id"`
	// ExecutionID 关联的执行 ID，可为空
	ExecutionID string `json:"execution_id,omitempty"`
	// Type 事件类型，如 "task.started"、"task.completed"
	Type string `json:"type"`
	// Timestamp 事件产生时间
	Timestamp time.Time `json:"timestamp"`
	// Data 事件负载，JSON 编码的原始数据
	Data json.RawMessage `json:"data,omitempty"`
}

// Publisher 定义事件发布能力。
type Publisher interface {
	// Publish 发布一个事件，非阻塞（fire-and-forget）。
	Publish(ctx context.Context, ev Event)
}

// Subscriber 定义事件订阅能力。
type Subscriber interface {
	// Subscribe 订阅事件流。
	// executionID 为空字符串时订阅所有事件；否则只接收匹配的事件。
	// 返回只读 channel 和取消订阅函数；调用取消函数后 channel 会被关闭。
	Subscribe(ctx context.Context, executionID string) (<-chan Event, func())
}

// EventBus 统一事件总线接口，同时具备发布和订阅能力。
type EventBus interface {
	Publisher
	Subscriber
}
