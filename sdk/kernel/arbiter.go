// arbiter.go — MACCS Wave 4: 冲突仲裁策略
//
// 当 ConflictDetector 检测到资源冲突时，Arbiter 决定如何解决：
// 串行化、优先级抢占、合并、分区、中止等。

package kernel

import (
	"fmt"
	"time"
)

// ─── 仲裁策略枚举 ─────────────────────────────────────────────────

// ArbiterStrategy 描述冲突解决策略。
type ArbiterStrategy string

const (
	StrategySerialize ArbiterStrategy = "serialize" // 串行化：冲突任务排队执行
	StrategyPriority  ArbiterStrategy = "priority"  // 优先级抢占：高优先级任务先执行
	StrategyMerge     ArbiterStrategy = "merge"      // 合并：将冲突任务合并为一个
	StrategyPartition ArbiterStrategy = "partition"  // 分区：将资源分区给不同任务
	StrategyAbort     ArbiterStrategy = "abort"      // 中止：中止低优先级任务
)

// ─── 仲裁决定 ─────────────────────────────────────────────────────

// ArbiterDecision 描述一次仲裁的决定结果。
type ArbiterDecision struct {
	DecisionID  string          `json:"decision_id"`
	ConflictID  string          `json:"conflict_id"`
	Strategy    ArbiterStrategy `json:"strategy"`
	WinnerTasks []string        `json:"winner_tasks"` // 可以继续的任务
	LoserTasks  []string        `json:"loser_tasks"`  // 需要等待/中止的任务
	Reason      string          `json:"reason"`
	Actions     []ArbiterAction `json:"actions"`
	DecidedAt   time.Time       `json:"decided_at"`
}

// ─── 仲裁动作 ─────────────────────────────────────────────────────

// ArbiterAction 描述对某个任务需要执行的具体操作。
type ArbiterAction struct {
	ActionType  string        `json:"action_type"`      // wait/abort/retry/requeue/merge
	TaskID      string        `json:"task_id"`
	Description string        `json:"description"`
	Delay       time.Duration `json:"delay,omitempty"` // wait 策略的等待时间
}

// ─── 任务优先级信息 ───────────────────────────────────────────────

// TaskPriorityInfo 携带任务的优先级信息，供仲裁器决策。
type TaskPriorityInfo struct {
	TaskID    string     `json:"task_id"`
	Priority  int        `json:"priority"`             // 1=highest
	BrainKind string     `json:"brain_kind"`
	StartedAt *time.Time `json:"started_at,omitempty"` // 已开始的任务优先级更高
	Critical  bool       `json:"critical"`             // 关键路径任务
}

// ─── 接口 ─────────────────────────────────────────────────────────

// Arbiter 定义冲突仲裁能力。
type Arbiter interface {
	// Arbitrate 对单个冲突做出仲裁决定。
	Arbitrate(conflict Conflict, priorities map[string]TaskPriorityInfo) *ArbiterDecision
	// ArbitrateAll 对冲突报告中的所有冲突做出仲裁决定。
	ArbitrateAll(report *ConflictReport, priorities map[string]TaskPriorityInfo) []*ArbiterDecision
	// ResolveDeadlock 解决死锁环：选择 victim 中止以打破循环。
	ResolveDeadlock(cycle DeadlockCycle, priorities map[string]TaskPriorityInfo) *ArbiterDecision
}

// ─── 默认实现 ─────────────────────────────────────────────────────

// DefaultArbiter 基于策略规则的冲突仲裁器。
type DefaultArbiter struct {
	defaultStrategy ArbiterStrategy
	strategyRules   map[ConflictType]ArbiterStrategy // 按冲突类型选策略
	seq             int
}

// NewDefaultArbiter 创建带默认策略映射的仲裁器。
func NewDefaultArbiter() *DefaultArbiter {
	return &DefaultArbiter{
		defaultStrategy: StrategyPriority,
		strategyRules: map[ConflictType]ArbiterStrategy{
			ConflictFileWrite:     StrategySerialize,
			ConflictFileReadWrite: StrategyPriority,
			ConflictPortBind:      StrategyAbort,
			ConflictDependency:    StrategySerialize,
			ConflictResource:      StrategyPriority,
		},
	}
}

// NewArbiterWithStrategy 创建使用自定义默认策略的仲裁器。
func NewArbiterWithStrategy(defaultStrategy ArbiterStrategy) *DefaultArbiter {
	a := NewDefaultArbiter()
	a.defaultStrategy = defaultStrategy
	return a
}

// SetStrategy 覆盖某类冲突的仲裁策略。
func (a *DefaultArbiter) SetStrategy(conflictType ConflictType, strategy ArbiterStrategy) {
	a.strategyRules[conflictType] = strategy
}

// Arbitrate 根据冲突类型选择策略，生成仲裁决定。
func (a *DefaultArbiter) Arbitrate(conflict Conflict, priorities map[string]TaskPriorityInfo) *ArbiterDecision {
	strategy := a.defaultStrategy
	if s, ok := a.strategyRules[conflict.Type]; ok {
		strategy = s
	}

	winners, losers := a.selectWinner(conflict.TaskIDs, priorities)
	actions := a.generateActions(strategy, winners, losers)

	a.seq++
	return &ArbiterDecision{
		DecisionID:  fmt.Sprintf("dec-%d", a.seq),
		ConflictID:  conflict.ConflictID,
		Strategy:    strategy,
		WinnerTasks: winners,
		LoserTasks:  losers,
		Reason:      fmt.Sprintf("conflict %s on %q resolved via %s", conflict.Type, conflict.ResourcePath, strategy),
		Actions:     actions,
		DecidedAt:   time.Now(),
	}
}

// ArbitrateAll 遍历报告中的所有冲突，逐一仲裁。
func (a *DefaultArbiter) ArbitrateAll(report *ConflictReport, priorities map[string]TaskPriorityInfo) []*ArbiterDecision {
	if report == nil {
		return nil
	}
	decisions := make([]*ArbiterDecision, 0, len(report.Conflicts))
	for _, c := range report.Conflicts {
		decisions = append(decisions, a.Arbitrate(c, priorities))
	}
	return decisions
}

// ResolveDeadlock 选择优先级最低的任务作为 victim 中止，打破死锁环。
func (a *DefaultArbiter) ResolveDeadlock(cycle DeadlockCycle, priorities map[string]TaskPriorityInfo) *ArbiterDecision {
	if len(cycle.TaskIDs) == 0 {
		return nil
	}

	// 选择优先级最低（数值最大）的非关键任务作为 victim
	victim := a.pickVictim(cycle.TaskIDs, priorities)

	var winners []string
	for _, tid := range cycle.TaskIDs {
		if tid != victim {
			winners = append(winners, tid)
		}
	}

	actions := []ArbiterAction{
		{
			ActionType:  "abort",
			TaskID:      victim,
			Description: fmt.Sprintf("abort task %s to break deadlock cycle %s", victim, cycle.CycleID),
		},
	}
	for _, w := range winners {
		actions = append(actions, ArbiterAction{
			ActionType:  "retry",
			TaskID:      w,
			Description: fmt.Sprintf("retry task %s after deadlock victim %s is aborted", w, victim),
		})
	}

	a.seq++
	return &ArbiterDecision{
		DecisionID:  fmt.Sprintf("dec-%d", a.seq),
		ConflictID:  cycle.CycleID,
		Strategy:    StrategyAbort,
		WinnerTasks: winners,
		LoserTasks:  []string{victim},
		Reason:      fmt.Sprintf("deadlock resolved by aborting lowest-priority task %s", victim),
		Actions:     actions,
		DecidedAt:   time.Now(),
	}
}

// ─── 内部方法 ─────────────────────────────────────────────────────

// selectWinner 按优先级将任务分为 winner 和 loser。
// 优先级规则：critical > 已开始(StartedAt != nil) > Priority 数值小者优先。
// 第一个为 winner，其余为 loser。
func (a *DefaultArbiter) selectWinner(taskIDs []string, priorities map[string]TaskPriorityInfo) (winners, losers []string) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	if len(taskIDs) == 1 {
		return taskIDs, nil
	}

	// 找到最高优先级任务的索引
	bestIdx := 0
	for i := 1; i < len(taskIDs); i++ {
		if a.higherPriority(taskIDs[i], taskIDs[bestIdx], priorities) {
			bestIdx = i
		}
	}

	for i, tid := range taskIDs {
		if i == bestIdx {
			winners = append(winners, tid)
		} else {
			losers = append(losers, tid)
		}
	}
	return winners, losers
}

// higherPriority 判断 a 是否比 b 优先级更高。
func (a *DefaultArbiter) higherPriority(tidA, tidB string, priorities map[string]TaskPriorityInfo) bool {
	infoA, okA := priorities[tidA]
	infoB, okB := priorities[tidB]

	// 无优先级信息的任务优先级最低
	if !okA {
		return false
	}
	if !okB {
		return true
	}

	// 关键路径任务优先
	if infoA.Critical && !infoB.Critical {
		return true
	}
	if !infoA.Critical && infoB.Critical {
		return false
	}

	// 已开始的任务优先
	aStarted := infoA.StartedAt != nil
	bStarted := infoB.StartedAt != nil
	if aStarted && !bStarted {
		return true
	}
	if !aStarted && bStarted {
		return false
	}

	// Priority 数值越小优先级越高
	return infoA.Priority < infoB.Priority
}

// generateActions 根据策略为 winner 和 loser 生成具体动作。
func (a *DefaultArbiter) generateActions(strategy ArbiterStrategy, winners, losers []string) []ArbiterAction {
	var actions []ArbiterAction

	switch strategy {
	case StrategySerialize:
		for _, tid := range losers {
			actions = append(actions, ArbiterAction{
				ActionType:  "wait",
				TaskID:      tid,
				Description: fmt.Sprintf("task %s waits for serialized execution", tid),
				Delay:       5 * time.Second,
			})
		}
	case StrategyPriority:
		for _, tid := range losers {
			actions = append(actions, ArbiterAction{
				ActionType:  "requeue",
				TaskID:      tid,
				Description: fmt.Sprintf("task %s requeued due to lower priority", tid),
			})
		}
	case StrategyMerge:
		for _, tid := range losers {
			actions = append(actions, ArbiterAction{
				ActionType:  "merge",
				TaskID:      tid,
				Description: fmt.Sprintf("task %s merged into winner task", tid),
			})
		}
	case StrategyPartition:
		for _, tid := range losers {
			actions = append(actions, ArbiterAction{
				ActionType:  "wait",
				TaskID:      tid,
				Description: fmt.Sprintf("task %s assigned to separate partition", tid),
				Delay:       2 * time.Second,
			})
		}
	case StrategyAbort:
		for _, tid := range losers {
			actions = append(actions, ArbiterAction{
				ActionType:  "abort",
				TaskID:      tid,
				Description: fmt.Sprintf("task %s aborted due to resource conflict", tid),
			})
		}
	}

	return actions
}

// pickVictim 从死锁环中选择优先级最低的非关键任务作为 victim。
func (a *DefaultArbiter) pickVictim(taskIDs []string, priorities map[string]TaskPriorityInfo) string {
	victim := taskIDs[0]
	for i := 1; i < len(taskIDs); i++ {
		// 如果当前 victim 比 taskIDs[i] 优先级高，则换 victim
		if a.higherPriority(victim, taskIDs[i], priorities) {
			victim = taskIDs[i]
		}
	}
	return victim
}
