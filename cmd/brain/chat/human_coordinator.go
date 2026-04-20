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
//   - REPL 打印 takeover 提示(reason / url / guidance)让用户知道怎么接管
//   - 用户在 brain 浏览器里完成操作 -> 回 chat 输入 /resume 或 /abort
//   - Resume/Abort 写 channel -> Agent 拿到 outcome 继续
//
// 同一时间只支持一个 pending takeover(chat 是单线程串行对话,够用)。
type ChatHumanCoordinator struct {
	mu      sync.Mutex
	pending *pendingTakeover
}

type pendingTakeover struct {
	req      tool.HumanTakeoverRequest
	respCh   chan tool.HumanTakeoverResponse
	notified bool
}

// NewChatHumanCoordinator 构造一个空闲协调器。
func NewChatHumanCoordinator() *ChatHumanCoordinator {
	return &ChatHumanCoordinator{}
}

// RequestTakeover 实现 tool.HumanTakeoverCoordinator 接口。
// 阻塞直到 Resume/Abort/ctx 取消。
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
	defer func() {
		c.mu.Lock()
		c.pending = nil
		c.mu.Unlock()
	}()

	// 打印提示给用户,引导怎么接管。
	c.printPrompt(req)

	select {
	case resp := <-respCh:
		return resp
	case <-ctx.Done():
		return tool.HumanTakeoverResponse{
			Outcome: tool.HumanOutcomeAborted,
			Note:    "agent context canceled",
		}
	}
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

func (c *ChatHumanCoordinator) printPrompt(req tool.HumanTakeoverRequest) {
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
