package events

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// stderrW 是 drop 事件诊断日志的输出目标,默认 os.Stderr。
// 测试可替换为 bytes.Buffer 验证日志内容。
var stderrW io.Writer = os.Stderr

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

	// dropCount 累计因 channel 满而被丢弃的事件数。原子计数,
	// 不持锁可并发读写。GetDropStats 暴露给外部用于诊断。
	dropCount atomic.Uint64

	// firstDropAt 记录第一次发生 drop 的时间戳(原子写入,用 UnixNano 存)。
	// 0 表示从未发生 drop。用于诊断"什么时候开始消费跟不上"。
	firstDropAt atomic.Int64
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
			// 慢消费者,丢弃事件。记录 drop 计数 + 首次 drop 时间戳,
			// GetDropStats 暴露给上层做监控告警。日志只在第一次 drop
			// 输出 stderr(避免 spam),关键 ExecutionID 应通过
			// GetDropStats 主动查询。
			n := sub.dropCount.Add(1)
			if n == 1 {
				sub.firstDropAt.Store(time.Now().UnixNano())
				fmt.Fprintf(stderrW, "MemEventBus: subscriber dropping events (executionID=%q, first drop at %s); slow consumer — check GetDropStats\n",
					sub.executionID, time.Now().Format(time.RFC3339))
			}
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

// DropStats 报告所有订阅者的事件丢弃情况。每个 entry 对应一个仍活跃的
// 订阅(包括订阅 executionID 为空 = 全订阅 的实例)。Dropped 是该订阅
// 累计因 channel 满而被丢的事件数;FirstDropAt 是首次发生丢弃的时间
// (零值表示从未发生)。
//
// 用于诊断"事件链路是否有数据丢失" — 健康系统所有 entry 应保持
// Dropped == 0。出现非零值意味着对应的消费方处理跟不上 publish 速率,
// 可能引发 task_complete / approval 等关键事件丢失。
type DropStats struct {
	ExecutionID  string
	Dropped      uint64
	FirstDropAt  time.Time
}

// GetDropStats 返回当前所有订阅者的 drop 统计。线程安全。
// 返回 nil 表示没有任何活跃订阅。
func (b *MemEventBus) GetDropStats() []DropStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.subs) == 0 {
		return nil
	}
	out := make([]DropStats, 0, len(b.subs))
	for sub := range b.subs {
		dropped := sub.dropCount.Load()
		var t time.Time
		if ns := sub.firstDropAt.Load(); ns > 0 {
			t = time.Unix(0, ns)
		}
		out = append(out, DropStats{
			ExecutionID: sub.executionID,
			Dropped:     dropped,
			FirstDropAt: t,
		})
	}
	return out
}
