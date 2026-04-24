package loop

import (
	"context"

	"github.com/leef-l/brain/sdk/llm"
)

// DefaultMessageCompressor 提供不依赖 LLM 的三层消息压缩。
// 策略 1: 窗口裁剪 — 保留 system 消息 + 最新 N 条非 system 消息。
// 策略 2: 截断最老消息内容（保留前 200 字符）。
// 策略 3: 硬截断 — 只保留最近消息直到预算内。
func DefaultMessageCompressor(_ context.Context, messages []llm.Message, budget int) ([]llm.Message, error) {
	if len(messages) == 0 || budget <= 0 {
		return messages, nil
	}
	if EstimateTokens(messages) <= budget {
		return messages, nil
	}

	var systemMsgs, nonSystemMsgs []llm.Message
	for _, m := range messages {
		if m.Role == "system" {
			systemMsgs = append(systemMsgs, m)
		} else {
			nonSystemMsgs = append(nonSystemMsgs, m)
		}
	}

	systemTokens := EstimateTokens(systemMsgs)
	remaining := budget - systemTokens
	if remaining <= 0 {
		return systemMsgs, nil
	}

	// 策略 1: 窗口裁剪
	trimmed := windowTrim(nonSystemMsgs, remaining)
	if EstimateTokens(append(systemMsgs, trimmed...)) <= budget {
		return append(systemMsgs, trimmed...), nil
	}

	// 策略 2: 截断旧消息
	truncated := truncateOldMessages(trimmed, remaining)
	if EstimateTokens(append(systemMsgs, truncated...)) <= budget {
		return append(systemMsgs, truncated...), nil
	}

	// 策略 3: 硬截断
	hard := hardTruncate(nonSystemMsgs, remaining)
	return append(systemMsgs, hard...), nil
}

// EstimateTokens 粗略估算消息列表 token 数（4 字符 ≈ 1 token）。
func EstimateTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += estimateMessageTokens(m)
	}
	if total < len(messages) {
		total = len(messages)
	}
	return total
}

func estimateMessageTokens(m llm.Message) int {
	t := len(m.Role) / 4
	for _, blk := range m.Content {
		t += len(blk.Text) / 4
		t += len(blk.ToolName) / 4
		t += len(blk.Input) / 4
		t += len(blk.Output) / 4
	}
	if t < 1 {
		t = 1
	}
	return t
}

func windowTrim(msgs []llm.Message, budget int) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}
	total := 0
	startIdx := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		t := estimateMessageTokens(msgs[i])
		if total+t > budget {
			break
		}
		total += t
		startIdx = i
	}
	if startIdx >= len(msgs) {
		return msgs[len(msgs)-1:]
	}
	return msgs[startIdx:]
}

func truncateOldMessages(msgs []llm.Message, budget int) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}
	result := make([]llm.Message, len(msgs))
	copy(result, msgs)
	for i := 0; i < len(result); i++ {
		if EstimateTokens(result) <= budget {
			break
		}
		result[i] = truncateMessageContent(result[i], 200)
	}
	return result
}

func hardTruncate(msgs []llm.Message, budget int) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}
	var result []llm.Message
	total := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		truncated := truncateMessageContent(msgs[i], 100)
		t := estimateMessageTokens(truncated)
		if total+t > budget {
			break
		}
		total += t
		result = append([]llm.Message{truncated}, result...)
	}
	if len(result) == 0 && len(msgs) > 0 {
		result = []llm.Message{truncateMessageContent(msgs[len(msgs)-1], 100)}
	}
	return result
}

func truncateMessageContent(m llm.Message, limit int) llm.Message {
	newMsg := llm.Message{Role: m.Role}
	newBlocks := make([]llm.ContentBlock, len(m.Content))
	for i, blk := range m.Content {
		newBlocks[i] = blk
		if blk.Type == "text" && len(blk.Text) > limit {
			newBlocks[i].Text = blk.Text[:limit] + "[...已截断]"
		}
	}
	newMsg.Content = newBlocks
	return newMsg
}
