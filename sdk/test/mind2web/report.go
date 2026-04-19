package mind2web

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Summary 汇总一次 Run 的全局 + 跨站指标。
// 与 webarena.Summary 保持相似,额外含 BySite(跨站命中率矩阵)和
// PatternReuseRate(模式复用信号)。
type Summary struct {
	Total            int
	Succeeded        int
	AvgTurns         float64
	AvgDuration      time.Duration
	ByCategory       map[string]*CategoryStat
	BySite           map[string]*SiteStat           // 跨站命中率维度
	ByCategorySite   map[string]map[string]*CellStat // category → site → cell,用于跨站迁移矩阵
	PatternReuseRate float64                        // 命中过 pattern 的任务中,被复用的比例
	PatternCount     int                            // 独立 pattern id 总数
}

// CategoryStat 按类别统计。
type CategoryStat struct {
	Total     int
	Succeeded int
}

// SiteStat 按站点统计(Mind2Web 专有,用于看某个站对 Brain 是否特别难)。
type SiteStat struct {
	Total     int
	Succeeded int
}

// CellStat 是 category × site 的二维聚合单元。
type CellStat struct {
	Total     int
	Succeeded int
}

// Summarize 把 TaskResult 聚合为 Summary。
func Summarize(results []*TaskResult) *Summary {
	s := &Summary{
		ByCategory:     map[string]*CategoryStat{},
		BySite:         map[string]*SiteStat{},
		ByCategorySite: map[string]map[string]*CellStat{},
	}
	if len(results) == 0 {
		return s
	}
	var totalTurns int
	var totalDur time.Duration
	patternHits := map[string]int{}
	patternReused := 0
	patternTasks := 0

	for _, r := range results {
		s.Total++
		totalTurns += r.Turns
		totalDur += r.Duration
		if r.Success {
			s.Succeeded++
		}

		cat := "uncategorized"
		site := "unknown"
		if r.Task != nil {
			if r.Task.Category != "" {
				cat = r.Task.Category
			}
			if r.Task.Site != "" {
				site = r.Task.Site
			}
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

		ss := s.BySite[site]
		if ss == nil {
			ss = &SiteStat{}
			s.BySite[site] = ss
		}
		ss.Total++
		if r.Success {
			ss.Succeeded++
		}

		inner := s.ByCategorySite[cat]
		if inner == nil {
			inner = map[string]*CellStat{}
			s.ByCategorySite[cat] = inner
		}
		cell := inner[site]
		if cell == nil {
			cell = &CellStat{}
			inner[site] = cell
		}
		cell.Total++
		if r.Success {
			cell.Succeeded++
		}

		if r.PatternID != "" {
			patternTasks++
			patternHits[r.PatternID]++
			if r.PatternReused {
				patternReused++
			}
		}
	}

	s.AvgTurns = float64(totalTurns) / float64(s.Total)
	s.AvgDuration = totalDur / time.Duration(s.Total)
	s.PatternCount = len(patternHits)
	if patternTasks > 0 {
		s.PatternReuseRate = float64(patternReused) / float64(patternTasks)
	}
	return s
}

// WriteMarkdown 输出 report.md。
func WriteMarkdown(path string, results []*TaskResult, s *Summary) error {
	var b strings.Builder
	b.WriteString("# Mind2Web 跨站泛化回归报告\n\n")
	b.WriteString(fmt.Sprintf("- 生成时间:%s\n", time.Now().Format(time.RFC3339)))
	if s.Total == 0 {
		b.WriteString("\n*无任务结果*\n")
		return os.WriteFile(path, []byte(b.String()), 0o644)
	}

	successRate := float64(s.Succeeded) / float64(s.Total) * 100
	b.WriteString(fmt.Sprintf("- 总任务:%d\n", s.Total))
	b.WriteString(fmt.Sprintf("- 成功:%d (%.1f%%)\n", s.Succeeded, successRate))
	b.WriteString(fmt.Sprintf("- 平均 turn 数:%.1f\n", s.AvgTurns))
	b.WriteString(fmt.Sprintf("- 平均耗时:%s\n", s.AvgDuration.Round(time.Millisecond)))
	b.WriteString(fmt.Sprintf("- 触发 UIPattern 种类:%d\n", s.PatternCount))
	b.WriteString(fmt.Sprintf("- 模式复用率:%.1f%%(命中 pattern 的任务中,在本轮被复用过的比例)\n", s.PatternReuseRate*100))

	b.WriteString("\n## 按类别\n\n")
	b.WriteString("| 类别 | 总数 | 成功 | 成功率 |\n")
	b.WriteString("|---|---:|---:|---:|\n")
	cats := sortedKeys(s.ByCategory)
	for _, c := range cats {
		stat := s.ByCategory[c]
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %.1f%% |\n",
			c, stat.Total, stat.Succeeded, pct(stat.Succeeded, stat.Total)))
	}

	b.WriteString("\n## 按站点(跨站难度)\n\n")
	b.WriteString("| Site | 总数 | 成功 | 成功率 |\n")
	b.WriteString("|---|---:|---:|---:|\n")
	sites := sortedSiteKeys(s.BySite)
	for _, site := range sites {
		stat := s.BySite[site]
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %.1f%% |\n",
			site, stat.Total, stat.Succeeded, pct(stat.Succeeded, stat.Total)))
	}

	b.WriteString("\n## 跨站迁移矩阵(category × site)\n\n")
	b.WriteString("每格显示 `成功/总数`,空格表示该组合没有任务。\n\n")
	siteHeader := strings.Join(sites, " | ")
	b.WriteString("| 类别 | " + siteHeader + " |\n")
	b.WriteString("|---|" + strings.Repeat("---:|", len(sites)) + "\n")
	for _, c := range cats {
		row := "| " + c + " |"
		for _, site := range sites {
			cell := s.ByCategorySite[c][site]
			if cell == nil {
				row += "  |"
			} else {
				row += fmt.Sprintf(" %d/%d |", cell.Succeeded, cell.Total)
			}
		}
		b.WriteString(row + "\n")
	}

	b.WriteString("\n## 任务明细\n\n")
	b.WriteString("| ID | 类别 | Site | 成功 | Turns | Pattern | 复用? | 失败原因 |\n")
	b.WriteString("|---|---|---|:---:|---:|---|:---:|---|\n")
	for _, r := range results {
		cat, site := "-", "-"
		if r.Task != nil {
			cat = r.Task.Category
			site = r.Task.Site
		}
		ok := "failed"
		if r.Success {
			ok = "ok"
		}
		pattern := r.PatternID
		if pattern == "" {
			pattern = "-"
		}
		reused := "-"
		if r.PatternReused {
			reused = "yes"
		}
		reason := r.FailReason
		if reason == "" {
			reason = "-"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %d | %s | %s | %s |\n",
			r.TaskID, cat, site, ok, r.Turns, pattern, reused, reason))
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func sortedKeys(m map[string]*CategoryStat) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSiteKeys(m map[string]*SiteStat) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func pct(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom) * 100
}
