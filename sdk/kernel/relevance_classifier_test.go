// relevance_classifier_test.go — RelevanceClassifier 30 条样本覆盖
//
// 项目铁律:本仓库禁 go test / go vet,只用 go build。本测试文件供后续 CI 用,
// 当下不会被执行。但写在这里有两个作用:
//   1. 锚定关键词集合 + 决策顺序的语义边界(防回归)
//   2. 未来 CI 启用时立即可跑

package kernel

import (
	"context"
	"strings"
	"testing"
)

func TestRelevanceClassifier_KeywordLayer(t *testing.T) {
	c := NewDefaultRelevanceClassifier()
	// 不注入 Provider,确保只走关键词层

	cases := []struct {
		name     string
		input    string
		expected Relevance
		hint     string
	}{
		// === Cancel 6 条 ===
		{"中文取消", "取消", RelevanceCancel, "Cancel 关键词直通"},
		{"中文算了", "算了", RelevanceCancel, ""},
		{"中文不要做了", "不要做了", RelevanceCancel, ""},
		{"英文 cancel", "cancel", RelevanceCancel, ""},
		{"英文 abort", "abort it", RelevanceCancel, ""},
		{"英文 nevermind", "nevermind, stop", RelevanceCancel, ""},

		// === StatusQuery 6 条 ===
		{"做完了吗", "做完了吗", RelevanceStatusQuery, "进度查询"},
		{"看下进度", "看下进度怎样", RelevanceStatusQuery, ""},
		{"现在做到哪", "现在做到哪了", RelevanceStatusQuery, ""},
		{"are you done", "are you done with the task", RelevanceStatusQuery, ""},
		{"how's it going", "how's it going so far", RelevanceStatusQuery, ""},
		{"还剩什么没做", "看下做完了没", RelevanceStatusQuery, "用户场景核心 case"},

		// === 疑问句保护(Unrelated)6 条 ===
		{"什么是 plan 命令", "什么是用 plan 命令", RelevanceUnrelated, "strongTrigger 抢跑修复"},
		{"为什么用 Go", "为什么用 Go 不用 Java?", RelevanceUnrelated, ""},
		{"解释微服务", "解释一下微服务架构", RelevanceUnrelated, ""},
		{"中文问号", "前后端怎么通信?", RelevanceUnrelated, "中文问号疑问"},
		{"英文 what is", "what is dependency injection", RelevanceUnrelated, ""},
		{"英文 how does", "how does prompt cache work", RelevanceUnrelated, ""},

		// === Modification 6 条 ===
		{"改成 SQLite", "改成 SQLite 不要 Postgres", RelevanceModification, "用户场景核心"},
		{"换成 Vue", "换成 Vue 不要 React", RelevanceModification, ""},
		{"也加搜索", "也加上搜索功能", RelevanceModification, ""},
		{"删除登录", "删除登录功能", RelevanceModification, ""},
		{"等下先", "等下先做登录", RelevanceModification, ""},
		{"英文 change to", "change to TypeScript", RelevanceModification, ""},

		// === Refine 4 条 ===
		{"再快一点", "再快一点", RelevanceRefine, ""},
		{"加注释", "加上注释", RelevanceRefine, ""},
		{"用中文", "用中文写", RelevanceRefine, ""},
		{"代码风格", "代码风格用 PEP 8", RelevanceRefine, ""},

		// === Unrelated 默认 2 条(无关键词命中) ===
		{"今天天气", "今天天气真好", RelevanceUnrelated, "无关闲聊"},
		{"哈哈", "哈哈", RelevanceUnrelated, "无意义短句"},
	}

	correct := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := c.Classify(context.Background(), tc.input, RelevanceContext{})
			if v.Kind != tc.expected {
				t.Errorf("input=%q\n  expected: %s\n  got:      %s\n  source:   %s\n  rationale:%s\n  hint:     %s",
					tc.input, tc.expected, v.Kind, v.Source, v.Rationale, tc.hint)
				return
			}
			correct++
		})
	}

	// 总体准确率(关键词层不应低于 100%,因为 LLM 兜底关闭)
	total := len(cases)
	t.Logf("RelevanceClassifier (keyword-only): %d/%d = %.1f%%", correct, total, float64(correct)/float64(total)*100)
}

func TestRelevanceClassifier_QuestionMarkPriority(t *testing.T) {
	// 关键 case:含 modification 关键词 + 疑问形式 → 应判 Unrelated
	c := NewDefaultRelevanceClassifier()
	cases := []string{
		"为什么要改成 SQLite?",
		"什么是改成 X 的意思",
		"换成 Vue 是什么意思?",
	}
	for _, in := range cases {
		v := c.Classify(context.Background(), in, RelevanceContext{})
		if v.Kind != RelevanceUnrelated {
			t.Errorf("question-form should be Unrelated, got %s for %q (rationale: %s)",
				v.Kind, in, v.Rationale)
		}
	}
}

func TestRelevanceClassifier_EmptyInput(t *testing.T) {
	c := NewDefaultRelevanceClassifier()
	for _, in := range []string{"", " ", "  ", "\n", "a"} {
		v := c.Classify(context.Background(), in, RelevanceContext{})
		if v.Kind != RelevanceUnrelated {
			t.Errorf("empty/short input should be Unrelated, got %s for %q", v.Kind, in)
		}
	}
}

func TestRelevanceClassifier_NilSafe(t *testing.T) {
	var c *RelevanceClassifier
	v := c.Classify(context.Background(), "test", RelevanceContext{})
	if v.Kind != RelevanceUnrelated {
		t.Errorf("nil classifier should return Unrelated, got %s", v.Kind)
	}
}

func TestExtractFirstJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"a":1}`, `{"a":1}`},
		{"with prefix", `Result: {"a":1}`, `{"a":1}`},
		{"markdown fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"nested", `{"a":{"b":2}}`, `{"a":{"b":2}}`},
		{"with string brace", `{"text":"has } brace"}`, `{"text":"has } brace"}`},
		{"none", "no json here", ""},
		{"unbalanced", `{"a":1`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFirstJSON(tc.in)
			if got != tc.want {
				t.Errorf("extractFirstJSON(%q):\n  want: %q\n  got:  %q", tc.in, tc.want, got)
			}
		})
	}
}

func TestNormalizeRelevance(t *testing.T) {
	cases := map[string]Relevance{
		"unrelated":      RelevanceUnrelated,
		"UNRELATED":      RelevanceUnrelated,
		"  irrelevant ":  RelevanceUnrelated,
		"status_query":   RelevanceStatusQuery,
		"progress":       RelevanceStatusQuery,
		"modification":   RelevanceModification,
		"change":         RelevanceModification,
		"cancel":         RelevanceCancel,
		"abort":          RelevanceCancel,
		"refine":         RelevanceRefine,
		"unknown_value":  RelevanceUnrelated, // fallback
		"":               RelevanceUnrelated,
	}
	for in, want := range cases {
		if got := normalizeRelevance(in); got != want {
			t.Errorf("normalizeRelevance(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestKeywordsLowercase ensures all keyword constants are lowercase
// (hasAnyKeyword assumes them already lowered).
func TestKeywordsLowercase(t *testing.T) {
	check := func(name string, kws []string) {
		for _, k := range kws {
			if k != strings.ToLower(k) {
				t.Errorf("%s contains non-lowercase keyword: %q", name, k)
			}
		}
	}
	check("cancelKeywords", cancelKeywords)
	check("statusQueryKeywords", statusQueryKeywords)
	check("questionMarkers", questionMarkers)
	check("modificationKeywords", modificationKeywords)
	check("refineKeywords", refineKeywords)
}
