package e2e_longchain

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/errors"
)

// Summary 聚合一次 Run 的指标。指标定义对齐文档 40 §6.1 阶段 2。
type Summary struct {
	Total               int
	Succeeded           int
	AvgTurns            float64
	AvgTokens           float64
	AvgDuration         time.Duration
	AvgPatternSwitches  float64
	StepPassRate        float64 // (sum StepsPassed) / (sum TotalSteps)
	AnomalyRecoveredOK  int     // 期望走 recovery 且命中的总次数
	AnomalyRecoveryTotal int    // 期望走 recovery 的总步数
	ByFailClass         map[errors.ErrorClass]int
}

// Summarize 把 ChainResult 聚合为 Summary。
func Summarize(results []*ChainResult) *Summary {
	s := &Summary{ByFailClass: map[errors.ErrorClass]int{}}
	if len(results) == 0 {
		return s
	}
	var totalTurns, totalTokens, totalSteps, stepsPassed, totalSwitches int
	var totalDur time.Duration
	for _, r := range results {
		s.Total++
		totalTurns += r.Turns
		totalTokens += r.TokensUsed
		totalDur += r.Duration
		totalSwitches += r.PatternSwitches
		totalSteps += r.TotalSteps
		stepsPassed += r.StepsPassed
		if r.Success {
			s.Succeeded++
		} else {
			s.ByFailClass[classifyOrDefault(r.FailClass)]++
		}
		for _, st := range r.StepResults {
			if st.Anomaly != "" && (st.RecoveryOK || st.Pass) {
				// 进入该分支说明这步有 expect_anomaly;Pass 时 recovery 自动算成功。
				s.AnomalyRecoveryTotal++
				if st.RecoveryOK {
					s.AnomalyRecoveredOK++
				}
			} else if st.Anomaly != "" {
				s.AnomalyRecoveryTotal++
			}
		}
	}
	s.AvgTurns = float64(totalTurns) / float64(s.Total)
	if totalTokens > 0 {
		s.AvgTokens = float64(totalTokens) / float64(s.Total)
	}
	s.AvgDuration = totalDur / time.Duration(s.Total)
	s.AvgPatternSwitches = float64(totalSwitches) / float64(s.Total)
	if totalSteps > 0 {
		s.StepPassRate = float64(stepsPassed) / float64(totalSteps)
	}
	return s
}

// WriteMarkdown 输出 report.md。路径由调用方控制。
func WriteMarkdown(path string, results []*ChainResult, s *Summary) error {
	var b strings.Builder
	b.WriteString("# E2E 长链路评测报告\n\n")
	b.WriteString(fmt.Sprintf("- 生成时间:%s\n", time.Now().Format(time.RFC3339)))
	if s.Total == 0 {
		b.WriteString("\n*无任务结果*\n")
		return os.WriteFile(path, []byte(b.String()), 0o644)
	}
	succRate := float64(s.Succeeded) / float64(s.Total) * 100
	b.WriteString(fmt.Sprintf("- 总任务:%d\n", s.Total))
	b.WriteString(fmt.Sprintf("- 成功:%d (%.1f%%)  `target >= 65%%`\n", s.Succeeded, succRate))
	b.WriteString(fmt.Sprintf("- 单步通过率:%.1f%%\n", s.StepPassRate*100))
	b.WriteString(fmt.Sprintf("- 平均 turn 数:%.1f  `target <= 10`\n", s.AvgTurns))
	if s.AvgTokens > 0 {
		b.WriteString(fmt.Sprintf("- 平均 token:%.0f  `budget threshold ~ 100k`\n", s.AvgTokens))
	}
	b.WriteString(fmt.Sprintf("- 平均耗时:%s\n", s.AvgDuration.Round(time.Millisecond)))
	b.WriteString(fmt.Sprintf("- 平均 pattern 跨 category 切换:%.1f\n", s.AvgPatternSwitches))
	if s.AnomalyRecoveryTotal > 0 {
		rec := float64(s.AnomalyRecoveredOK) / float64(s.AnomalyRecoveryTotal) * 100
		b.WriteString(fmt.Sprintf("- 异常恢复:%d/%d (%.1f%%)\n", s.AnomalyRecoveredOK, s.AnomalyRecoveryTotal, rec))
	}

	// 失败按 ErrorClass 分布
	if len(s.ByFailClass) > 0 {
		b.WriteString("\n## 失败分类(sdk/errors.ErrorClass)\n\n")
		b.WriteString("| Class | 次数 |\n|---|---:|\n")
		classes := make([]string, 0, len(s.ByFailClass))
		for c := range s.ByFailClass {
			classes = append(classes, string(c))
		}
		sort.Strings(classes)
		for _, c := range classes {
			b.WriteString(fmt.Sprintf("| %s | %d |\n", c, s.ByFailClass[errors.ErrorClass(c)]))
		}
	}

	// 任务明细
	b.WriteString("\n## 任务明细\n\n")
	b.WriteString("| ID | 成功 | Steps | Turns | Tokens | 切换 | 失败 Class | 失败原因 |\n")
	b.WriteString("|---|:---:|---|---:|---:|---:|---|---|\n")
	for _, r := range results {
		ok := "✗"
		if r.Success {
			ok = "✓"
		}
		steps := fmt.Sprintf("%d/%d", r.StepsPassed, r.TotalSteps)
		class := "-"
		if !r.Success {
			class = string(classifyOrDefault(r.FailClass))
		}
		reason := r.FailReason
		if reason == "" {
			reason = "-"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %d | %d | %d | %s | %s |\n",
			r.TaskID, ok, steps, r.Turns, r.TokensUsed, r.PatternSwitches, class, reason))
	}

	// 单步命中明细(便于定位模式库跨 URL 迁移问题)
	b.WriteString("\n## 单步命中\n\n")
	b.WriteString("| Task | Step | 期望 pattern | 命中 | 异常 | 恢复 |\n")
	b.WriteString("|---|---|---|---|---|:---:|\n")
	for _, r := range results {
		for _, st := range r.StepResults {
			expected := "-"
			if r.Task != nil {
				for _, cs := range r.Task.Steps {
					if cs.Name == st.Name {
						expected = cs.PatternID
						break
					}
				}
			}
			matched := st.MatchedID
			if matched == "" {
				matched = "-"
			}
			anomaly := st.Anomaly
			if anomaly == "" {
				anomaly = "-"
			}
			rec := "-"
			if st.Anomaly != "" {
				if st.RecoveryOK {
					rec = "✓"
				} else {
					rec = "✗"
				}
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
				r.TaskID, st.Name, expected, matched, anomaly, rec))
		}
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
