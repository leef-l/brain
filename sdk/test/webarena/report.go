package webarena

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Summary 汇总一次 Run 的全局指标。
type Summary struct {
	Total       int
	Succeeded   int
	AvgTurns    float64
	AvgDuration time.Duration
	ByCategory  map[string]*CategoryStat
}

// CategoryStat 是按类别的子指标。
type CategoryStat struct {
	Total     int
	Succeeded int
}

// Summarize 把 TaskResult 聚合为 Summary。
func Summarize(results []*TaskResult) *Summary {
	s := &Summary{ByCategory: map[string]*CategoryStat{}}
	if len(results) == 0 {
		return s
	}
	var totalTurns int
	var totalDur time.Duration
	for _, r := range results {
		s.Total++
		totalTurns += r.Turns
		totalDur += r.Duration
		if r.Success {
			s.Succeeded++
		}
		cat := "uncategorized"
		if r.Task != nil && r.Task.Category != "" {
			cat = r.Task.Category
		}
		c := s.ByCategory[cat]
		if c == nil {
			c = &CategoryStat{}
			s.ByCategory[cat] = c
		}
		c.Total++
		if r.Success {
			c.Succeeded++
		}
	}
	s.AvgTurns = float64(totalTurns) / float64(s.Total)
	s.AvgDuration = totalDur / time.Duration(s.Total)
	return s
}

// WriteMarkdown 输出 report.md。
func WriteMarkdown(path string, results []*TaskResult, s *Summary) error {
	var b strings.Builder
	b.WriteString("# WebArena 回归报告\n\n")
	b.WriteString(fmt.Sprintf("- 生成时间:%s\n", time.Now().Format(time.RFC3339)))
	if s.Total == 0 {
		b.WriteString("\n*无任务结果*\n")
	} else {
		successRate := float64(s.Succeeded) / float64(s.Total) * 100
		b.WriteString(fmt.Sprintf("- 总任务:%d\n", s.Total))
		b.WriteString(fmt.Sprintf("- 成功:%d (%.1f%%)\n", s.Succeeded, successRate))
		b.WriteString(fmt.Sprintf("- 平均 turn 数:%.1f\n", s.AvgTurns))
		b.WriteString(fmt.Sprintf("- 平均耗时:%s\n", s.AvgDuration.Round(time.Millisecond)))
	}

	b.WriteString("\n## 按类别\n\n")
	b.WriteString("| 类别 | 总数 | 成功 | 成功率 |\n")
	b.WriteString("|---|---:|---:|---:|\n")
	cats := make([]string, 0, len(s.ByCategory))
	for k := range s.ByCategory {
		cats = append(cats, k)
	}
	sort.Strings(cats)
	for _, c := range cats {
		stat := s.ByCategory[c]
		rate := 0.0
		if stat.Total > 0 {
			rate = float64(stat.Succeeded) / float64(stat.Total) * 100
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %.1f%% |\n", c, stat.Total, stat.Succeeded, rate))
	}

	b.WriteString("\n## 任务明细\n\n")
	b.WriteString("| ID | 类别 | 成功 | Turns | 耗时 | 失败原因 |\n")
	b.WriteString("|---|---|:---:|---:|---:|---|\n")
	for _, r := range results {
		cat := "-"
		if r.Task != nil {
			cat = r.Task.Category
		}
		ok := "✗"
		if r.Success {
			ok = "✓"
		}
		reason := r.FailReason
		if reason == "" {
			reason = "-"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %d | %s | %s |\n",
			r.TaskID, cat, ok, r.Turns, r.Duration.Round(time.Millisecond), reason))
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}
