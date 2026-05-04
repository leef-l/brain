// Package llm — builtin_capabilities.go
//
// # Why this file exists
//
// `InferCapabilities` started as a hardcoded switch over a handful of
// model-name substrings. As we add国内外主流 model 的支持,这种 switch 越
// 来越长且越来越难审查,而且数据(每个 model 的真实能力)和代码(查找
// 逻辑)混在一起。
//
// Phase 7 把它分成两层:
//
//   1. builtinTable — 一个声明式数据表(本文件),每条记录刻画一个 model
//      family 的 capability + 匹配规则 + 元数据(标定日期 / 置信度 /
//      备注)。新增一个 model 只需在这里加一行,不需要写匹配逻辑。
//
//   2. LookupBuiltin — 按表顺序匹配,先命中先返回。返回 (caps, true)
//      表示命中内置数据;返回 (zero, false) 表示需要走启发式 fallback
//      (InferCapabilities)。
//
// # 优先级链
//
// Capability 的最终值由以下来源按优先级 merge 得到(高优先级覆盖同名字段):
//
//   1. 用户 config.json 里 active_provider.capabilities 的显式声明
//   2. 本文件的 builtinTable(主流 model 零配置即用)
//   3. InferCapabilities 启发式(关键词 / baseURL 兜底)
//   4. DefaultCapabilities()(最保守安全档)
//
// 用户主权永远高于内置数据 —— 即便我们标 deepseek tool_choice=none,
// 用户在 config 里写 "tool_choice":"required" 我们就听用户的。
//
// # 表的顺序
//
// 顺序敏感:具体规则放前,通用规则放后。reasoner 类用 ModelExcludes 避免
// 被通用关键词抢先(例如 "deepseek" 不能匹配 "deepseek-reasoner")。
//
// # Confidence 字段
//
// 所有未亲测 model 标 "documented" — 数据来自 vendor 公开文档 + 社区
// 经验,大概率正确但未在本项目中实测过。亲测过的 model 标 "verified"。
// 生产事故出现后回头修订并更新 VerifiedAt。

package llm

import "strings"

// BuiltinMatch declares how an entry matches a (baseURL, model) pair.
//
// Each slice is OR-semantic (any contains-match counts). Within an entry,
// non-nil slices are AND-semantic (BaseURL matches AND Model matches).
//
// A nil/empty slice means "no constraint on this dimension". An entry with
// both BaseURL and Model nil/empty would match everything — never declare
// such an entry.
type BuiltinMatch struct {
	// BaseURLContains: any of these substrings (case-insensitive) appearing
	// in baseURL counts as a hit on the URL dimension. Used for platform-
	// only entries (localhost / openrouter / volces 等).
	BaseURLContains []string

	// ModelContains: any of these substrings (case-insensitive) appearing
	// in model name counts as a hit on the model dimension. The most
	// common matcher.
	ModelContains []string

	// ModelExcludes: if any of these substrings appears in the model name
	// the entry is SKIPPED. Used to keep generic entries from stealing
	// reasoner-specific traffic (e.g. "deepseek" matcher excludes "reasoner"
	// so deepseek-reasoner falls through to the reasoner-specific entry).
	ModelExcludes []string
}

// matches tests whether this BuiltinMatch matches the given (baseURL, model).
// Both inputs are lowercased for substring comparison; callers don't need
// to normalize.
func (m BuiltinMatch) matches(baseURL, model string) bool {
	bl := strings.ToLower(baseURL)
	ml := strings.ToLower(model)

	// Excludes win — even if model would match positively, an exclude kills it.
	for _, ex := range m.ModelExcludes {
		if ex != "" && strings.Contains(ml, strings.ToLower(ex)) {
			return false
		}
	}

	urlOK := len(m.BaseURLContains) == 0
	for _, sub := range m.BaseURLContains {
		if sub != "" && strings.Contains(bl, strings.ToLower(sub)) {
			urlOK = true
			break
		}
	}

	modelOK := len(m.ModelContains) == 0
	for _, sub := range m.ModelContains {
		if sub != "" && strings.Contains(ml, strings.ToLower(sub)) {
			modelOK = true
			break
		}
	}

	// Reject the "match-everything" entry to surface mistakes early.
	if len(m.BaseURLContains) == 0 && len(m.ModelContains) == 0 {
		return false
	}

	return urlOK && modelOK
}

// BuiltinEntry is one declarative row of the capability table.
type BuiltinEntry struct {
	Match BuiltinMatch

	Family                  string
	ToolChoiceSupport       ToolChoiceMode
	Reasoner                bool
	EmitsReasoningContent   bool
	MaxParallelTools        int
	PrefersStructuredOutput bool

	// Confidence is one of:
	//   "verified"   — tested in this project against the live API
	//   "documented" — derived from vendor docs / community reports
	//   "inferred"   — best-guess, hasn't been confirmed
	Confidence string

	// VerifiedAt is the YYYY-MM-DD this row was last reviewed. Refresh
	// when fixing a bug or revisiting a vendor's behavior.
	VerifiedAt string

	// Notes is an optional free-form annotation (deployment quirks,
	// known incompatibilities). Shown in `brain capability list` (future).
	Notes string
}

// toCapabilities converts a BuiltinEntry into the runtime Capabilities
// shape consumed by Provider / Runner. NativeToolCall is hardcoded true
// because every entry in the builtin table is a native-tool-calling model
// (entries that aren't would not belong here).
func (e BuiltinEntry) toCapabilities() Capabilities {
	maxParallel := e.MaxParallelTools
	if maxParallel <= 0 {
		maxParallel = 1
	}
	return Capabilities{
		Family:                  e.Family,
		NativeToolCall:          true,
		ToolChoiceSupport:       e.ToolChoiceSupport,
		Reasoner:                e.Reasoner,
		MaxParallelTools:        maxParallel,
		EmitsReasoningContent:   e.EmitsReasoningContent,
		PrefersStructuredOutput: e.PrefersStructuredOutput,
	}
}

// builtinTable is the canonical list of model capability declarations for
// 国内外主流 LLM family. ORDER MATTERS — entries earlier in the slice are
// matched first. Specific (reasoner) entries MUST come before their generic
// counterparts; generic entries should use ModelExcludes to avoid stealing
// the specific entries' traffic.
//
// Adding a new model:
//
//   1. Decide which family it belongs to (or create a new one).
//   2. Insert in priority order — reasoner before generic, specific keyword
//      before broad keyword.
//   3. Set Confidence honestly: "verified" only if you've actually
//      run it through brain end-to-end.
//   4. Update VerifiedAt to today.
//
// All entries set MaxParallelTools=4 by default for non-reasoner / 1 for
// reasoner — these are conservative; bump for vendors known to handle
// higher concurrency reliably.
var builtinTable = []BuiltinEntry{
	// ────────────────────────────────────────────────────────────────────
	// Reasoner-class (must come BEFORE the generic entries of the same
	// vendor so generic "qwen" / "deepseek" don't steal reasoner traffic).
	// ────────────────────────────────────────────────────────────────────

	// OpenAI o-series — official reasoner family. tool_choice IS supported
	// (per the o1/o3/o4 docs) but the model frequently spends turn 1 in
	// silent reasoning; runner gives grace turn via Reasoner=true.
	// reasoning_content is opaque (OpenAI doesn't expose it in the API).
	{
		Match:                 BuiltinMatch{ModelContains: []string{"o1-", "o3-", "o4-"}},
		Family:                "openai-reasoner",
		ToolChoiceSupport:     ToolChoiceRequired,
		Reasoner:              true,
		EmitsReasoningContent: false,
		MaxParallelTools:      4,
		Confidence:            "documented",
		VerifiedAt:            "2026-05-04",
		Notes:                 "OpenAI o-series — supports tool_choice but frequently silent on turn 1",
	},

	// DeepSeek-Reasoner / R1 — Chinese flagship reasoner. tool_choice is
	// silently dropped (HTTP 200 but ignored). Round-trips reasoning_content
	// in the chat response — provider must preserve it.
	{
		Match:                 BuiltinMatch{ModelContains: []string{"deepseek-reasoner", "deepseek-r1"}},
		Family:                "deepseek-reasoner",
		ToolChoiceSupport:     ToolChoiceNone,
		Reasoner:              true,
		EmitsReasoningContent: true,
		MaxParallelTools:      1,
		Confidence:            "verified",
		VerifiedAt:            "2026-05-04",
		Notes:                 "Reasoning roundtrip via reasoning_content; tool_choice silently ignored",
	},

	// Qwen-Reasoner / QwQ / Qwen-R1 — Alibaba's reasoner variants. Same
	// quirks as deepseek-reasoner from the API surface perspective.
	{
		Match:                 BuiltinMatch{ModelContains: []string{"qwq", "qwen-r1", "qwen-reasoner"}},
		Family:                "qwen-reasoner",
		ToolChoiceSupport:     ToolChoiceNone,
		Reasoner:              true,
		EmitsReasoningContent: true,
		MaxParallelTools:      1,
		Confidence:            "documented",
		VerifiedAt:            "2026-05-04",
		Notes:                 "Tongyi reasoner family — same shape as deepseek-r1",
	},

	// Mimo (小米) — reasoner-class throughout the lineup, even non-r variants
	// produce thinking tokens. tool_choice not honored.
	{
		Match:                 BuiltinMatch{ModelContains: []string{"mimo"}},
		Family:                "mimo",
		ToolChoiceSupport:     ToolChoiceNone,
		Reasoner:              true,
		EmitsReasoningContent: true,
		MaxParallelTools:      1,
		Confidence:            "verified",
		VerifiedAt:            "2026-05-04",
		Notes:                 "Xiaomi mimo — entire lineup behaves reasoner-style",
	},

	// ────────────────────────────────────────────────────────────────────
	// 国外 mainstream(非 reasoner)
	// ────────────────────────────────────────────────────────────────────

	// Anthropic Claude — flagship native tool calling. tool_choice fully
	// honored ("any" maps to required, specific tool name supported).
	// Covers Claude 3 / 3.5 / 4 / opus / sonnet / haiku — all share the
	// same protocol envelope.
	{
		Match:                   BuiltinMatch{ModelContains: []string{"claude"}},
		Family:                  "anthropic-claude",
		ToolChoiceSupport:       ToolChoiceSpecific,
		Reasoner:                false,
		MaxParallelTools:        8,
		PrefersStructuredOutput: true,
		Confidence:              "verified",
		VerifiedAt:              "2026-05-04",
		Notes:                   "Anthropic direct + Bedrock + Vertex — uniform behavior",
	},

	// OpenAI GPT-3.5/4/5 — flagship native tool calling. tool_choice fully
	// honored including specific function name.
	{
		Match:                   BuiltinMatch{ModelContains: []string{"gpt-4", "gpt-5", "gpt-3.5"}},
		Family:                  "openai-gpt",
		ToolChoiceSupport:       ToolChoiceSpecific,
		Reasoner:                false,
		MaxParallelTools:        8,
		PrefersStructuredOutput: true,
		Confidence:              "verified",
		VerifiedAt:              "2026-05-04",
		Notes:                   "OpenAI direct + Azure",
	},

	// Google Gemini — native tool calling, tool_choice supported.
	{
		Match:             BuiltinMatch{ModelContains: []string{"gemini"}},
		Family:            "google-gemini",
		ToolChoiceSupport: ToolChoiceRequired,
		MaxParallelTools:  8,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
		Notes:             "AI Studio + Vertex — OpenAI-compatible mode",
	},

	// Mistral AI — supports tool_choice but only at "auto" / "any" granularity.
	{
		Match:             BuiltinMatch{ModelContains: []string{"mistral", "mixtral", "codestral"}},
		Family:            "mistral",
		ToolChoiceSupport: ToolChoiceAuto,
		MaxParallelTools:  4,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
	},

	// Cohere Command — strong tool support including required mode.
	{
		Match:             BuiltinMatch{ModelContains: []string{"command", "cohere"}},
		Family:            "cohere",
		ToolChoiceSupport: ToolChoiceRequired,
		MaxParallelTools:  4,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
	},

	// Meta Llama 3.1+ — tool calling supported on most hosting platforms,
	// quality varies. We claim Auto rather than Required because some
	// hosts (vLLM defaults) silently drop the field.
	{
		Match:             BuiltinMatch{ModelContains: []string{"llama"}},
		Family:            "meta-llama",
		ToolChoiceSupport: ToolChoiceAuto,
		MaxParallelTools:  4,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
		Notes:             "Behavior varies by host (Together / Fireworks / Groq / self-hosted)",
	},

	// ────────────────────────────────────────────────────────────────────
	// 国内 mainstream(非 reasoner)
	// ────────────────────────────────────────────────────────────────────

	// DeepSeek (chat / coder / V3 / V4 等) — OpenAI-compatible 但 tool_choice
	// silently dropped(HTTP 200 但不强制)。runner 必须靠 IntentChain 兜底。
	{
		Match: BuiltinMatch{
			ModelContains: []string{"deepseek"},
			ModelExcludes: []string{"reasoner", "r1"},
		},
		Family:            "deepseek",
		ToolChoiceSupport: ToolChoiceNone,
		MaxParallelTools:  4,
		Confidence:        "verified",
		VerifiedAt:        "2026-05-04",
		Notes:             "DeepSeek chat/coder/V3/V4 — tool_choice silently ignored",
	},

	// Qwen 普通版(非 reasoner) — tool_choice 部分支持,行为最近有变化,
	// 保守标 Auto。
	{
		Match: BuiltinMatch{
			ModelContains: []string{"qwen"},
			ModelExcludes: []string{"qwq", "r1", "reasoner"},
		},
		Family:            "qwen",
		ToolChoiceSupport: ToolChoiceAuto,
		MaxParallelTools:  4,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
		Notes:             "Tongyi 通义千问 base/coder/max — tool_choice support evolving",
	},

	// 智谱 GLM-4.x — tool_choice 部分支持。
	{
		Match:             BuiltinMatch{ModelContains: []string{"glm"}},
		Family:            "glm",
		ToolChoiceSupport: ToolChoiceAuto,
		MaxParallelTools:  4,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
		Notes:             "智谱 GLM-4 / GLM-4.5 / GLM-4.6 — official + bigmodel.cn",
	},

	// 字节豆包 doubao — Volcengine 部署,tool_choice 不生效。
	{
		Match: BuiltinMatch{
			ModelContains:   []string{"doubao"},
			BaseURLContains: nil, // model 名命中即可
		},
		Family:            "doubao",
		ToolChoiceSupport: ToolChoiceNone,
		MaxParallelTools:  4,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
		Notes:             "字节豆包 — volces 平台",
	},

	// 月之暗面 Kimi / Moonshot — tool_choice 官方文档支持。
	{
		Match:             BuiltinMatch{ModelContains: []string{"moonshot", "kimi"}},
		Family:            "moonshot",
		ToolChoiceSupport: ToolChoiceRequired,
		MaxParallelTools:  4,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
		Notes:             "月之暗面 Kimi — tool_choice 文档声明支持但少量代理实现可能 drop",
	},

	// 零一万物 Yi — tool 支持基础水平。
	{
		Match:             BuiltinMatch{ModelContains: []string{"yi-"}},
		Family:            "yi",
		ToolChoiceSupport: ToolChoiceAuto,
		MaxParallelTools:  4,
		Confidence:        "inferred",
		VerifiedAt:        "2026-05-04",
		Notes:             "零一万物 — 接口靠近 OpenAI 但 tool_choice 行为未亲测",
	},

	// 阶跃星辰 step.
	{
		Match:             BuiltinMatch{ModelContains: []string{"step-"}},
		Family:            "step",
		ToolChoiceSupport: ToolChoiceAuto,
		MaxParallelTools:  4,
		Confidence:        "inferred",
		VerifiedAt:        "2026-05-04",
		Notes:             "StepFun 阶跃星辰 — OpenAI 兼容路径",
	},

	// 腾讯混元 hunyuan.
	{
		Match:             BuiltinMatch{ModelContains: []string{"hunyuan"}},
		Family:            "hunyuan",
		ToolChoiceSupport: ToolChoiceAuto,
		MaxParallelTools:  4,
		Confidence:        "inferred",
		VerifiedAt:        "2026-05-04",
		Notes:             "腾讯混元 — Tencent Cloud / hunyuan API",
	},

	// 百度文心 ERNIE / wenxin — tool_choice 行为尚不稳定。
	{
		Match:             BuiltinMatch{ModelContains: []string{"ernie", "wenxin"}},
		Family:            "ernie",
		ToolChoiceSupport: ToolChoiceNone,
		MaxParallelTools:  4,
		Confidence:        "inferred",
		VerifiedAt:        "2026-05-04",
		Notes:             "百度文心 — 接口与 OpenAI 偏差较大",
	},

	// 讯飞星火 spark / xinghuo — 老牌国产 LLM,接口 quirk 多。
	{
		Match:             BuiltinMatch{ModelContains: []string{"spark", "xinghuo"}},
		Family:            "spark",
		ToolChoiceSupport: ToolChoiceNone,
		MaxParallelTools:  4,
		Confidence:        "inferred",
		VerifiedAt:        "2026-05-04",
		Notes:             "讯飞星火 — tool 支持参差,稳妥起见关闭 tool_choice",
	},

	// ────────────────────────────────────────────────────────────────────
	// 平台/代理 / 本地部署兜底(baseURL-only,model 任意)
	// ────────────────────────────────────────────────────────────────────

	// 本地部署(ollama / llama.cpp server / lmstudio 等) — tool_choice 在
	// 本地推理引擎里几乎都不生效。其余字段保持默认,由用户在 config
	// 里覆盖 model-specific 行为。
	//
	// 注意:此条放在最末,只有所有 model-name 匹配都 miss 时才走到这里。
	// 如果用户在 localhost 跑 Llama,上面的 "llama" 条目会先命中,这里
	// 是真正未知 model 的兜底。
	{
		Match: BuiltinMatch{
			BaseURLContains: []string{"localhost", "127.0.0.1", ":11434", ":8080", ":1234", "ollama"},
		},
		Family:            "local-deploy",
		ToolChoiceSupport: ToolChoiceNone,
		MaxParallelTools:  1,
		Confidence:        "documented",
		VerifiedAt:        "2026-05-04",
		Notes:             "Local inference (ollama / llama.cpp / lmstudio) — tool_choice rarely honored",
	},
}

// LookupBuiltin returns the first builtinTable entry that matches the given
// (baseURL, model). Returns ok=false when no entry matches — the caller
// should then fall back to InferCapabilities (heuristic) or
// DefaultCapabilities (zero-knowledge safe defaults).
//
// Match semantics: see BuiltinMatch. Order semantics: see builtinTable doc.
func LookupBuiltin(baseURL, model string) (Capabilities, bool) {
	for _, entry := range builtinTable {
		if entry.Match.matches(baseURL, model) {
			return entry.toCapabilities(), true
		}
	}
	return Capabilities{}, false
}

// BuiltinEntries returns a defensive copy of the builtin table for callers
// that want to enumerate (e.g. a future `brain capability list` command).
// Returning a copy keeps the table immutable from outside the package.
func BuiltinEntries() []BuiltinEntry {
	out := make([]BuiltinEntry, len(builtinTable))
	copy(out, builtinTable)
	return out
}
