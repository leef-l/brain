package chat

import (
	"context"
	"fmt"
	"sync"

	"github.com/leef-l/brain/sdk/tool"
)

// ChatHumanCoordinator 是 chat REPL 场景的 HumanTakeoverCoordinator 实现。
//
// serve 模式走 HTTP /v1/executions/{id}/resume|abort,chat 模式没有 HTTP
// 端点,需要一个走内存 channel + slash 命令的轻量协调器:
//   - Agent 调 human.request_takeover -> RequestTakeover 阻塞在 pending 的
//     channel 上
//   - 通过 events 频道广播 TakeoverEvent 让 chat REPL 主循环收到后
//     DetachPromptFrame + 打印求助提示(reason / url / guidance)
//   - 用户在浏览器里完成操作 -> chat 输入 /resume 或 /abort
//   - Resume/Abort 写 channel -> Agent 拿到 outcome 继续
//
// 同一时间只支持一个 pending takeover(chat 是单线程串行对话,够用)。
type ChatHumanCoordinator struct {
	mu      sync.Mutex
	pending *pendingTakeover
	events  chan TakeoverEvent
}

// TakeoverEvent 是 chat REPL 主循环订阅的事件,用来触发屏幕提示 +
// 清 prompt frame。
type TakeoverEvent struct {
	Kind     string                      // "requested" / "resolved"
	Request  tool.HumanTakeoverRequest   // 仅 requested 有效
	Response tool.HumanTakeoverResponse  // 仅 resolved 有效
}

type pendingTakeover struct {
	req      tool.HumanTakeoverRequest
	respCh   chan tool.HumanTakeoverResponse
	notified bool
}

// NewChatHumanCoordinator 构造一个空闲协调器。
func NewChatHumanCoordinator() *ChatHumanCoordinator {
	return &ChatHumanCoordinator{
		events: make(chan TakeoverEvent, 4),
	}
}

// Events 返回只读事件 channel,chat REPL 主循环 select 它,收到 requested
// 时 DetachPromptFrame + 打印求助提示,收到 resolved 时清理提示。
func (c *ChatHumanCoordinator) Events() <-chan TakeoverEvent {
	return c.events
}

func (c *ChatHumanCoordinator) emit(ev TakeoverEvent) {
	select {
	case c.events <- ev:
	default:
	}
}

// RequestTakeover 实现 tool.HumanTakeoverCoordinator 接口。
// 阻塞直到 Resume/Abort/ctx 取消(ctx 超时对应 aborted+"timed out")。
func (c *ChatHumanCoordinator) RequestTakeover(ctx context.Context, req tool.HumanTakeoverRequest) tool.HumanTakeoverResponse {
	respCh := make(chan tool.HumanTakeoverResponse, 1)
	c.mu.Lock()
	// 如果已有 pending,直接把旧的标记为 aborted(理论上不会发生,chat 串行)
	if old := c.pending; old != nil {
		select {
		case old.respCh <- tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: "superseded"}:
		default:
		}
	}
	c.pending = &pendingTakeover{req: req, respCh: respCh}
	c.mu.Unlock()

	// 通知 chat REPL 主循环:DetachPromptFrame + 打印求助提示。
	c.emit(TakeoverEvent{Kind: "requested", Request: req})

	var resp tool.HumanTakeoverResponse
	select {
	case resp = <-respCh:
	case <-ctx.Done():
		note := "agent context canceled"
		if ctx.Err() == context.DeadlineExceeded {
			note = "timed out waiting for human"
		}
		resp = tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: note}
	}

	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()

	c.emit(TakeoverEvent{Kind: "resolved", Response: resp})
	return resp
}

// Pending 返回当前是否有正在等待的 takeover(供 slash 命令判断)。
func (c *ChatHumanCoordinator) Pending() (tool.HumanTakeoverRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return tool.HumanTakeoverRequest{}, false
	}
	return c.pending.req, true
}

// Resume 由 /resume 命令调用,返回 true 表示成功送达。
func (c *ChatHumanCoordinator) Resume(note string) bool {
	return c.deliver(tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeResumed, Note: note})
}

// Abort 由 /abort 命令调用,返回 true 表示成功送达。
func (c *ChatHumanCoordinator) Abort(note string) bool {
	return c.deliver(tool.HumanTakeoverResponse{Outcome: tool.HumanOutcomeAborted, Note: note})
}

func (c *ChatHumanCoordinator) deliver(resp tool.HumanTakeoverResponse) bool {
	c.mu.Lock()
	p := c.pending
	c.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case p.respCh <- resp:
		return true
	default:
		return false
	}
}

// PrintRequested 供 REPL 主循环收到 requested 事件后调用:在已
// Detach 的屏幕上打印求助提示,引导用户 /resume 或 /abort。
func PrintRequested(req tool.HumanTakeoverRequest) {
	fmt.Println()
	fmt.Println("  \033[1;33m✋ Browser needs human help\033[0m")
	if req.Reason != "" {
		fmt.Printf("     reason:   %s\n", req.Reason)
	}
	if req.URL != "" {
		fmt.Printf("     page:     %s\n", req.URL)
	}
	if req.Guidance != "" {
		fmt.Printf("     guidance: %s\n", req.Guidance)
	}
	fmt.Println("     Finish in the browser window, then type \033[1m/resume\033[0m.")
	fmt.Println("     Or type \033[1m/abort\033[0m to give up this step.")
	fmt.Println()
}

// PrintResolved 供 REPL 主循环收到 resolved 事件后调用:提示用户
// takeover 已经返回,Agent 继续往下走。
func PrintResolved(resp tool.HumanTakeoverResponse) {
	switch resp.Outcome {
	case tool.HumanOutcomeResumed:
		fmt.Println("  \033[32m✓ Takeover resumed — agent continuing\033[0m")
	case tool.HumanOutcomeAborted:
		note := resp.Note
		if note == "" {
			note = "aborted"
		}
		fmt.Printf("  \033[33m✗ Takeover aborted: %s\033[0m\n", note)
	}
	fmt.Println()
}
