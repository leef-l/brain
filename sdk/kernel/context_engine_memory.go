package kernel

import (
	"context"
	"fmt"
	"os"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/llm"
)

// ContextEngineWithMemory 是 DefaultContextEngine 的增强包装。
// 在 Assemble 流程中自动注入项目记忆，让 LLM 能够参考项目历史经验和关键决策。
//
// 工作原理：
//   - Assemble 时，如果 AssembleRequest.ProjectID 非空且 memory 不为 nil，
//     调用 ProjectMemory.Summarize 生成项目记忆摘要，作为 system 消息前置注入。
//   - maxTokens 取 TokenBudget 的 15%（上限 2000），避免记忆占用过多上下文窗口。
//   - Compress / Share 等方法直接委托给内部 engine。
type ContextEngineWithMemory struct {
	engine    *DefaultContextEngine
	memory    ProjectMemory
	retriever *MemoryRetriever
}

// memoryTokenCap 是记忆摘要的 token 上限。
const memoryTokenCap = 2000

// memoryTokenRatio 是记忆摘要占 TokenBudget 的比例。
const memoryTokenRatio = 0.15

// NewContextEngineWithMemory 创建带项目记忆注入的 ContextEngine。
// engine 不能为 nil；memory 为 nil 时退化为普通 engine 行为。
func NewContextEngineWithMemory(engine *DefaultContextEngine, memory ProjectMemory) *ContextEngineWithMemory {
	return &ContextEngineWithMemory{
		engine:    engine,
		memory:    memory,
		retriever: NewMemoryRetriever(),
	}
}

// Assemble 在调用原始 engine.Assemble 之前，注入项目记忆上下文。
//
// 流程：
//  1. 如果 ProjectID 非空且 memory 不为 nil，调用 memory.Summarize 获取项目摘要。
//     maxTokens = min(TokenBudget * 15%, 2000)。
//  2. 将摘要包装为 system 消息，前置到 req.Messages 中。
//  3. 委托给 engine.Assemble 完成后续装配（含 token 压缩）。
func (c *ContextEngineWithMemory) Assemble(ctx context.Context, req AssembleRequest) ([]llm.Message, error) {
	// 注入项目记忆
	if req.ProjectID != "" && c.memory != nil {
		maxTokens := int(float64(req.TokenBudget) * memoryTokenRatio)
		if maxTokens > memoryTokenCap {
			maxTokens = memoryTokenCap
		}
		if maxTokens <= 0 {
			// TokenBudget 为 0 或负数时使用上限值
			maxTokens = memoryTokenCap
		}

		summary, err := c.memory.Summarize(ctx, req.ProjectID, maxTokens)
		if err != nil {
			// 记忆获取失败不阻断主流程，仅在 stderr 报告
			fmt.Fprintf(os.Stderr, "contextEngine: project memory summarize error: %v\n", err)
		} else if summary != "" {
			memoryMsg := llm.Message{
				Role: "system",
				Content: []llm.ContentBlock{{
					Type: "text",
					Text: "[项目记忆] " + summary,
				}},
			}
			// 前置到消息列表
			req.Messages = append([]llm.Message{memoryMsg}, req.Messages...)
		}
	}

	return c.engine.Assemble(ctx, req)
}

// Compress 委托给内部 engine。
func (c *ContextEngineWithMemory) Compress(ctx context.Context, messages []llm.Message, budget int) ([]llm.Message, error) {
	return c.engine.Compress(ctx, messages, budget)
}

// Share 委托给内部 engine。
func (c *ContextEngineWithMemory) Share(ctx context.Context, from, to agent.Kind, messages []llm.Message) error {
	return c.engine.Share(ctx, from, to, messages)
}

// SharedFor 委托给内部 engine。返回 (from, to) 桶中的消息拷贝。
func (c *ContextEngineWithMemory) SharedFor(from, to agent.Kind) []llm.Message {
	return c.engine.SharedFor(from, to)
}

// ClearShared 委托给内部 engine。切断 (from, to) 桶。
func (c *ContextEngineWithMemory) ClearShared(from, to agent.Kind) {
	c.engine.ClearShared(from, to)
}

// Memory 返回内部的 ProjectMemory 实例（可用于外部直接操作记忆）。
func (c *ContextEngineWithMemory) Memory() ProjectMemory {
	return c.memory
}

// Retriever 返回内部的 MemoryRetriever 实例（可用于自定义检索）。
func (c *ContextEngineWithMemory) Retriever() *MemoryRetriever {
	return c.retriever
}

// Engine 返回内部的 DefaultContextEngine（用于需要访问底层 engine 的场景）。
func (c *ContextEngineWithMemory) Engine() *DefaultContextEngine {
	return c.engine
}
