// smart_scheduler.go — MACCS Wave 4 Batch 2: 智能重排调度（DefaultTaskScheduler 的可选增强工具）
//
// 定位：这是 DefaultTaskScheduler（scheduler.go）的**可选辅助/增强组件**，
// 不是独立的第三套调度器。主线调度路径仍是 DefaultTaskScheduler.Plan()
// （拓扑排序 + L1 brain 选择 + 优先级批次）。
//
// SmartScheduler 在拓扑分层之后，提供两类增强能力：
//  1. 贪心冲突分离：Reschedule(layers, decls) — 把同层有资源冲突的任务挤到下一层
//  2. 并行度建议：SuggestParallelism(decls) — 根据冲突比例建议合理的最大并行度
//
// 二者都依赖 ConflictDetector / DeadlockDetector 提供的资源声明检测。
// 当上层（如 BatchPlanner / Orchestrator）需要在拓扑层之上做"冲突感知重排"时，
// 才会使用本组件。它**不替代**也**不绕开** DefaultTaskScheduler。

package kernel

import (
	"fmt"
	"strings"
)

// ─── 调度约束 ─────────────────────────────────────────────────────

// ScheduleConstraint 表示两个任务之间的调度约束。
type ScheduleConstraint struct {
	TaskA         string `json:"task_a"`
	TaskB         string `json:"task_b"`
	MustSerialize bool   `json:"must_serialize"` // 必须串行
	Reason        string `json:"reason"`
}

// ─── 重排结果 ─────────────────────────────────────────────────────

// SmartScheduleResult 包含智能重排的完整结果。
type SmartScheduleResult struct {
	OriginalLayers   [][]string           `json:"original_layers"`   // 原始分层
	OptimizedLayers  [][]string           `json:"optimized_layers"`  // 优化后分层
	Constraints      []ScheduleConstraint `json:"constraints"`       // 应用的约束
	ConflictsAvoided int                  `json:"conflicts_avoided"` // 避免的冲突数
	LayersDelta      int                  `json:"layers_delta"`      // 层数变化（正数=增加了层）
	Explanation      string               `json:"explanation"`
}

// ─── 智能调度器 ───────────────────────────────────────────────────

// SmartScheduler 在冲突检测的基础上自动重排并行层，确保同层任务无资源冲突。
type SmartScheduler struct {
	detector    ConflictDetector
	deadlockDet *DeadlockDetector
	maxParallel int // 单层最大并行数
}

// NewSmartScheduler 创建智能调度器。
func NewSmartScheduler(detector ConflictDetector, deadlockDet *DeadlockDetector, maxParallel int) *SmartScheduler {
	if maxParallel <= 0 {
		maxParallel = 4
	}
	return &SmartScheduler{
		detector:    detector,
		deadlockDet: deadlockDet,
		maxParallel: maxParallel,
	}
}

// ─── 核心方法 ─────────────────────────────────────────────────────

// Reschedule 对已有分层做冲突感知重排。
// 算法：遍历每层，对同层任务用 ConflictDetector.Detect 检测冲突，
// 将冲突任务移到下一层，直到所有层无冲突。
func (s *SmartScheduler) Reschedule(layers [][]string, declarations map[string]TaskResourceDecl) *SmartScheduleResult {
	result := &SmartScheduleResult{
		OriginalLayers: copyLayers(layers),
	}

	// 工作副本
	work := copyLayers(layers)
	var constraints []ScheduleConstraint
	conflictsAvoided := 0

	for i := 0; i < len(work); i++ {
		if len(work[i]) <= 1 {
			continue
		}

		safe, deferred := s.splitConflictingTasks(work[i], declarations)

		// 如果有冲突任务需要延迟
		if len(deferred) > 0 {
			// 为冲突任务生成约束记录
			layerDecls := s.buildDeclarationsFromLayer(work[i], declarations)
			report := s.detector.Detect(layerDecls)
			for _, c := range report.Conflicts {
				if len(c.TaskIDs) >= 2 {
					constraints = append(constraints, ScheduleConstraint{
						TaskA:         c.TaskIDs[0],
						TaskB:         c.TaskIDs[1],
						MustSerialize: c.Severity == SeverityBlocker || c.Severity == SeverityCritical,
						Reason:        c.Description,
					})
				}
			}
			conflictsAvoided += report.TotalCount

			// 保留安全任务在当前层
			work[i] = safe

			// 将延迟任务插入下一层
			if i+1 < len(work) {
				work[i+1] = append(deferred, work[i+1]...)
			} else {
				work = append(work, deferred)
			}
		}

		// 如果单层超出 maxParallel，拆分
		if len(work[i]) > s.maxParallel {
			overflow := work[i][s.maxParallel:]
			work[i] = work[i][:s.maxParallel]
			if i+1 < len(work) {
				work[i+1] = append(overflow, work[i+1]...)
			} else {
				work = append(work, overflow)
			}
		}
	}

	// 移除空层
	var cleaned [][]string
	for _, layer := range work {
		if len(layer) > 0 {
			cleaned = append(cleaned, layer)
		}
	}

	result.OptimizedLayers = cleaned
	result.Constraints = constraints
	result.ConflictsAvoided = conflictsAvoided
	result.LayersDelta = len(cleaned) - len(layers)
	result.Explanation = s.buildExplanation(layers, cleaned, conflictsAvoided)
	return result
}

// OptimizePlan 对 TaskPlan 做智能重排调度。
// 先调用 plan.ComputeParallelLayers() 获取原始分层，然后调用 Reschedule。
func (s *SmartScheduler) OptimizePlan(plan *TaskPlan, declarations map[string]TaskResourceDecl) *SmartScheduleResult {
	if plan == nil || len(plan.SubTasks) == 0 {
		return &SmartScheduleResult{Explanation: "empty plan, nothing to optimize"}
	}

	// 计算拓扑分层
	if err := plan.ComputeParallelLayers(); err != nil {
		return &SmartScheduleResult{Explanation: fmt.Sprintf("compute layers failed: %v", err)}
	}

	// 如果缺少声明，从 PlanSubTask 推断
	if declarations == nil {
		declarations = make(map[string]TaskResourceDecl)
	}
	for _, sub := range plan.SubTasks {
		if _, ok := declarations[sub.TaskID]; !ok {
			declarations[sub.TaskID] = TaskResourceDecl{
				TaskID:    sub.TaskID,
				BrainKind: string(sub.Kind),
			}
		}
	}

	return s.Reschedule(plan.ParallelLayers, declarations)
}

// SuggestParallelism 分析所有声明的资源重叠情况，建议最优并行度。
// 返回避免大部分冲突的最大并行数。
func (s *SmartScheduler) SuggestParallelism(declarations []TaskResourceDecl) int {
	n := len(declarations)
	if n <= 1 {
		return n
	}

	// 统计冲突对数
	conflictPairs := 0
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			conflicts := s.detector.DetectPair(declarations[i], declarations[j])
			if len(conflicts) > 0 {
				conflictPairs++
			}
		}
	}

	totalPairs := n * (n - 1) / 2
	if totalPairs == 0 {
		return n
	}

	// 冲突比例
	conflictRatio := float64(conflictPairs) / float64(totalPairs)

	// 无冲突：全部并行
	if conflictPairs == 0 {
		return n
	}

	// 高冲突（>50%）：建议串行或低并行
	if conflictRatio > 0.5 {
		suggested := 2
		if suggested > n {
			suggested = n
		}
		return suggested
	}

	// 中等冲突：按比例缩减
	suggested := int(float64(n) * (1.0 - conflictRatio))
	if suggested < 2 {
		suggested = 2
	}
	if suggested > s.maxParallel {
		suggested = s.maxParallel
	}
	return suggested
}

// ValidateSchedule 验证给定的分层调度是否仍有冲突。
// 返回所有层中仍然存在的冲突列表。
func (s *SmartScheduler) ValidateSchedule(layers [][]string, declarations map[string]TaskResourceDecl) []Conflict {
	var all []Conflict
	for _, layer := range layers {
		if len(layer) <= 1 {
			continue
		}
		decls := s.buildDeclarationsFromLayer(layer, declarations)
		report := s.detector.Detect(decls)
		all = append(all, report.Conflicts...)
	}
	return all
}

// ─── 内部辅助 ─────────────────────────────────────────────────────

// splitConflictingTasks 将一层拆分为安全组和延迟组。
// 贪心策略：依次尝试将任务加入安全组，若与已有安全任务冲突则延迟。
func (s *SmartScheduler) splitConflictingTasks(layer []string, declarations map[string]TaskResourceDecl) (safe []string, deferred []string) {
	var safeDecls []TaskResourceDecl

	for _, taskID := range layer {
		decl, ok := declarations[taskID]
		if !ok {
			// 没有声明的任务默认安全
			safe = append(safe, taskID)
			continue
		}

		// 检测与已有安全任务的冲突
		conflicts := s.detector.CheckNewTask(safeDecls, decl)
		if len(conflicts) == 0 {
			safe = append(safe, taskID)
			safeDecls = append(safeDecls, decl)
		} else {
			deferred = append(deferred, taskID)
		}
	}
	return
}

// buildDeclarationsFromLayer 从全量声明中提取指定任务的声明列表。
func (s *SmartScheduler) buildDeclarationsFromLayer(taskIDs []string, allDecls map[string]TaskResourceDecl) []TaskResourceDecl {
	var out []TaskResourceDecl
	for _, id := range taskIDs {
		if decl, ok := allDecls[id]; ok {
			out = append(out, decl)
		}
	}
	return out
}

// buildExplanation 生成人可读的重排说明。
func (s *SmartScheduler) buildExplanation(original, optimized [][]string, conflictsAvoided int) string {
	if conflictsAvoided == 0 {
		return "no conflicts detected, schedule unchanged"
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("avoided %d conflict(s)", conflictsAvoided))
	delta := len(optimized) - len(original)
	if delta > 0 {
		parts = append(parts, fmt.Sprintf("added %d layer(s) for serialization", delta))
	} else if delta == 0 {
		parts = append(parts, "layer count unchanged")
	}
	return strings.Join(parts, "; ")
}

// copyLayers 深拷贝分层。
func copyLayers(layers [][]string) [][]string {
	if layers == nil {
		return nil
	}
	out := make([][]string, len(layers))
	for i, layer := range layers {
		cp := make([]string, len(layer))
		copy(cp, layer)
		out[i] = cp
	}
	return out
}
