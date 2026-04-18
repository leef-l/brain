package events

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// 辅助函数：带超时从 channel 读取事件
func recvTimeout(ch <-chan Event, timeout time.Duration) (Event, bool) {
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(timeout):
		return Event{}, false
	}
}

// TestPublishSubscribe 发布/订阅基本流程
func TestPublishSubscribe(t *testing.T) {
	bus := NewMemEventBus()
	ctx := context.Background()

	ch, cancel := bus.Subscribe(ctx, "")
	defer cancel()

	bus.Publish(ctx, Event{
		Type: "test.event",
		Data: json.RawMessage(`{"key":"value"}`),
	})

	ev, ok := recvTimeout(ch, time.Second)
	if !ok {
		t.Fatal("未收到事件")
	}
	if ev.Type != "test.event" {
		t.Fatalf("事件类型不匹配: got %s, want test.event", ev.Type)
	}
	if ev.ID == "" {
		t.Fatal("事件 ID 不应为空")
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("事件时间戳不应为零值")
	}
}

// TestMultipleSubscribers 多订阅者都能收到事件
func TestMultipleSubscribers(t *testing.T) {
	bus := NewMemEventBus()
	ctx := context.Background()

	const n = 5
	chs := make([]<-chan Event, n)
	cancels := make([]func(), n)
	for i := 0; i < n; i++ {
		chs[i], cancels[i] = bus.Subscribe(ctx, "")
		defer cancels[i]()
	}

	bus.Publish(ctx, Event{Type: "multi.test"})

	for i, ch := range chs {
		ev, ok := recvTimeout(ch, time.Second)
		if !ok {
			t.Fatalf("订阅者 %d 未收到事件", i)
		}
		if ev.Type != "multi.test" {
			t.Fatalf("订阅者 %d 事件类型不匹配", i)
		}
	}
}

// TestExecutionIDFilter executionID 过滤
func TestExecutionIDFilter(t *testing.T) {
	bus := NewMemEventBus()
	ctx := context.Background()

	// 订阅特定 executionID
	chA, cancelA := bus.Subscribe(ctx, "exec-1")
	defer cancelA()

	// 订阅全部
	chAll, cancelAll := bus.Subscribe(ctx, "")
	defer cancelAll()

	// 发布匹配的事件
	bus.Publish(ctx, Event{Type: "matched", ExecutionID: "exec-1"})
	// 发布不匹配的事件
	bus.Publish(ctx, Event{Type: "unmatched", ExecutionID: "exec-2"})

	// chA 应该只收到 matched
	ev, ok := recvTimeout(chA, time.Second)
	if !ok || ev.Type != "matched" {
		t.Fatal("过滤订阅者应收到匹配事件")
	}
	_, ok = recvTimeout(chA, 100*time.Millisecond)
	if ok {
		t.Fatal("过滤订阅者不应收到不匹配的事件")
	}

	// chAll 应该收到两个
	ev1, ok1 := recvTimeout(chAll, time.Second)
	ev2, ok2 := recvTimeout(chAll, time.Second)
	if !ok1 || !ok2 {
		t.Fatal("全量订阅者应收到所有事件")
	}
	if ev1.Type != "matched" || ev2.Type != "unmatched" {
		t.Fatal("全量订阅者事件顺序或类型不匹配")
	}
}

// TestUnsubscribe 取消订阅后不再收到事件
func TestUnsubscribe(t *testing.T) {
	bus := NewMemEventBus()
	ctx := context.Background()

	ch, cancel := bus.Subscribe(ctx, "")
	cancel() // 立即取消

	bus.Publish(ctx, Event{Type: "after.cancel"})

	// channel 已关闭，读取应返回零值
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("取消订阅后不应收到事件")
		}
		// ok == false 表示 channel 已关闭，符合预期
	case <-time.After(100 * time.Millisecond):
		// channel 已关闭但缓冲为空，也可能走这里
		// 再确认一下 channel 是关闭状态
		select {
		case _, ok := <-ch:
			if ok {
				t.Fatal("取消订阅后不应收到事件")
			}
		default:
			t.Fatal("channel 应该已被关闭")
		}
	}
}

// TestDoubleCancel 取消函数可安全多次调用
func TestDoubleCancel(t *testing.T) {
	bus := NewMemEventBus()
	ctx := context.Background()

	_, cancel := bus.Subscribe(ctx, "")
	cancel()
	cancel() // 第二次调用不应 panic
}

// TestConcurrentSafety 并发安全测试
func TestConcurrentSafety(t *testing.T) {
	bus := NewMemEventBus()
	ctx := context.Background()

	var wg sync.WaitGroup
	const goroutines = 50
	const eventsPerGoroutine = 100

	// 启动多个订阅者
	for i := 0; i < 10; i++ {
		ch, cancel := bus.Subscribe(ctx, "")
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			count := 0
			for range ch {
				count++
				if count >= eventsPerGoroutine {
					return
				}
			}
		}()
	}

	// 并发发布
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				bus.Publish(ctx, Event{
					Type:        "concurrent.test",
					ExecutionID: "exec-concurrent",
				})
			}
		}()
	}

	// 并发订阅和取消
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, cancel := bus.Subscribe(ctx, "")
			// 读一些事件后取消
			for range 10 {
				select {
				case <-ch:
				case <-time.After(50 * time.Millisecond):
				}
			}
			cancel()
		}()
	}

	wg.Wait()
}

// TestSlowConsumerNonBlocking 慢消费者不阻塞生产者
func TestSlowConsumerNonBlocking(t *testing.T) {
	bus := NewMemEventBus()
	ctx := context.Background()

	// 订阅但不消费
	_, cancel := bus.Subscribe(ctx, "")
	defer cancel()

	// 发布超过 channel 缓冲大小的事件，不应阻塞
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberChanSize*2; i++ {
			bus.Publish(ctx, Event{Type: "flood"})
		}
		close(done)
	}()

	select {
	case <-done:
		// 正常完成
	case <-time.After(3 * time.Second):
		t.Fatal("发布不应被慢消费者阻塞")
	}
}
