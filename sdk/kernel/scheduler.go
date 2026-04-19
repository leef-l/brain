package kernel

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/leef-l/brain/sdk/agent"
)

// TaskScheduler 是任务级调度引擎的核心接口。
// 与工具级的 BatchPlanner（单 turn 内 tool_call 并发分组）不同，
// TaskScheduler 处理跨 task 的依赖检查、brain 选择和执行批次规划。
type TaskScheduler interface {
	// Plan 根据任务列表生成执行计划：多个有序批次，批次内可并行。
	Plan(ctx context.Context, tasks []SchedulableTask) (*TaskSchedulePlan, error)

	// SelectBrain 根据 L1 学习结果为单个任务选择最优 brain。
	// candidates 为空时返回 error。
	SelectBrain(ctx context.Context, taskType string, candidates []agent.Kind) (agent.Kind, error)
}

// SchedulableTask 是可调度的任务描述。
type SchedulableTask struct {
	ID        string     `json:"id"`
	TaskType  string     `json:"task_type"`
	BrainKind agent.Kind `json:"brain_kind,omitempty"`
	DependsOn []string   `json:"depends_on,omitempty"`
	Priority  int        `json:"priority,omitempty"`
}

// TaskSchedulePlan 是调度器输出的执行计划。
type TaskSchedulePlan struct {
	Batches []TaskBatch `json:"batches"`
}

// TaskBatch 是一个可并行执行的任务批次。
type TaskBatch struct {
	Tasks           []SchedulableTask       `json:"tasks"`
	BrainAssignment map[string]agent.Kind   `json:"brain_assignment"`
}

// DefaultTaskScheduler 是 TaskScheduler 的默认实现。
// 使用拓扑排序处理依赖，通过 LearningEngine 排名选择 brain。
type DefaultTaskScheduler struct {
	learner   *LearningEngine
	available func() []agent.Kind
	mu        sync.RWMutex
}

// NewDefaultTaskScheduler 创建默认调度器。
// learner 可为 nil（退化为无学习反馈的纯拓扑调度）。
// availableFn 返回当前可用的 brain 列表。
func NewDefaultTaskScheduler(learner *LearningEngine, availableFn func() []agent.Kind) *DefaultTaskScheduler {
	return &DefaultTaskScheduler{
		learner:   learner,
		available: availableFn,
	}
}

func (s *DefaultTaskScheduler) Plan(ctx context.Context, tasks []SchedulableTask) (*TaskSchedulePlan, error) {
	if len(tasks) == 0 {
		return &TaskSchedulePlan{}, nil
	}

	// 构建 ID → task 索引
	taskMap := make(map[string]*SchedulableTask, len(tasks))
	for i := range tasks {
		taskMap[tasks[i].ID] = &tasks[i]
	}

	// 验证依赖
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := taskMap[dep]; !ok {
				return nil, fmt.Errorf("task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}

	// 拓扑排序（Kahn's algorithm）
	inDegree := make(map[string]int, len(tasks))
	downstream := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		if _, ok := inDegree[t.ID]; !ok {
			inDegree[t.ID] = 0
		}
		for _, dep := range t.DependsOn {
			inDegree[t.ID]++
			downstream[dep] = append(downstream[dep], t.ID)
		}
	}

	var batches []TaskBatch
	remaining := len(tasks)

	for remaining > 0 {
		// 收集入度为 0 的任务
		var ready []SchedulableTask
		for _, t := range tasks {
			if inDegree[t.ID] == 0 {
				ready = append(ready, t)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("cycle detected in task dependencies")
		}

		// 按优先级排序（高优先级先执行）
		sort.SliceStable(ready, func(i, j int) bool {
			return ready[i].Priority > ready[j].Priority
		})

		// 为每个任务分配 brain
		assignment := make(map[string]agent.Kind, len(ready))
		for _, t := range ready {
			if t.BrainKind != "" {
				assignment[t.ID] = t.BrainKind
			} else {
				brain, err := s.SelectBrain(ctx, t.TaskType, s.getAvailable())
				if err == nil {
					assignment[t.ID] = brain
				}
			}
		}

		batches = append(batches, TaskBatch{
			Tasks:           ready,
			BrainAssignment: assignment,
		})

		// 从图中移除已调度的任务
		for _, t := range ready {
			inDegree[t.ID] = -1
			remaining--
			for _, child := range downstream[t.ID] {
				inDegree[child]--
			}
		}
	}

	return &TaskSchedulePlan{Batches: batches}, nil
}

func (s *DefaultTaskScheduler) SelectBrain(ctx context.Context, taskType string, candidates []agent.Kind) (agent.Kind, error) {
	if len(candidates) == 0 {
		return "", fmt.Errorf("no candidates available")
	}
	if len(candidates) == 1 || s.learner == nil {
		return candidates[0], nil
	}

	// 使用 L1 学习引擎排名
	rankings := s.learner.RankBrains(taskType, WeightPolicy{})
	rankMap := make(map[agent.Kind]float64, len(rankings))
	for _, r := range rankings {
		rankMap[r.BrainKind] = r.Score
	}

	best := candidates[0]
	bestScore := rankMap[best]
	for _, c := range candidates[1:] {
		if rankMap[c] > bestScore {
			best = c
			bestScore = rankMap[c]
		}
	}
	return best, nil
}

func (s *DefaultTaskScheduler) getAvailable() []agent.Kind {
	if s.available == nil {
		return nil
	}
	return s.available()
}

// WithTaskScheduler 注入 TaskScheduler 到 Kernel。
func WithTaskScheduler(ts TaskScheduler) Option {
	return func(k *Kernel) {
		k.TaskScheduler = ts
	}
}
