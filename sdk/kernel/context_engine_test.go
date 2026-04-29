package kernel

import (
	"context"
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/llm"
)

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// textMsg 创建一条简单的文本消息。
func textMsg(role, text string) llm.Message {
	return llm.Message{
		Role: role,
		Content: []llm.ContentBlock{
			{Type: "text", Text: text},
		},
	}
}

// systemMsg 创建一条 system 消息。
func systemMsg(text string) llm.Message {
	return textMsg("system", text)
}

// ---------------------------------------------------------------------------
// Assemble 测试
// ---------------------------------------------------------------------------

func TestAssemble_WithinBudget_ReturnsOriginal(t *testing.T) {
	// 不超限时应原样返回
	eng := NewDefaultContextEngine()
	msgs := []llm.Message{
		textMsg("user", "hello"),
		textMsg("assistant", "hi there"),
	}

	result, err := eng.Assemble(context.Background(), AssembleRequest{
		RunID:       "run-1",
		BrainKind:   agent.KindCentral,
		TaskType:    "conversation",
		Messages:    msgs,
		TokenBudget: 10000, // 远超实际 token
	})
	if err != nil {
		t.Fatalf("Assemble 返回错误: %v", err)
	}
	if len(result) != len(msgs) {
		t.Fatalf("期望 %d 条消息，得到 %d 条", len(msgs), len(result))
	}
	for i := range msgs {
		if result[i].Role != msgs[i].Role {
			t.Errorf("消息 %d: 期望 role=%s，得到 %s", i, msgs[i].Role, result[i].Role)
		}
	}
}

func TestAssemble_ExceedsBudget_TriggersCompress(t *testing.T) {
	// 超限时应触发压缩，返回更少的消息
	eng := NewDefaultContextEngine()

	// 创建大量消息
	var msgs []llm.Message
	for i := 0; i < 50; i++ {
		msgs = append(msgs, textMsg("user", strings.Repeat("x", 100)))
		msgs = append(msgs, textMsg("assistant", strings.Repeat("y", 100)))
	}

	totalTokens := estimateTokens(msgs)
	// 设置一个比总 token 小的预算
	budget := totalTokens / 3

	result, err := eng.Assemble(context.Background(), AssembleRequest{
		RunID:       "run-2",
		BrainKind:   agent.KindQuant,
		TaskType:    "analysis",
		Messages:    msgs,
		TokenBudget: budget,
	})
	if err != nil {
		t.Fatalf("Assemble 返回错误: %v", err)
	}
	if len(result) >= len(msgs) {
		t.Fatalf("超限后消息数应减少: 原始 %d 条，压缩后 %d 条", len(msgs), len(result))
	}
	// 压缩后的 token 数不应超过预算太多
	resultTokens := estimateTokens(result)
	if resultTokens > budget*2 {
		t.Errorf("压缩后 token (%d) 远超预算 (%d)", resultTokens, budget)
	}
}

func TestAssemble_ZeroBudget_ReturnsOriginal(t *testing.T) {
	// TokenBudget=0 表示不限制，原样返回
	eng := NewDefaultContextEngine()
	msgs := []llm.Message{
		textMsg("user", "hello"),
	}

	result, err := eng.Assemble(context.Background(), AssembleRequest{
		RunID:       "run-3",
		BrainKind:   agent.KindCentral,
		Messages:    msgs,
		TokenBudget: 0,
	})
	if err != nil {
		t.Fatalf("Assemble 返回错误: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("期望 1 条消息，得到 %d 条", len(result))
	}
}

func TestAssemble_EmptyMessages(t *testing.T) {
	// 空消息应返回 nil
	eng := NewDefaultContextEngine()

	result, err := eng.Assemble(context.Background(), AssembleRequest{
		RunID:       "run-4",
		BrainKind:   agent.KindCentral,
		Messages:    nil,
		TokenBudget: 1000,
	})
	if err != nil {
		t.Fatalf("Assemble 返回错误: %v", err)
	}
	if result != nil {
		t.Fatalf("空输入应返回 nil，得到 %d 条消息", len(result))
	}
}

// ---------------------------------------------------------------------------
// Compress 测试
// ---------------------------------------------------------------------------

func TestCompress_WindowTrim_PreservesSystemMessages(t *testing.T) {
	// 压缩时 system 消息不应被裁掉
	eng := NewDefaultContextEngine()

	msgs := []llm.Message{
		systemMsg("你是一个助手"),
		textMsg("user", strings.Repeat("old message ", 50)),
		textMsg("assistant", strings.Repeat("old reply ", 50)),
		textMsg("user", strings.Repeat("another old ", 50)),
		textMsg("assistant", strings.Repeat("another reply ", 50)),
		textMsg("user", "最新的问题"),
		textMsg("assistant", "最新的回答"),
	}

	// 用一个很小的预算触发压缩
	result, err := eng.Compress(context.Background(), msgs, 50)
	if err != nil {
		t.Fatalf("Compress 返回错误: %v", err)
	}

	// 检查 system 消息是否保留
	hasSystem := false
	for _, m := range result {
		if m.Role == "system" {
			hasSystem = true
			break
		}
	}
	if !hasSystem {
		t.Error("压缩后丢失了 system 消息")
	}

	// 应该有比原始更少的消息
	if len(result) >= len(msgs) {
		t.Errorf("压缩后消息数应减少: 原始 %d，压缩后 %d", len(msgs), len(result))
	}
}

func TestCompress_WindowTrim_KeepsRecentMessages(t *testing.T) {
	// 窗口裁剪应保留最新的消息
	eng := NewDefaultContextEngine()

	msgs := []llm.Message{
		textMsg("user", strings.Repeat("a", 400)),   // 老消息
		textMsg("assistant", strings.Repeat("b", 400)), // 老消息
		textMsg("user", "recent question"),            // 新消息
		textMsg("assistant", "recent answer"),         // 新消息
	}

	// 预算足够放下最后两条但不够放全部
	result, err := eng.Compress(context.Background(), msgs, 30)
	if err != nil {
		t.Fatalf("Compress 返回错误: %v", err)
	}

	if len(result) == 0 {
		t.Fatal("压缩结果不应为空")
	}

	// 最后一条应该是 "recent answer"
	lastMsg := result[len(result)-1]
	if lastMsg.Role != "assistant" || !strings.Contains(messageText(lastMsg), "recent") {
		t.Error("压缩后最后一条消息不是最新的回答")
	}
}

func TestCompress_EmptyMessages(t *testing.T) {
	eng := NewDefaultContextEngine()

	result, err := eng.Compress(context.Background(), nil, 1000)
	if err != nil {
		t.Fatalf("Compress 返回错误: %v", err)
	}
	if result != nil {
		t.Fatalf("空输入应返回 nil，得到 %d 条", len(result))
	}
}

func TestCompress_ZeroBudget_ReturnsOriginal(t *testing.T) {
	eng := NewDefaultContextEngine()
	msgs := []llm.Message{textMsg("user", "hello")}

	result, err := eng.Compress(context.Background(), msgs, 0)
	if err != nil {
		t.Fatalf("Compress 返回错误: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("预算为 0 应返回原消息，得到 %d 条", len(result))
	}
}

// ---------------------------------------------------------------------------
// Share 测试
// ---------------------------------------------------------------------------

func TestShare_LimitsTo10Messages(t *testing.T) {
	// Share 最多传递 10 条消息
	eng := NewDefaultContextEngine()
	eng.MaxShareMessages = 10

	var msgs []llm.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, textMsg("user", "msg"))
	}

	err := eng.Share(context.Background(), agent.KindCentral, agent.KindQuant, msgs)
	if err != nil {
		t.Fatalf("Share 返回错误: %v", err)
	}

	if len(eng.SharedMessages) > 10 {
		t.Fatalf("Share 应最多传递 10 条，实际传递了 %d 条", len(eng.SharedMessages))
	}
	if len(eng.SharedMessages) != 10 {
		t.Fatalf("期望传递 10 条，实际 %d 条", len(eng.SharedMessages))
	}
}

func TestShare_FiltersCredentials(t *testing.T) {
	// Share 应过滤包含凭证信息的消息
	eng := NewDefaultContextEngine()

	msgs := []llm.Message{
		textMsg("user", "正常消息"),
		textMsg("assistant", "api_key=sk-12345"),
		textMsg("user", "password: hunter2"),
		textMsg("assistant", "这是安全的回复"),
		textMsg("user", "secret token = abc"),
	}

	err := eng.Share(context.Background(), agent.KindCentral, agent.KindCode, msgs)
	if err != nil {
		t.Fatalf("Share 返回错误: %v", err)
	}

	// 应该过滤掉包含 api_key、password、secret token 的消息
	for _, m := range eng.SharedMessages {
		text := messageText(m)
		if strings.Contains(text, "api_key") || strings.Contains(text, "password") || strings.Contains(text, "secret") {
			t.Errorf("Share 未过滤敏感消息: %s", text)
		}
	}

	// 应该保留 2 条安全消息
	if len(eng.SharedMessages) != 2 {
		t.Fatalf("期望 2 条安全消息，得到 %d 条", len(eng.SharedMessages))
	}
}

func TestShare_EmptyMessages(t *testing.T) {
	eng := NewDefaultContextEngine()

	err := eng.Share(context.Background(), agent.KindCentral, agent.KindQuant, nil)
	if err != nil {
		t.Fatalf("Share 返回错误: %v", err)
	}
	if eng.SharedMessages != nil {
		t.Error("空输入 Share 后 SharedMessages 应为 nil")
	}
}

func TestShare_KeepsRecentMessages(t *testing.T) {
	// 超过 10 条时应保留最新的
	eng := NewDefaultContextEngine()
	eng.MaxShareMessages = 10

	var msgs []llm.Message
	for i := 0; i < 15; i++ {
		msgs = append(msgs, textMsg("user", strings.Repeat("x", i+1)))
	}

	err := eng.Share(context.Background(), agent.KindCentral, agent.KindData, msgs)
	if err != nil {
		t.Fatalf("Share 返回错误: %v", err)
	}

	// 应该取最后 10 条（即 i=5..14，文本长度 6..15）
	if len(eng.SharedMessages) != 10 {
		t.Fatalf("期望 10 条，得到 %d 条", len(eng.SharedMessages))
	}
	// 第一条应该是原列表中第 6 条（index 5），文本长度 6
	firstText := messageText(eng.SharedMessages[0])
	if len(firstText) != 6 {
		t.Errorf("保留的第一条消息文本长度应为 6，实际为 %d", len(firstText))
	}
}

// ---------------------------------------------------------------------------
// estimateTokens 测试
// ---------------------------------------------------------------------------

func TestEstimateTokens_Basic(t *testing.T) {
	msgs := []llm.Message{
		textMsg("user", strings.Repeat("a", 400)), // 400/4 = 100 token
	}
	tokens := estimateTokens(msgs)
	// user = 1 token (4/4), text = 100 token => ~101
	if tokens < 90 || tokens > 120 {
		t.Errorf("期望约 100 token，得到 %d", tokens)
	}
}

func TestEstimateTokens_EmptyMessages(t *testing.T) {
	tokens := estimateTokens(nil)
	if tokens != 0 {
		t.Errorf("空消息应返回 0 token，得到 %d", tokens)
	}
}
