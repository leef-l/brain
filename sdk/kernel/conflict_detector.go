// conflict_detector.go — MACCS Wave 4: 并发控制与冲突仲裁
//
// 冲突检测器分析并行任务之间的资源冲突（文件读写、端口绑定、循环依赖），
// 生成冲突报告供调度器决策。

package kernel

import (
	"fmt"
	"strings"
	"time"
)

// ─── 冲突类型 ─────────────────────────────────────────────────────

// ConflictType 表示冲突的类别。
type ConflictType string

const (
	ConflictFileWrite     ConflictType = "file_write"      // 多个任务写同一文件
	ConflictFileReadWrite ConflictType = "file_read_write"  // 一个读一个写
	ConflictPortBind      ConflictType = "port_bind"        // 多个任务绑同一端口
	ConflictDependency    ConflictType = "dependency"       // 依赖冲突（循环依赖）
	ConflictResource      ConflictType = "resource_general" // 通用资源冲突
)

// ─── 冲突严重度 ───────────────────────────────────────────────────

// ConflictSeverity 表示冲突的严重程度。
type ConflictSeverity string

const (
	SeverityBlocker  ConflictSeverity = "blocker"  // 必须解决，否则无法继续
	SeverityCritical ConflictSeverity = "critical" // 严重，可能导致数据损坏
	SeverityWarning  ConflictSeverity = "warning"  // 警告，可能影响结果
	SeverityInfo     ConflictSeverity = "info"     // 信息，供参考
)

// ─── 冲突记录 ─────────────────────────────────────────────────────

// Conflict 表示一条具体的资源冲突。
type Conflict struct {
	ConflictID   string           `json:"conflict_id"`
	Type         ConflictType     `json:"type"`
	Severity     ConflictSeverity `json:"severity"`
	ResourcePath string           `json:"resource_path"`
	TaskIDs      []string         `json:"task_ids"`
	Description  string           `json:"description"`
	DetectedAt   time.Time        `json:"detected_at"`
	Resolved     bool             `json:"resolved"`
	Resolution   string           `json:"resolution,omitempty"`
}

// ─── 冲突报告 ─────────────────────────────────────────────────────

// ConflictReport 汇总一次检测的所有冲突。
type ConflictReport struct {
	ReportID      string     `json:"report_id"`
	Conflicts     []Conflict `json:"conflicts"`
	TotalCount    int        `json:"total_count"`
	BlockerCount  int        `json:"blocker_count"`
	CriticalCount int        `json:"critical_count"`
	WarningCount  int        `json:"warning_count"`
	HasBlockers   bool       `json:"has_blockers"`
	GeneratedAt   time.Time  `json:"generated_at"`
}

// Summary 返回冲突报告的一行摘要。
func (r *ConflictReport) Summary() string {
	if r.TotalCount == 0 {
		return "no conflicts detected"
	}
	return fmt.Sprintf("%d conflicts (blocker=%d critical=%d warning=%d)",
		r.TotalCount, r.BlockerCount, r.CriticalCount, r.WarningCount)
}

// ─── 任务资源声明 ─────────────────────────────────────────────────

// TaskResourceDecl 描述单个任务对外声明的资源使用意图。
type TaskResourceDecl struct {
	TaskID       string   `json:"task_id"`
	BrainKind    string   `json:"brain_kind"`
	ReadPaths    []string `json:"read_paths"`
	WritePaths   []string `json:"write_paths"`
	Ports        []int    `json:"ports,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

// ─── 接口 ─────────────────────────────────────────────────────────

// ConflictDetector 定义冲突检测能力。
type ConflictDetector interface {
	// Detect 对所有任务声明做全量冲突检测。
	Detect(declarations []TaskResourceDecl) *ConflictReport
	// DetectPair 检测两个任务之间的冲突。
	DetectPair(a, b TaskResourceDecl) []Conflict
	// CheckNewTask 检测新任务与已有任务集的冲突。
	CheckNewTask(existing []TaskResourceDecl, newTask TaskResourceDecl) []Conflict
}

// ─── 默认实现 ─────────────────────────────────────────────────────

// DefaultConflictDetector 提供基于资源声明的冲突检测。
type DefaultConflictDetector struct {
	seq int // 冲突 ID 自增序列
}

// NewConflictDetector 创建默认冲突检测器。
func NewConflictDetector() *DefaultConflictDetector {
	return &DefaultConflictDetector{}
}

// Detect 对所有任务对做两两检测 + 循环依赖检测，汇总生成报告。
func (d *DefaultConflictDetector) Detect(declarations []TaskResourceDecl) *ConflictReport {
	now := time.Now()
	var all []Conflict

	// 两两检测
	for i := 0; i < len(declarations); i++ {
		for j := i + 1; j < len(declarations); j++ {
			all = append(all, d.DetectPair(declarations[i], declarations[j])...)
		}
	}

	// 循环依赖检测
	all = append(all, d.hasCyclicDeps(declarations)...)

	report := &ConflictReport{
		ReportID:    fmt.Sprintf("rpt-%d", now.UnixMilli()),
		Conflicts:   all,
		TotalCount:  len(all),
		GeneratedAt: now,
	}
	for _, c := range all {
		switch c.Severity {
		case SeverityBlocker:
			report.BlockerCount++
		case SeverityCritical:
			report.CriticalCount++
		case SeverityWarning:
			report.WarningCount++
		}
	}
	report.HasBlockers = report.BlockerCount > 0
	return report
}

// DetectPair 检测两个任务之间的文件冲突和端口冲突。
func (d *DefaultConflictDetector) DetectPair(a, b TaskResourceDecl) []Conflict {
	now := time.Now()
	var out []Conflict

	// 写-写冲突: a.WritePaths × b.WritePaths
	for _, wp1 := range a.WritePaths {
		for _, wp2 := range b.WritePaths {
			if pathOverlaps(wp1, wp2) {
				d.seq++
				out = append(out, Conflict{
					ConflictID:   fmt.Sprintf("cfl-%d", d.seq),
					Type:         ConflictFileWrite,
					Severity:     SeverityBlocker,
					ResourcePath: wp1,
					TaskIDs:      []string{a.TaskID, b.TaskID},
					Description:  fmt.Sprintf("tasks %s and %s both write to overlapping path %q / %q", a.TaskID, b.TaskID, wp1, wp2),
					DetectedAt:   now,
				})
			}
		}
	}

	// 读-写冲突: a.ReadPaths × b.WritePaths
	for _, rp := range a.ReadPaths {
		for _, wp := range b.WritePaths {
			if pathOverlaps(rp, wp) {
				d.seq++
				out = append(out, Conflict{
					ConflictID:   fmt.Sprintf("cfl-%d", d.seq),
					Type:         ConflictFileReadWrite,
					Severity:     SeverityCritical,
					ResourcePath: rp,
					TaskIDs:      []string{a.TaskID, b.TaskID},
					Description:  fmt.Sprintf("task %s reads %q while task %s writes %q", a.TaskID, rp, b.TaskID, wp),
					DetectedAt:   now,
				})
			}
		}
	}

	// 读-写冲突: b.ReadPaths × a.WritePaths
	for _, rp := range b.ReadPaths {
		for _, wp := range a.WritePaths {
			if pathOverlaps(rp, wp) {
				d.seq++
				out = append(out, Conflict{
					ConflictID:   fmt.Sprintf("cfl-%d", d.seq),
					Type:         ConflictFileReadWrite,
					Severity:     SeverityCritical,
					ResourcePath: rp,
					TaskIDs:      []string{b.TaskID, a.TaskID},
					Description:  fmt.Sprintf("task %s reads %q while task %s writes %q", b.TaskID, rp, a.TaskID, wp),
					DetectedAt:   now,
				})
			}
		}
	}

	// 端口冲突
	portSet := make(map[int]bool, len(a.Ports))
	for _, p := range a.Ports {
		portSet[p] = true
	}
	for _, p := range b.Ports {
		if portSet[p] {
			d.seq++
			out = append(out, Conflict{
				ConflictID:   fmt.Sprintf("cfl-%d", d.seq),
				Type:         ConflictPortBind,
				Severity:     SeverityBlocker,
				ResourcePath: fmt.Sprintf("port:%d", p),
				TaskIDs:      []string{a.TaskID, b.TaskID},
				Description:  fmt.Sprintf("tasks %s and %s both bind port %d", a.TaskID, b.TaskID, p),
				DetectedAt:   now,
			})
		}
	}

	return out
}

// CheckNewTask 检测新任务与已有任务集之间的冲突。
func (d *DefaultConflictDetector) CheckNewTask(existing []TaskResourceDecl, newTask TaskResourceDecl) []Conflict {
	var out []Conflict
	for _, ex := range existing {
		out = append(out, d.DetectPair(ex, newTask)...)
	}
	return out
}

// ─── 辅助函数 ─────────────────────────────────────────────────────

// pathOverlaps 判断两个路径是否重叠。
// 完全相等或其中一个是另一个的目录前缀（以 "/" 结尾的目录匹配子路径）。
func pathOverlaps(a, b string) bool {
	if a == b {
		return true
	}
	// 规范化：确保目录以 "/" 结尾时能正确匹配
	a = strings.TrimRight(a, "/")
	b = strings.TrimRight(b, "/")
	if a == b {
		return true
	}
	// a 是 b 的目录前缀
	if strings.HasPrefix(b, a+"/") {
		return true
	}
	// b 是 a 的目录前缀
	if strings.HasPrefix(a, b+"/") {
		return true
	}
	return false
}

// hasCyclicDeps 检测任务声明中的循环依赖。
// 使用 DFS 着色法：0=未访问，1=访问中，2=已完成。
func (d *DefaultConflictDetector) hasCyclicDeps(declarations []TaskResourceDecl) []Conflict {
	// 构建邻接表
	graph := make(map[string][]string)
	taskSet := make(map[string]bool)
	for _, decl := range declarations {
		taskSet[decl.TaskID] = true
		graph[decl.TaskID] = decl.Dependencies
	}

	color := make(map[string]int) // 0=white, 1=gray, 2=black
	var cycleMembers []string
	now := time.Now()

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = 1
		for _, dep := range graph[node] {
			if !taskSet[dep] {
				continue // 依赖的任务不在本次声明中，跳过
			}
			if color[dep] == 1 {
				cycleMembers = append(cycleMembers, node, dep)
				return true
			}
			if color[dep] == 0 {
				if dfs(dep) {
					return true
				}
			}
		}
		color[node] = 2
		return false
	}

	var out []Conflict
	for _, decl := range declarations {
		if color[decl.TaskID] == 0 {
			cycleMembers = nil
			if dfs(decl.TaskID) {
				d.seq++
				// 去重收集参与循环的任务 ID
				seen := make(map[string]bool)
				var ids []string
				for _, id := range cycleMembers {
					if !seen[id] {
						seen[id] = true
						ids = append(ids, id)
					}
				}
				out = append(out, Conflict{
					ConflictID:   fmt.Sprintf("cfl-%d", d.seq),
					Type:         ConflictDependency,
					Severity:     SeverityBlocker,
					ResourcePath: "dependency-graph",
					TaskIDs:      ids,
					Description:  fmt.Sprintf("cyclic dependency detected among tasks: %s", strings.Join(ids, " -> ")),
					DetectedAt:   now,
				})
			}
		}
	}
	return out
}
