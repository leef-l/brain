// relevance_classifier.go — 用户输入与当前任务的关联性判定
//
// 设计动机:
//   chat 跑长任务期间用户随时可输入。如果不分类一律打断,会破坏正在进行的工作;
//   如果一律不打断,用户的"改成 X"等修正诉求被忽略,产生死循环。
//   分类器决定输入是否触发 Replan / 入队列 / 即时回复进度等。
//
// 决策顺序(短路,从上到下):
//   1. 空字符串 / 极短(< 2 字符) → Unrelated
//   2. Cancel 关键词("取消"/"算了"/"abort"等) → Cancel
//   3. StatusQuery 关键词("做完了吗"/"进度"/"状态"/"status"等) → StatusQuery
//   4. 疑问句保护(含"?"/"为什么"/"什么是"/"how"等) → Unrelated
//      关键:疑问句永远不触发 Modification,即使含项目关键词。
//   5. Modification 关键词("改成"/"不要"/"也加"/"换成"等) → Modification
//   6. Refine 关键词("再快一点"/"代码风格"等模糊补充) → Refine
//   7. LLM 兜底(仅当 Provider 非 nil 且前面都未命中)
//      调用 chat 默认 Provider 输出 JSON {relevance, confidence, rationale}
//      confidence < 0.7 降级为 Unrelated(保守)
//   8. 默认 Unrelated
//
// 设计文档:Replan-基于现有持久化与EventBus的渐进路线.md §2.1

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/leef-l/brain/sdk/llm"
)

// Relevance 表示输入与当前任务的关联类型。
type Relevance string

const (
	// RelevanceUnrelated 无关 — 闲聊 / 提问 / 不相关请求,不打断当前任务。
	// chat REPL 应入队列,等当前 turn 完成后正常处理。
	RelevanceUnrelated Relevance = "unrelated"

	// RelevanceStatusQuery 状态查询 — "做完了吗"/"进度怎样",不打断,即时打印进度摘要。
	// 不进对话历史(避免污染 LLM context)。
	RelevanceStatusQuery Relevance = "status_query"

	// RelevanceModification 修改请求 — "改成 X"/"不要 Y"/"也加 Z",触发 Replan。
	// 进入对话历史并写 ProjectMemory(MemoryDecision)。
	RelevanceModification Relevance = "modification"

	// RelevanceCancel 取消 — "取消"/"算了"/"abort",STOP 但不 Replan。
	RelevanceCancel Relevance = "cancel"

	// RelevanceRefine 补充指令 — "再快一点"/"代码风格用 X",不停任务。
	// 走 brain.feedback.requested 事件,sub 内部决定是否吸收。
	RelevanceRefine Relevance = "refine"

	// RelevanceContinue 继续指令 — "确认"/"开工"/"继续"/"go ahead",不打断、不入队列。
	// 这是用户对当前 plan 的 ack(同意继续推进),应该静默放行,不触发任何动作。
	// 用户日志中"开工"被误判为 Unrelated 入队等待是真实场景的痛点。
	RelevanceContinue Relevance = "continue"
)

// RelevanceContext 是分类器需要的上下文(由调用方组装)。
type RelevanceContext struct {
	// PlanGoal 当前 plan 的 Goal 字符串(spec.Goal),空表示没有正在跑的 plan。
	PlanGoal string

	// RunningTaskNames 正在跑的子任务名字列表,用于 LLM prompt 帮助判断关联性。
	RunningTaskNames []string

	// CompletedTaskNames 已完成的子任务名字列表。
	CompletedTaskNames []string
}

// RelevanceVerdict 分类结果。
type RelevanceVerdict struct {
	Kind       Relevance `json:"kind"`
	Confidence float64   `json:"confidence"` // 0.0 ~ 1.0
	Rationale  string    `json:"rationale,omitempty"`
	// Source 标识哪一层做出了判定:"keyword" / "llm" / "default"
	Source string `json:"source"`
}

// RelevanceClassifier 分类 user input / sub feedback 的关联性。
//
// Provider 可选,nil 时跳过 LLM 兜底,只用关键词层。
// 推荐用法:chat REPL 把当前 chat session 的 llm.Provider 注入进来。
// 因 brain-v3 已支持「不同 brain 不同模型」(LLMProxy.ModelForKind),不需要在
// 此层再开独立配置 — 谁调用谁传 provider。
type RelevanceClassifier struct {
	Provider llm.Provider

	// Model 调用 LLM 兜底时使用的模型名。空 → 让 provider 用默认。
	// 一般传 chat session 当前用的 model,保证体验一致。
	Model string

	// MinConfidence LLM 兜底输出的置信度阈值,低于此值降级为 Unrelated。默认 0.7。
	MinConfidence float64
}

// NewDefaultRelevanceClassifier 返回默认配置的分类器(provider/model 由调用方注入)。
func NewDefaultRelevanceClassifier() *RelevanceClassifier {
	return &RelevanceClassifier{
		MinConfidence: 0.7,
	}
}

// 关键词集合(中英文双语)。设计原则:**少而精**,避免误判。
// 命中即返回对应 Relevance,不再走后续层。

var cancelKeywords = []string{
	// 中文
	"取消", "算了", "停止", "撤销", "撤回", "中止", "中断",
	"不做了", "不要做", "不要了", "废弃", "丢弃",
	// 英文
	"cancel", "abort", "stop", "halt", "nevermind", "never mind",
	"forget it", "scratch that", "discard",
}

var statusQueryKeywords = []string{
	// 中文 — 询问状态
	"做完了吗", "做完没", "完成了吗", "完成没", "好了吗", "好了没",
	"进度怎么样", "进度如何", "进度怎样", "什么进度", "现在做到哪",
	"目前状态", "现在状态", "当前状态",
	"看下进度", "看看进度", "看一下进度",
	"看下做完", "看下做没", "看看做完", "看看做没",
	"现在到哪", "做到哪", "进展",
	// 英文
	"are you done", "is it done", "is it finished", "are we done",
	"how's it going", "how is it going", "what's the status",
	"what is the status", "show progress", "current progress",
	"any progress", "still working",
}

// 疑问句标记 — 命中任一直接 Unrelated,**不允许** Modification 抢跑。
// 关键 case:"什么是用 plan 命令" → 必须 Unrelated,不能 Modification。
var questionMarkers = []string{
	// 中文疑问
	"什么是", "啥是", "什么叫", "怎么理解",
	"为什么", "为啥", "怎么会", "为何",
	"是什么", "是啥",
	"区别", "差别", "不同点", "差异",
	"解释一下", "解释下", "解释一", "讲解",
	"介绍一下", "介绍下", "说说", "聊聊", "讲讲",
	// 英文疑问
	"how does", "how do", "what is", "what are", "what's",
	"why is", "why does", "why do",
	"explain", "describe", "compare",
}

var modificationKeywords = []string{
	// 中文 — 直接修正动词
	"改成", "改为", "改用", "改到", "换成", "换为", "换用", "换到",
	"不要用", "不用", "改不要", "去掉", "删除", "移除",
	"也加", "再加", "加上", "加个", "加一个", "加点",
	"修改", "调整", "重新做", "重新写", "重做",
	"等下先", "先做", "先搞", "插队",
	// 英文
	"change to", "change it to", "switch to", "replace with",
	"don't use", "do not use", "remove", "drop",
	"also add", "add a", "add an", "add the",
	"modify", "adjust", "redo", "rewrite",
}

var refineKeywords = []string{
	// 中文 — 补充约束
	"再快一点", "快一点", "速度", "性能",
	"代码风格", "命名", "注释",
	"加上注释", "加注释", "写注释",
	"用中文", "用英文",
	// 英文
	"faster", "speed up", "code style", "naming convention",
	"add comments", "use chinese", "use english",
}

// 继续指令关键词 — 用户对当前 plan 的 ack。
// 命中即返回 Continue,不打断不入队列。这些词的语义是"按现在这样继续推进",
// 与 Modification("改成 X")的"换方向"明确不同。
//
// 重要:必须放在 Modification 前判定。"确认,开工" 这种短语在现行实现里走不到
// Modification(无关键词命中)就被默认归 Unrelated 入队,体验灾难。
var continueKeywords = []string{
	// 中文 — 同意/继续/确认
	"确认", "确定", "同意", "可以", "ok", "OK",
	"开工", "开始", "开干", "开搞", "搞起", "干起来",
	"继续", "接着干", "接着做", "继续干",
	"go ahead", "proceed", "approved", "confirmed",
	"yes do it", "let's go", "go for it",
}

// Classify 判断 input 的关联性。
//
// ctx 用于 LLM 兜底调用;如果只用关键词层,传 context.Background() 即可。
// rctx 提供 plan 上下文(可零值,LLM 兜底时仅有用)。
func (c *RelevanceClassifier) Classify(ctx context.Context, input string, rctx RelevanceContext) RelevanceVerdict {
	if c == nil {
		return RelevanceVerdict{Kind: RelevanceUnrelated, Source: "default"}
	}
	text := strings.TrimSpace(input)
	if len(text) < 2 {
		return RelevanceVerdict{Kind: RelevanceUnrelated, Confidence: 1.0, Source: "default"}
	}
	low := strings.ToLower(text)

	// 1. Cancel 关键词
	if hasAnyKeyword(low, cancelKeywords) {
		return RelevanceVerdict{
			Kind:       RelevanceCancel,
			Confidence: 0.95,
			Source:     "keyword",
			Rationale:  "matched cancel keyword",
		}
	}

	// 2. StatusQuery 关键词
	if hasAnyKeyword(low, statusQueryKeywords) {
		return RelevanceVerdict{
			Kind:       RelevanceStatusQuery,
			Confidence: 0.9,
			Source:     "keyword",
			Rationale:  "matched status query keyword",
		}
	}

	// 3. 疑问句保护(放在 Modification 前,确保"什么是 X" 不会被 Modification 抢)
	if isQuestionLike(text) {
		return RelevanceVerdict{
			Kind:       RelevanceUnrelated,
			Confidence: 0.85,
			Source:     "keyword",
			Rationale:  "question form (info request, not modification)",
		}
	}

	// 4. Continue 关键词("确认/开工/继续/go ahead/ok")— 用户 ack,不打断不入队列。
	// 必须放在 Modification 前:这些词大多很短,LLM 兜底也未必能稳判。
	// 短句优先短路命中。
	if hasAnyKeyword(low, continueKeywords) {
		return RelevanceVerdict{
			Kind:       RelevanceContinue,
			Confidence: 0.9,
			Source:     "keyword",
			Rationale:  "matched continue keyword (user ack)",
		}
	}

	// 5. Modification 关键词
	if hasAnyKeyword(low, modificationKeywords) {
		return RelevanceVerdict{
			Kind:       RelevanceModification,
			Confidence: 0.85,
			Source:     "keyword",
			Rationale:  "matched modification keyword",
		}
	}

	// 5. Refine 关键词
	if hasAnyKeyword(low, refineKeywords) {
		return RelevanceVerdict{
			Kind:       RelevanceRefine,
			Confidence: 0.75,
			Source:     "keyword",
			Rationale:  "matched refine keyword",
		}
	}

	// 6. LLM 兜底(可选,Provider==nil 时跳过)
	if c.Provider != nil {
		v, err := c.classifyWithLLM(ctx, text, rctx)
		if err == nil {
			// 置信度低降级 Unrelated(保守:宁可漏判不误停)
			if v.Confidence < c.MinConfidence {
				v.Kind = RelevanceUnrelated
				v.Source = "llm_low_confidence"
			}
			return v
		}
		// LLM 失败,fall through 到默认
	}

	// 7. 默认 Unrelated
	return RelevanceVerdict{
		Kind:       RelevanceUnrelated,
		Confidence: 0.5,
		Source:     "default",
		Rationale:  "no keyword matched, no LLM available",
	}
}

// classifyWithLLM 调 chat 默认 Provider 做 JSON 分类。失败返回 error。
func (c *RelevanceClassifier) classifyWithLLM(ctx context.Context, input string, rctx RelevanceContext) (RelevanceVerdict, error) {
	system := buildClassifierSystemPrompt(rctx)
	prompt := fmt.Sprintf("用户刚说: \"%s\"\n\n判断关系并输出 JSON。", input)

	req := &llm.ChatRequest{
		System: []llm.SystemBlock{{Text: system, Cache: true}}, // cache=true,system 复用
		Messages: []llm.Message{{
			Role:    "user",
			Content: []llm.ContentBlock{{Type: "text", Text: prompt}},
		}},
		Model:     c.Model,
		MaxTokens: 200, // JSON 输出不需要长
	}

	resp, err := c.Provider.Complete(ctx, req)
	if err != nil {
		return RelevanceVerdict{}, fmt.Errorf("relevance llm: %w", err)
	}

	// 提取首块 text
	var raw string
	for _, b := range resp.Content {
		if b.Type == "text" {
			raw = b.Text
			break
		}
	}
	if raw == "" {
		return RelevanceVerdict{}, fmt.Errorf("relevance llm: empty response")
	}

	// 容错 JSON 提取(LLM 可能加 markdown 围栏 / 前置说明)
	jsonStr := extractFirstJSON(raw)
	if jsonStr == "" {
		return RelevanceVerdict{}, fmt.Errorf("relevance llm: no JSON in response: %q", raw)
	}

	var parsed struct {
		Relevance  string  `json:"relevance"`
		Confidence float64 `json:"confidence"`
		Rationale  string  `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return RelevanceVerdict{}, fmt.Errorf("relevance llm: parse JSON: %w (raw: %q)", err, jsonStr)
	}

	kind := normalizeRelevance(parsed.Relevance)
	return RelevanceVerdict{
		Kind:       kind,
		Confidence: parsed.Confidence,
		Source:     "llm",
		Rationale:  parsed.Rationale,
	}, nil
}

// buildClassifierSystemPrompt 生成 LLM system prompt。
// 用 cache=true 让 prompt 在多次分类间复用 cache。
func buildClassifierSystemPrompt(rctx RelevanceContext) string {
	var b strings.Builder
	b.WriteString("你是任务关联性分类器。判断用户输入与当前正在执行的项目任务的关系。\n\n")
	if rctx.PlanGoal != "" {
		b.WriteString("【当前项目目标】\n")
		b.WriteString(rctx.PlanGoal)
		b.WriteString("\n\n")
	}
	if len(rctx.RunningTaskNames) > 0 {
		b.WriteString("【正在执行】\n")
		for _, n := range rctx.RunningTaskNames {
			b.WriteString("- ")
			b.WriteString(n)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(rctx.CompletedTaskNames) > 0 {
		b.WriteString("【已完成】\n")
		for _, n := range rctx.CompletedTaskNames {
			b.WriteString("- ")
			b.WriteString(n)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(`只输出 JSON,不解释,不加 markdown 围栏:
{
  "relevance": "unrelated"|"status_query"|"modification"|"cancel"|"refine",
  "confidence": 0.0~1.0,
  "rationale": "<一句话原因>"
}

分类标准:
- unrelated: 闲聊、问无关问题、与项目无关
- status_query: 问当前进度、做完了吗、看下状态(不需要改动)
- modification: 要求改方案、换技术栈、加新需求、删除已有需求
- cancel: 明确说取消、停止、放弃整个任务
- refine: 补充小约束(代码风格、注释、性能要求)而不改方案

注意:
- 疑问句(什么是/为什么/?)永远是 unrelated,即使含技术词
- 用户提到当前没有的功能但语气是"问"而非"加",归 unrelated
- 把握不准时给低 confidence(< 0.7),系统会保守降级为 unrelated`)
	return b.String()
}

// normalizeRelevance 把 LLM 输出的字符串映射到合法 Relevance。
// 兼容大小写 / 拼写小错。
func normalizeRelevance(s string) Relevance {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "unrelated", "irrelevant", "none":
		return RelevanceUnrelated
	case "status_query", "status", "query", "progress":
		return RelevanceStatusQuery
	case "modification", "modify", "change":
		return RelevanceModification
	case "cancel", "abort", "stop":
		return RelevanceCancel
	case "refine", "refinement", "tweak":
		return RelevanceRefine
	}
	return RelevanceUnrelated
}

// hasAnyKeyword 任一命中即返回 true。
// 注意:keywords 必须已 ToLower 化(本文件常量都是)。
func hasAnyKeyword(low string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// isQuestionLike 检测是否为疑问句。
//   - 含中文?或英文 ?
//   - 含 questionMarkers 任一关键词
func isQuestionLike(text string) bool {
	if strings.Contains(text, "?") || strings.Contains(text, "?") {
		return true
	}
	low := strings.ToLower(text)
	for _, m := range questionMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// extractFirstJSON 从 LLM 输出文本中提取第一个 JSON 对象。
// 处理常见 case:
//   - markdown 围栏: ```json\n{...}\n```
//   - 前置说明文字: "Result: {...}"
//   - 纯 JSON
//
// 提取失败返回空字符串。
func extractFirstJSON(s string) string {
	// 找到第一个 {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	// 平衡括号找匹配的 }
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if escape {
			escape = false
			continue
		}
		if ch == '\\' {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return "" // 不平衡
}
