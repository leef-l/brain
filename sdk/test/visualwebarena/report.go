package visualwebarena

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Summary 是单轮聚合指标。
type Summary struct {
	Mode              RunMode
	Total             int
	Succeeded         int
	AvgTurns          float64
	AvgDuration       time.Duration
	VisualTriggerRate float64 // B 轮里真正调过 visual_inspect 的任务比例;A 轮为 0
	TotalTokens       int
	AvgTokens         float64
	ByCategory        map[string]*CategoryStat
	ByVisualRequired  map[string]*CategoryStat
}

// CategoryStat 按 category 或 visual_required 维度的小聚合。
type CategoryStat struct {
	Total     int
	Succeeded int
}

// Summarize 把单轮 TaskResult 聚合为 Summary。
func Summarize(mode RunMode, results []*TaskResult) *Summary {
	s := &Summary{
		Mode:             mode,
		ByCategory:       map[string]*CategoryStat{},
		ByVisualRequired: map[string]*CategoryStat{},
	}
	if len(results) == 0 {
		return s
	}
	var totalTurns int
	var totalDur time.Duration
	var visualTriggered int
	for _, r := range results {
		s.Total++
		totalTurns += r.Turns
		totalDur += r.Duration
		s.TotalTokens += r.TokenCost
		if r.Success {
			s.Succeeded++
		}
		if r.VisualCalls > 0 {
			visualTriggered++
		}
		catKey := "uncategorized"
		vrKey := "unspecified"
		if r.Task != nil {
			if r.Task.Category != "" {
				catKey = r.Task.Category
			}
			if r.Task.VisualRequired != "" {
				vrKey = r.Task.VisualRequired
			}
		}
		addStat(s.ByCategory, catKey, r.Success)
		addStat(s.ByVisualRequired, vrKey, r.Success)
	}
	s.AvgTurns = float64(totalTurns) / float64(s.Total)
	s.AvgDuration = totalDur / time.Duration(s.Total)
	s.AvgTokens = float64(s.TotalTokens) / float64(s.Total)
	if mode == ModeWithVisual && s.Total > 0 {
		s.VisualTriggerRate = float64(visualTriggered) / float64(s.Total)
	}
	return s
}

func addStat(m map[string]*CategoryStat, key string, success bool) {
	c := m[key]
	if c == nil {
		c = &CategoryStat{}
		m[key] = c
	}
	c.Total++
	if success {
		c.Succeeded++
	}
}

// ABComparison 是对比 A/B 两轮得到的关键差值,驱动 ROI 决策。
type ABComparison struct {
	A               *Summary
	B               *Summary
	SuccessDelta    float64 // B 成功率 − A 成功率(百分比差,e.g. 0.25 = +25%)
	TurnInflation   float64 // B.AvgTurns / A.AvgTurns
	TokenInflation  float64 // B.AvgTokens / A.AvgTokens(A=0 时记 ∞,用 -1 表示)
	VisualTriggerPct float64
	Recommendation  string
	Rationale       string
}

// CompareAB 聚合 A/B 比较。A/B 任务数不一致视为异常,Rationale 标注。
func CompareAB(a, b *Summary) *ABComparison {
	cmp := &ABComparison{A: a, B: b}
	if a == nil || b == nil || a.Total == 0 || b.Total == 0 {
		cmp.Recommendation = "insufficient_data"
		cmp.Rationale = "at least one run has zero tasks"
		return cmp
	}
	mismatchNote := ""
	if a.Total != b.Total {
		mismatchNote = fmt.Sprintf(
			"task count mismatch between A(%d) and B(%d); comparison may be biased",
			a.Total, b.Total)
	}

	rateA := float64(a.Succeeded) / float64(a.Total)
	rateB := float64(b.Succeeded) / float64(b.Total)
	cmp.SuccessDelta = rateB - rateA

	if a.AvgTurns > 0 {
		cmp.TurnInflation = b.AvgTurns / a.AvgTurns
	}
	if a.AvgTokens > 0 {
		cmp.TokenInflation = b.AvgTokens / a.AvgTokens
	} else {
		cmp.TokenInflation = -1
	}
	cmp.VisualTriggerPct = b.VisualTriggerRate
	cmp.Recommendation, cmp.Rationale = DecideVisualROI(cmp)
	if mismatchNote != "" {
		// 保留 ROI 决策,同时把口径失准的提醒拼到 rationale 前面,
		// 避免读者以为结论是对等样本得出的。
		cmp.Rationale = mismatchNote + "; " + cmp.Rationale
	}
	return cmp
}

// DecideVisualROI 依据 README 里约定的阈值给出 tighten/keep/loosen 建议。
// 返回 (recommendation, rationale)。纯函数,便于单测。
//
// 阈值:
//   - Δ < 10%  且  token_ratio ≥ 2.0  → tighten
//   - Δ ≥ 30%                         → loosen
//   - 其他                             → keep
//
// token_ratio 未知(A 无 token 数据)时退化为"只看 Δ":
//   - Δ < 10%  → 不建议 tighten(证据不足),标 keep + rationale 提醒缺数据
func DecideVisualROI(cmp *ABComparison) (string, string) {
	if cmp == nil || cmp.A == nil || cmp.B == nil {
		return "insufficient_data", "nil summary"
	}
	delta := cmp.SuccessDelta
	ratio := cmp.TokenInflation
	switch {
	case delta >= 0.30:
		return "loosen", fmt.Sprintf(
			"success delta %.1f%% ≥ 30%%, visual_inspect carries its weight — keep fallback stage wide open",
			delta*100)
	case delta < 0.10 && ratio >= 2.0:
		return "tighten", fmt.Sprintf(
			"success delta %.1f%% < 10%% while token inflation %.2fx ≥ 2.0 — restrict visual_inspect to N consecutive pattern failures",
			delta*100, ratio)
	case delta < 0.10 && ratio < 0:
		return "keep", fmt.Sprintf(
			"success delta %.1f%% < 10%% but token cost unavailable — cannot justify tightening without cost data",
			delta*100)
	default:
		return "keep", fmt.Sprintf(
			"success delta %.1f%% in [10%%, 30%%) band — stay with current triggering rules, observe further",
			delta*100)
	}
}

// WriteMarkdown 输出对比报告到 path。A/B summary 各一节 + ABComparison 结论节。
func WriteMarkdown(path string, cmp *ABComparison,
	resultsA, resultsB []*TaskResult) error {

	var b strings.Builder
	b.WriteString("# Visual WebArena A/B 对比报告\n\n")
	b.WriteString(fmt.Sprintf("- 生成时间:%s\n", time.Now().Format(time.RFC3339)))
	if cmp == nil || cmp.A == nil || cmp.B == nil {
		b.WriteString("\n*比较数据缺失*\n")
		return os.WriteFile(path, []byte(b.String()), 0o644)
	}
	b.WriteString(fmt.Sprintf("- Run A(DOM-only,BrowserStage=%s): %d 任务\n",
		BrowserStageFor(ModeDOMOnly), cmp.A.Total))
	b.WriteString(fmt.Sprintf("- Run B(with visual,BrowserStage=%s): %d 任务\n",
		BrowserStageFor(ModeWithVisual), cmp.B.Total))

	b.WriteString("\n## ROI 结论\n\n")
	b.WriteString(fmt.Sprintf("- **建议**:%s\n", cmp.Recommendation))
	b.WriteString(fmt.Sprintf("- **理由**:%s\n", cmp.Rationale))
	b.WriteString(fmt.Sprintf("- 成功率差 Δ:%+.1f%%\n", cmp.SuccessDelta*100))
	b.WriteString(fmt.Sprintf("- Turn 膨胀:%.2fx\n", cmp.TurnInflation))
	if cmp.TokenInflation >= 0 {
		b.WriteString(fmt.Sprintf("- Token 膨胀:%.2fx\n", cmp.TokenInflation))
	} else {
		b.WriteString("- Token 膨胀:N/A(A 轮未上报 token)\n")
	}
	b.WriteString(fmt.Sprintf("- 视觉触发率(B 轮):%.1f%%\n", cmp.VisualTriggerPct*100))

	writeRun(&b, "A", cmp.A, resultsA)
	writeRun(&b, "B", cmp.B, resultsB)
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeRun(b *strings.Builder, label string, s *Summary, results []*TaskResult) {
	b.WriteString(fmt.Sprintf("\n## Run %s — %s\n\n", label, s.Mode))
	if s.Total == 0 {
		b.WriteString("*无任务结果*\n")
		return
	}
	successRate := float64(s.Succeeded) / float64(s.Total) * 100
	b.WriteString(fmt.Sprintf("- 总任务:%d\n", s.Total))
	b.WriteString(fmt.Sprintf("- 成功:%d (%.1f%%)\n", s.Succeeded, successRate))
	b.WriteString(fmt.Sprintf("- 平均 turn 数:%.1f\n", s.AvgTurns))
	b.WriteString(fmt.Sprintf("- 平均耗时:%s\n", s.AvgDuration.Round(time.Millisecond)))
	if s.TotalTokens > 0 {
		b.WriteString(fmt.Sprintf("- 平均 token:%.0f\n", s.AvgTokens))
	}
	if s.Mode == ModeWithVisual {
		b.WriteString(fmt.Sprintf("- visual_inspect 触发率:%.1f%%\n", s.VisualTriggerRate*100))
	}

	b.WriteString("\n### 按 visual_required\n\n")
	b.WriteString("| 维度 | 总数 | 成功 | 成功率 |\n")
	b.WriteString("|---|---:|---:|---:|\n")
	keys := sortedKeys(s.ByVisualRequired)
	for _, k := range keys {
		st := s.ByVisualRequired[k]
		rate := 0.0
		if st.Total > 0 {
			rate = float64(st.Succeeded) / float64(st.Total) * 100
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %.1f%% |\n", k, st.Total, st.Succeeded, rate))
	}

	b.WriteString("\n### 按 category\n\n")
	b.WriteString("| 类别 | 总数 | 成功 | 成功率 |\n")
	b.WriteString("|---|---:|---:|---:|\n")
	keys = sortedKeys(s.ByCategory)
	for _, k := range keys {
		st := s.ByCategory[k]
		rate := 0.0
		if st.Total > 0 {
			rate = float64(st.Succeeded) / float64(st.Total) * 100
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %.1f%% |\n", k, st.Total, st.Succeeded, rate))
	}

	b.WriteString("\n### 任务明细\n\n")
	b.WriteString("| ID | 类别 | 视觉需求 | 成功 | Turns | Visual 调用 | Token | 失败原因 |\n")
	b.WriteString("|---|---|---|:---:|---:|---:|---:|---|\n")
	for _, r := range results {
		cat, vr := "-", "-"
		if r.Task != nil {
			cat = r.Task.Category
			vr = r.Task.VisualRequired
		}
		ok := "✗"
		if r.Success {
			ok = "✓"
		}
		reason := r.FailReason
		if reason == "" {
			reason = "-"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %d | %d | %d | %s |\n",
			r.TaskID, cat, vr, ok, r.Turns, r.VisualCalls, r.TokenCost, reason))
	}
}

func sortedKeys(m map[string]*CategoryStat) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
