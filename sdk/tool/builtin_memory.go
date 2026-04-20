package tool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/leef-l/brain/sdk/persistence"
)

// brain.memory_recall 让 central / 专家 LLM 在运行时查询"持久化记忆",
// 回答用户"读取上下文记忆 / 你学过什么 / 以前是怎么做的"这类问题。
//
// 存储介质是 ~/.brain/brain.db(SQLite),包含:
//   - ui_patterns       学到/seed 的可重放 UI 自动化模式
//   - human_demo_sequences 人类演示序列(审批后转 pattern)
//   - learning_profiles  每个 brain 的能力画像
//
// 工具按 kind 路由:
//   kind="patterns"  → 列最近 ui_patterns(id/category/description/enabled)
//   kind="demos"     → 列最近 human_demo_sequences(id/run_id/url/approved)
//   kind="profiles"  → 列 brain capability profiles
//   kind="summary"   → 三者的计数摘要
//
// 附带 query 做 LIKE 过滤,limit 默认 20。

// MemoryStore 是工具依赖的 persistence 子集,方便测试注入。
type MemoryStore interface {
	ListHumanDemoSequences(ctx context.Context, approvedOnly bool) ([]*persistence.HumanDemoSequence, error)
	ListProfiles(ctx context.Context) ([]*persistence.LearningProfile, error)
}

type memoryRecallTool struct {
	store MemoryStore
	lib   *PatternLibrary
}

// NewMemoryRecallTool 构造工具。store/lib 任一为 nil 对应能力会返回空。
// 供 cmd/brain 层在启动时注册到 central registry(和 quant/data 工具
// 同一风格)。
func NewMemoryRecallTool(store MemoryStore, lib *PatternLibrary) Tool {
	return &memoryRecallTool{store: store, lib: lib}
}

func (t *memoryRecallTool) Name() string { return "brain.memory_recall" }
func (t *memoryRecallTool) Risk() Risk   { return RiskSafe }
func (t *memoryRecallTool) Schema() Schema {
	return Schema{
		Name: t.Name(),
		Description: `Recall brain's persistent memory (SQLite ~/.brain/brain.db).

USE THIS when the user asks "你学过什么" / "读取上下文记忆" / "以前是怎么登录这个站的"
/ "有没有这个网站的 pattern" — the answer is in the brain.db tables, NOT in workdir
files. Do NOT use central.list_files / read_file to look for memory.

kind:
  - "summary"  counts of patterns/demos/profiles (quick overview)
  - "patterns" list ui_patterns (learned/seed automation flows)
  - "demos"    list human_demo_sequences (human demonstrations; approved ones
               become ui_patterns)
  - "profiles" list brain capability profiles (per-brain learning metrics)

query: optional substring; filter by id/description/url/site/brain_kind.
limit: max rows to return (default 20).`,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind":  { "type": "string", "enum": ["summary", "patterns", "demos", "profiles"] },
    "query": { "type": "string", "description": "optional substring filter" },
    "limit": { "type": "integer" }
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "kind":    { "type": "string" },
    "total":   { "type": "integer" },
    "items":   { "type": "array" },
    "summary": { "type": "object" }
  }
}`),
		Brain: "",
		Concurrency: &ToolConcurrencySpec{
			Capability:    "memory.read",
			AccessMode:    "shared-read",
			Scope:         "turn",
			ApprovalClass: "readonly",
		},
	}
}

func (t *memoryRecallTool) Execute(ctx context.Context, args json.RawMessage) (*Result, error) {
	var input struct {
		Kind  string `json:"kind"`
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &input); err != nil {
			return errResult("invalid arguments: %v", err), nil
		}
	}
	if input.Limit <= 0 {
		input.Limit = 20
	}
	if input.Kind == "" {
		input.Kind = "summary"
	}
	q := strings.ToLower(input.Query)

	switch input.Kind {
	case "summary":
		return t.summary(ctx)
	case "patterns":
		return t.listPatterns(q, input.Limit)
	case "demos":
		return t.listDemos(ctx, q, input.Limit)
	case "profiles":
		return t.listProfiles(ctx, q, input.Limit)
	default:
		return errResult("unknown kind: %s", input.Kind), nil
	}
}

func (t *memoryRecallTool) summary(ctx context.Context) (*Result, error) {
	out := map[string]interface{}{
		"kind": "summary",
	}
	summary := map[string]interface{}{}

	patternCount := 0
	var patternCats map[string]int
	if t.lib != nil {
		all := t.lib.ListAll("")
		patternCount = len(all)
		patternCats = map[string]int{}
		for _, p := range all {
			if p == nil {
				continue
			}
			patternCats[p.Category]++
		}
	}
	summary["patterns_total"] = patternCount
	summary["patterns_by_category"] = patternCats

	if t.store != nil {
		if demos, err := t.store.ListHumanDemoSequences(ctx, false); err == nil {
			approved := 0
			for _, d := range demos {
				if d != nil && d.Approved {
					approved++
				}
			}
			summary["demos_total"] = len(demos)
			summary["demos_approved"] = approved
		}
		if profiles, err := t.store.ListProfiles(ctx); err == nil {
			summary["profiles_total"] = len(profiles)
		}
	}

	out["summary"] = summary
	data, _ := json.Marshal(out)
	return &Result{Output: data}, nil
}

func (t *memoryRecallTool) listPatterns(q string, limit int) (*Result, error) {
	items := []map[string]interface{}{}
	if t.lib != nil {
		for _, p := range t.lib.ListAll("") {
			if p == nil {
				continue
			}
			if q != "" {
				blob := strings.ToLower(p.ID + " " + p.Category + " " + p.Description + " " + p.AppliesWhen.URLPattern)
				if !strings.Contains(blob, q) {
					continue
				}
			}
			items = append(items, map[string]interface{}{
				"id":          p.ID,
				"category":    p.Category,
				"description": p.Description,
				"source":      p.Source,
				"url_pattern": p.AppliesWhen.URLPattern,
				"steps":       len(p.ActionSequence),
				"enabled":     p.Enabled,
				"success":     p.Stats.SuccessCount,
				"failure":     p.Stats.FailureCount,
			})
			if len(items) >= limit {
				break
			}
		}
	}
	data, _ := json.Marshal(map[string]interface{}{
		"kind":  "patterns",
		"total": len(items),
		"items": items,
	})
	return &Result{Output: data}, nil
}

func (t *memoryRecallTool) listDemos(ctx context.Context, q string, limit int) (*Result, error) {
	items := []map[string]interface{}{}
	if t.store != nil {
		if demos, err := t.store.ListHumanDemoSequences(ctx, false); err == nil {
			for _, d := range demos {
				if d == nil {
					continue
				}
				if q != "" {
					blob := strings.ToLower(d.RunID + " " + d.BrainKind + " " + d.Goal + " " + d.Site + " " + d.URL)
					if !strings.Contains(blob, q) {
						continue
					}
				}
				items = append(items, map[string]interface{}{
					"id":          d.ID,
					"run_id":      d.RunID,
					"brain_kind":  d.BrainKind,
					"goal":        d.Goal,
					"site":        d.Site,
					"url":         d.URL,
					"approved":    d.Approved,
					"recorded_at": d.RecordedAt,
				})
				if len(items) >= limit {
					break
				}
			}
		}
	}
	data, _ := json.Marshal(map[string]interface{}{
		"kind":  "demos",
		"total": len(items),
		"items": items,
	})
	return &Result{Output: data}, nil
}

func (t *memoryRecallTool) listProfiles(ctx context.Context, q string, limit int) (*Result, error) {
	items := []map[string]interface{}{}
	if t.store != nil {
		if profiles, err := t.store.ListProfiles(ctx); err == nil {
			for _, p := range profiles {
				if p == nil {
					continue
				}
				if q != "" {
					blob := strings.ToLower(p.BrainKind)
					if !strings.Contains(blob, q) {
						continue
					}
				}
				items = append(items, map[string]interface{}{
					"brain_kind": p.BrainKind,
					"cold_start": p.ColdStart,
					"updated_at": p.UpdatedAt,
				})
				if len(items) >= limit {
					break
				}
			}
		}
	}
	data, _ := json.Marshal(map[string]interface{}{
		"kind":  "profiles",
		"total": len(items),
		"items": items,
	})
	return &Result{Output: data}, nil
}
