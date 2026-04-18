package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// ringCapacity 环形缓冲区容量，溢出时丢弃最旧事件
	ringCapacity = 10000
	// subscriberChanSize 订阅者 channel 缓冲大小，慢消费者丢事件而不阻塞
	subscriberChanSize = 256
)

// idCounter 全局原子计数器，用于生成事件 ID
var idCounter atomic.Uint64

// subscriber 内部订阅者结构
type subscriber struct {
	ch          chan Event
	executionID string // 为空表示订阅全部
}

// MemEventBus 基于内存的 EventBus 实现。
// 使用环形缓冲存储事件，channel 分发给订阅者。
// 线程安全，Publish 非阻塞，慢消费者丢弃事件。
type MemEventBus struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}

	// 环形缓冲
	ring    []Event
	ringIdx int // 下一个写入位置
	ringLen int // 当前已有元素数
}

// NewMemEventBus 创建一个内存事件总线实例。
func NewMemEventBus() *MemEventBus {
	return &MemEventBus{
		subs: make(map[*subscriber]struct{}),
		ring: make([]Event, ringCapacity),
	}
}

// Publish 发布事件，非阻塞。自动填充 ID 和 Timestamp（若未设置）。
func (b *MemEventBus) Publish(_ context.Context, ev Event) {
	// 自动生成 ID
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("evt-%d", idCounter.Add(1))
	}
	// 自动填充时间戳
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}

	b.mu.Lock()
	// 写入环形缓冲
	b.ring[b.ringIdx] = ev
	b.ringIdx = (b.ringIdx + 1) % ringCapacity
	if b.ringLen < ringCapacity {
		b.ringLen++
	}

	// 分发给所有订阅者（非阻塞）
	for sub := range b.subs {
		if sub.executionID != "" && sub.executionID != ev.ExecutionID {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// 慢消费者，丢弃事件
		}
	}
	b.mu.Unlock()
}

// Subscribe 订阅事件。executionID 为空时订阅所有事件。
// 返回只读 channel 和取消函数，取消后 channel 被关闭。
func (b *MemEventBus) Subscribe(_ context.Context, executionID string) (<-chan Event, func()) {
	sub := &subscriber{
		ch:          make(chan Event, subscriberChanSize),
		executionID: executionID,
	}

	b.mu.Lock()
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	// 取消订阅：移除并关闭 channel，仅执行一次
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, sub)
			b.mu.Unlock()
			close(sub.ch)
		})
	}

	return sub.ch, cancel
}
