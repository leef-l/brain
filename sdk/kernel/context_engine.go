// Context Engine — 上下文装配层
//
// 负责三件事：
//   1. Assemble：根据 brain 类型、任务类型、token 预算装配上下文
//   2. Compress：长对话 token 爆炸时压缩（窗口裁剪 → 截断 → 硬截断）
//   3. Share：跨脑上下文传递（隐私过滤 + 数量限制）
//
// 设计详见 sdk/docs/35-Context-Engine详细设计.md
package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/persistence"
)

// ---------------------------------------------------------------------------
// 请求 & 接口
// ---------------------------------------------------------------------------

// AssembleRequest 是 Assemble() 的入参。
type AssembleRequest struct {
	// RunID 是当前 TaskExecution 的唯一 ID。
	RunID string
	// BrainKind 是发起请求的 brain 类型。
	BrainKind agent.Kind
	// TaskType 描述当前任务的语义类型（如 "analysis"、"execution"）。
	TaskType string
	// Messages 是原始消息列表（L3 History）。
	Messages []llm.Message
	// TokenBudget 是本次 Assemble 允许使用的最大 token 数。
	// 0 表示不限制。
	TokenBudget int
	// ProjectID 关联项目级记忆。非空时，ContextEngine 会自动加载该项目的
	// 历史对话并前置到当前消息列表中，实现跨 Run 的上下文继承。
	ProjectID string
}

// ContextEngine 是上下文装配层的主接口。
type ContextEngine interface {
	// Assemble 根据请求返回装配好的消息列表。
	// 当消息 token 数超过预算时自动调用 Compress 压缩。
	Assemble(ctx context.Context, req AssembleRequest) ([]llm.Message, error)

	// Compress 对消息列表执行三层压缩策略：
	//   1. 窗口裁剪：保留最新 N 条（system 消息不被裁掉）
	//   2. 截断最老的消息内容
	//   3. 硬截断兜底
	Compress(ctx context.Context, messages []llm.Message, budget int) ([]llm.Message, error)

	// Share 将 from brain 的上下文传递给 to brain。
	// 实现隐私过滤 + 限制最多 10 条消息。
	Share(ctx context.Context, from, to agent.Kind, messages []llm.Message) error
}

// ---------------------------------------------------------------------------
// 默认实现
// ---------------------------------------------------------------------------

// DefaultContextEngine 是 ContextEngine 的默认实现。
type DefaultContextEngine struct {
	// SharedMessages 保留为 Deprecated 字段,仅反映"最近一次 Share() 调用"。
	// 旧代码有读这个字段的测试,为不破坏 API 保留;但实际生产路径(多 delegate
	// 并发)应读 ShareFor(from, to) 而不是读这个字段。
	//
	// Task #18:多 delegate 并发时这个字段会被后来的 Share() 覆盖,产生跨脑
	// 上下文串。真正的消息按 (from, to) 写进 sharedBuckets;Delegate 边界
	// 切断时,Orchestrator 必须调 ClearShared(from, to) 显式回收桶,防止
	// 下一次 delegate 继承前一次的 shared 消息。
	SharedMessages []llm.Message

	// Summarizer 是可选的 LLM provider，用于 Compress 阶段 2.5 的智能摘要。
	// 当为 nil 时，Compress 退化为纯截断策略。
	Summarizer llm.Provider

	// SummaryModel 指定摘要使用的模型。为空时使用 provider 默认模型。
	SummaryModel string

	// SharedStore 是可选的持久化后端，用于保存 Share() 的跨脑消息。
	SharedStore persistence.SharedMessageStore

	// ProjectStore 是可选的项目级记忆存储。非 nil 时，Assemble() 会根据
	// AssembleRequest.ProjectID 自动加载项目历史对话，实现跨 Run 上下文继承。
	ProjectStore persistence.ProjectStore

	// MaxShareMessages 是 Share() 传递的最大消息条数。零值回退为默认 30。
	MaxShareMessages int

	// ShareTokenBudget 是 Share() 传递消息的 token 上限。零值回退为 8000。
	// 当消息总 token 超过此预算时，从最老的消息开始裁剪。
	ShareTokenBudget int

	// sharedBuckets 是按 (from, to) 分桶的跨脑消息存储,解决多 delegate 并发
	// 时 SharedMessages 相互覆盖的问题(Task #18)。
	sharedMu      sync.Mutex
	sharedBuckets map[sharedKey][]llm.Message
}

// sharedKey 标识一对 (from, to) brain 通信通道。
type sharedKey struct {
	from agent.Kind
	to   agent.Kind
}

// NewDefaultContextEngine 创建默认的 ContextEngine。
func NewDefaultContextEngine() *DefaultContextEngine {
	return &DefaultContextEngine{}
}

// NewContextEngineWithLLM 创建带 LLM 摘要能力的 ContextEngine。
func NewContextEngineWithLLM(provider llm.Provider, model string) *DefaultContextEngine {
	return &DefaultContextEngine{
		Summarizer:   provider,
		SummaryModel: model,
	}
}

// estimateTokens 粗略估算消息列表的 token 数。
// 按 4 字符 ≈ 1 token 估算，不依赖外部 tokenizer。
func estimateTokens(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		// 角色名本身也占 token
		total += len(m.Role) / 4
		for _, blk := range m.Content {
			total += len(blk.Text) / 4
			total += len(blk.ToolName) / 4
			total += len(blk.Input) / 4
			total += len(blk.Output) / 4
		}
	}
	// 至少返回消息数量（每条消息至少 1 token 开销）
	if total < len(messages) {
		total = len(messages)
	}
	return total
}

// estimateMessageTokens 估算单条消息的 token 数。
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

// isSystemMessage 判断消息是否为 system 角色（不可被裁掉）。
func isSystemMessage(m llm.Message) bool {
	return m.Role == "system"
}

// Assemble 根据请求装配消息。
// 当 token 数超过预算时自动压缩。
// 如果 ProjectID 非空且 ProjectStore 已配置，会自动加载项目历史对话并
// 前置到当前消息列表中，实现跨 Run 的上下文继承。
func (e *DefaultContextEngine) Assemble(ctx context.Context, req AssembleRequest) ([]llm.Message, error) {
	messages := req.Messages

	// ─── Project Memory Loading ────────────────────────────────────────────
	if req.ProjectID != "" && e.ProjectStore != nil {
		history, err := e.ProjectStore.LoadMessages(ctx, req.ProjectID, 20)
		if err == nil && len(history) > 0 {
			messages = append(history, messages...)
		}
	}

	if len(messages) == 0 {
		return nil, nil
	}

	// 无预算限制，原样返回
	if req.TokenBudget <= 0 {
		return messages, nil
	}

	// 估算当前 token 数
	tokens := estimateTokens(messages)
	if tokens <= req.TokenBudget {
		// 未超限，原样返回
		return messages, nil
	}

	// 超限，调用压缩
	return e.Compress(ctx, messages, req.TokenBudget)
}

// Compress 执行三层压缩策略。
//
// 策略 1：窗口裁剪 — 保留所有 system 消息 + 最新 N 条非 system 消息。
// 策略 2：截断最老的非 system 消息内容（保留前 200 字符）。
// 策略 3：硬截断 — 只保留 system 消息 + 最近若干条消息直到预算内。
func (e *DefaultContextEngine) Compress(ctx context.Context, messages []llm.Message, budget int) ([]llm.Message, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	if budget <= 0 {
		return messages, nil
	}

	// 分离 system 消息和非 system 消息
	var systemMsgs []llm.Message
	var nonSystemMsgs []llm.Message
	for _, m := range messages {
		if isSystemMessage(m) {
			systemMsgs = append(systemMsgs, m)
		} else {
			nonSystemMsgs = append(nonSystemMsgs, m)
		}
	}

	// --- 策略 1：窗口裁剪 ---
	// 计算 system 消息占用的 token
	systemTokens := estimateTokens(systemMsgs)
	remainingBudget := budget - systemTokens
	if remainingBudget <= 0 {
		// system 消息已经超预算，只能返回 system 消息
		return systemMsgs, nil
	}

	// 从最新的消息开始，往前保留直到 token 用完
	result := windowTrim(nonSystemMsgs, remainingBudget)
	combined := append(systemMsgs, result...)
	if estimateTokens(combined) <= budget {
		return combined, nil
	}

	// --- 策略 2：截断最老的消息内容 ---
	truncated := truncateOldMessages(result, remainingBudget)
	combined = append(systemMsgs, truncated...)
	if estimateTokens(combined) <= budget {
		return combined, nil
	}

	// --- 策略 2.5：LLM 摘要（当有 Summarizer 时） ---
	if e.Summarizer != nil {
		summarized, err := e.summarizeMessages(ctx, truncated, remainingBudget)
		if err == nil {
			combined = append(systemMsgs, summarized...)
			if estimateTokens(combined) <= budget {
				return combined, nil
			}
		}
	}

	// --- 策略 3：硬截断兜底 ---
	hardResult := hardTruncate(nonSystemMsgs, remainingBudget)
	return append(systemMsgs, hardResult...), nil
}

// windowTrim 保留最新的消息，使总 token 不超过 budget。
func windowTrim(msgs []llm.Message, budget int) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}

	// 从尾部往前累加，找到能放下的起始位置
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
		// 连最后一条都放不下，至少返回最后一条
		return msgs[len(msgs)-1:]
	}
	return msgs[startIdx:]
}

// truncateOldMessages 截断列表中较老消息的内容。
// 保留前 200 字符，超出部分用 "[...已截断]" 替代。
func truncateOldMessages(msgs []llm.Message, budget int) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}

	const truncateLimit = 200

	result := make([]llm.Message, len(msgs))
	copy(result, msgs)

	// 从最老的消息开始截断
	for i := 0; i < len(result); i++ {
		if estimateTokens(result) <= budget {
			break
		}
		// 截断这条消息的文本内容
		truncated := truncateMessageContent(result[i], truncateLimit)
		result[i] = truncated
	}
	return result
}

// truncateMessageContent 截断单条消息的文本内容块。
func truncateMessageContent(m llm.Message, limit int) llm.Message {
	newMsg := llm.Message{Role: m.Role}
	newBlocks := make([]llm.ContentBlock, len(m.Content))
	for i, blk := range m.Content {
		newBlocks[i] = blk
		if (blk.Type == "text" || blk.Type == "thinking") && len(blk.Text) > limit {
			newBlocks[i].Text = blk.Text[:limit] + "[...已截断]"
		}
	}
	newMsg.Content = newBlocks
	return newMsg
}

// hardTruncate 硬截断兜底：只保留能放入预算的最新消息。
func hardTruncate(msgs []llm.Message, budget int) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}

	// 从尾部取消息，每条消息硬截断到最多 100 字符
	const hardLimit = 100
	var result []llm.Message
	total := 0

	for i := len(msgs) - 1; i >= 0; i-- {
		truncated := truncateMessageContent(msgs[i], hardLimit)
		t := estimateMessageTokens(truncated)
		if total+t > budget {
			break
		}
		total += t
		result = append([]llm.Message{truncated}, result...)
	}

	// 至少返回最后一条
	if len(result) == 0 && len(msgs) > 0 {
		last := truncateMessageContent(msgs[len(msgs)-1], hardLimit)
		result = []llm.Message{last}
	}
	return result
}

// summarizeMessages 使用 LLM 将多条消息压缩为一条摘要消息。
// 保留最新的 keepRecent 条消息原样，其余消息由 LLM 摘要。
func (e *DefaultContextEngine) summarizeMessages(ctx context.Context, msgs []llm.Message, budget int) ([]llm.Message, error) {
	if len(msgs) <= 2 {
		return msgs, nil
	}

	// 保留最新 2 条原样，其余喂给 LLM 做摘要
	keepRecent := 2
	if keepRecent > len(msgs) {
		keepRecent = len(msgs)
	}
	toSummarize := msgs[:len(msgs)-keepRecent]
	recent := msgs[len(msgs)-keepRecent:]

	// 拼接要摘要的消息文本
	var sb strings.Builder
	for _, m := range toSummarize {
		sb.WriteString("[")
		sb.WriteString(m.Role)
		sb.WriteString("] ")
		sb.WriteString(messageText(m))
		sb.WriteString("\n")
	}

	summaryPrompt := "请将以下对话历史压缩为一段简洁的摘要，保留关键信息、决策和结论。" +
		"用中文输出，不超过 500 字。\n\n" + sb.String()

	model := e.SummaryModel
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}

	resp, err := e.Summarizer.Complete(ctx, &llm.ChatRequest{
		Model:     model,
		MaxTokens: 1024,
		Messages: []llm.Message{{
			Role:    "user",
			Content: []llm.ContentBlock{{Type: "text", Text: summaryPrompt}},
		}},
	})
	if err != nil {
		return nil, err
	}

	summaryText := ""
	if resp != nil && len(resp.Content) > 0 {
		summaryText = resp.Content[0].Text
	}
	if summaryText == "" {
		return msgs, nil
	}

	summaryMsg := llm.Message{
		Role: "assistant",
		Content: []llm.ContentBlock{{
			Type: "text",
			Text: "[上下文摘要] " + summaryText,
		}},
	}

	result := append([]llm.Message{summaryMsg}, recent...)
	return result, nil
}

// ---------------------------------------------------------------------------
// Share — 跨脑上下文传递
// ---------------------------------------------------------------------------

// credentialPattern 匹配敏感凭证关键词。
var credentialPattern = regexp.MustCompile(`(?i)(api_key|password|secret|token\s*=|private_key|credential)`)

// Share 将 from brain 的上下文传递给 to brain。
// 规则：
//   - 隐私过滤：剔除包含凭证关键词的消息
//   - 数量限制：最多传递 10 条消息（取最新的）
//   - 按 (from, to) 分桶存储，支持多 delegate 并发（Task #18）
//
// 调用方(Orchestrator)在 delegate 结束后 SHOULD 调 ClearShared(from, to)
// 切断桶，防止下一次 delegate 继承前一次的 shared 消息。
func (e *DefaultContextEngine) Share(ctx context.Context, from, to agent.Kind, messages []llm.Message) error {
	if len(messages) == 0 {
		e.sharedMu.Lock()
		if e.sharedBuckets != nil {
			delete(e.sharedBuckets, sharedKey{from, to})
		}
		e.SharedMessages = nil
		e.sharedMu.Unlock()
		return nil
	}

	// 隐私过滤
	var filtered []llm.Message
	for _, m := range messages {
		if containsCredential(m) {
			continue
		}
		filtered = append(filtered, m)
	}

	// 数量限制
	maxMsg := e.MaxShareMessages
	if maxMsg <= 0 {
		maxMsg = 30
	}
	if len(filtered) > maxMsg {
		filtered = filtered[len(filtered)-maxMsg:]
	}
	// token 预算裁剪：从最新消息往前保留，超预算的老消息丢弃
	tokenBudget := e.ShareTokenBudget
	if tokenBudget <= 0 {
		tokenBudget = 8000
	}
	if estimateTokens(filtered) > tokenBudget {
		filtered = windowTrim(filtered, tokenBudget)
	}

	e.sharedMu.Lock()
	if e.sharedBuckets == nil {
		e.sharedBuckets = map[sharedKey][]llm.Message{}
	}
	e.sharedBuckets[sharedKey{from, to}] = filtered
	// 兼容旧字段:后写覆盖,仅给 legacy 测试和单桶读取场景用
	e.SharedMessages = filtered
	e.sharedMu.Unlock()

	// 异步持久化
	if e.SharedStore != nil && len(filtered) > 0 {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "contextEngine: persist panic: %v\n", r)
				}
			}()
			data, err := json.Marshal(filtered)
			if err != nil {
				return
			}
			e.SharedStore.Save(context.Background(), &persistence.SharedMessage{
				FromBrain: string(from),
				ToBrain:   string(to),
				Messages:  data,
				Count:     len(filtered),
			})
		}()
	}
	return nil
}

// SharedFor 返回 (from, to) 桶中的消息拷贝。无桶返回 nil。
// 线程安全;调用方不应共享返回 slice(不锁保护)。
func (e *DefaultContextEngine) SharedFor(from, to agent.Kind) []llm.Message {
	e.sharedMu.Lock()
	defer e.sharedMu.Unlock()
	msgs, ok := e.sharedBuckets[sharedKey{from, to}]
	if !ok {
		return nil
	}
	out := make([]llm.Message, len(msgs))
	copy(out, msgs)
	return out
}

// ClearShared 切断 (from, to) 桶。Orchestrator.Delegate 在 subtask 完成后
// 必须调用本方法,否则下一次 delegate 会继承前一次的 shared 消息(Task #18)。
// from/to 都为零值时清空全部桶——仅应在 engine 重置时使用。
func (e *DefaultContextEngine) ClearShared(from, to agent.Kind) {
	e.sharedMu.Lock()
	defer e.sharedMu.Unlock()
	if from == "" && to == "" {
		e.sharedBuckets = nil
		e.SharedMessages = nil
		return
	}
	delete(e.sharedBuckets, sharedKey{from, to})
}

// containsCredential 检查消息是否包含敏感凭证信息。
func containsCredential(m llm.Message) bool {
	for _, blk := range m.Content {
		if credentialPattern.MatchString(blk.Text) {
			return true
		}
		// 检查工具输入输出中的凭证
		if credentialPattern.Match(blk.Input) {
			return true
		}
		if credentialPattern.Match(blk.Output) {
			return true
		}
	}
	return false
}

// messageText 提取消息中所有文本内容（用于调试和测试）。
func messageText(m llm.Message) string {
	var parts []string
	for _, blk := range m.Content {
		if blk.Text != "" {
			parts = append(parts, blk.Text)
		}
	}
	return strings.Join(parts, " ")
}
