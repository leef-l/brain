package kernel

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// DynamicBudgetPool 管理项目级的动态预算分配。
// 总预算在项目开始时设定，按需分配给各子任务，任务完成后回收未用额度。
type DynamicBudgetPool struct {
	mu             sync.Mutex
	totalTurns     int
	remainingTurns int
	allocations    map[string]int // taskID -> allocated turns
	history        []BudgetEvent
}

type BudgetEvent struct {
	At     time.Time `json:"at"`
	TaskID string    `json:"task_id"`
	Action string    `json:"action"` // "allocate"/"reclaim"/"emergency"
	Turns  int       `json:"turns"`
	Reason string    `json:"reason"`
}

func NewDynamicBudgetPool(totalTurns int) *DynamicBudgetPool {
	return &DynamicBudgetPool{
		totalTurns:     totalTurns,
		remainingTurns: totalTurns,
		allocations:    make(map[string]int),
	}
}

// Allocate 为指定任务分配 turn 预算。
// 基于 estimated turns 加安全边际（1.5x），不超过剩余预算。
func (p *DynamicBudgetPool) Allocate(taskID string, estimatedTurns int) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	allocated := int(float64(estimatedTurns) * 1.5)
	if allocated < 10 {
		allocated = 10 // 最低保障
	}
	if allocated > p.remainingTurns {
		allocated = p.remainingTurns
	}

	p.allocations[taskID] = allocated
	p.remainingTurns -= allocated
	p.history = append(p.history, BudgetEvent{
		At:     time.Now().UTC(),
		TaskID: taskID,
		Action: "allocate",
		Turns:  allocated,
		Reason: fmt.Sprintf("estimated %d, allocated %d (1.5x + floor 10)", estimatedTurns, allocated),
	})

	return allocated
}

// Reclaim 回收任务完成后的未用预算。
func (p *DynamicBudgetPool) Reclaim(taskID string, usedTurns int) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	allocated, ok := p.allocations[taskID]
	if !ok {
		return 0
	}
	unused := allocated - usedTurns
	if unused < 0 {
		unused = 0
	}
	p.remainingTurns += unused
	delete(p.allocations, taskID)
	p.history = append(p.history, BudgetEvent{
		At:     time.Now().UTC(),
		TaskID: taskID,
		Action: "reclaim",
		Turns:  unused,
		Reason: fmt.Sprintf("used %d of %d, reclaimed %d", usedTurns, allocated, unused),
	})

	return unused
}

// EmergencyAllocate 紧急分配：即使预算紧张也要保证关键任务。
func (p *DynamicBudgetPool) EmergencyAllocate(taskID string, minTurns int, reason string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	allocated := minTurns
	if allocated > p.remainingTurns {
		allocated = p.remainingTurns
	}
	if allocated <= 0 {
		return 0
	}

	p.allocations[taskID] = p.allocations[taskID] + allocated
	p.remainingTurns -= allocated
	p.history = append(p.history, BudgetEvent{
		At:     time.Now().UTC(),
		TaskID: taskID,
		Action: "emergency",
		Turns:  allocated,
		Reason: reason,
	})

	return allocated
}

// Remaining 返回剩余预算。
func (p *DynamicBudgetPool) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remainingTurns
}

// Total 返回总预算。
func (p *DynamicBudgetPool) Total() int {
	return p.totalTurns
}

// AllocationFor 返回指定任务的当前分配。
func (p *DynamicBudgetPool) AllocationFor(taskID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allocations[taskID]
}

// History 返回预算事件历史。
func (p *DynamicBudgetPool) History() []BudgetEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]BudgetEvent, len(p.history))
	copy(out, p.history)
	return out
}

// AllocateForPlan 为整个 TaskPlan 分配预算。
// 遍历所有 pending 子任务，按 EstimatedTurns 分配。
func (p *DynamicBudgetPool) AllocateForPlan(plan *TaskPlan) {
	for _, task := range plan.SubTasks {
		if task.Status == PlanTaskPending {
			est := task.EstimatedTurns
			if est <= 0 {
				est = estimateDefault(task.Instruction)
			}
			p.Allocate(task.TaskID, est)
		}
	}
}

// estimateDefault 基于指令文本的启发式估算。
func estimateDefault(instruction string) int {
	base := 10
	lower := strings.ToLower(instruction)
	if strings.Contains(lower, "implement") || strings.Contains(lower, "实现") {
		base += 15
	}
	if strings.Contains(lower, "refactor") || strings.Contains(lower, "重构") {
		base += 10
	}
	if strings.Contains(lower, "test") || strings.Contains(lower, "测试") {
		base += 8
	}
	if strings.Contains(lower, "review") || strings.Contains(lower, "审核") {
		base += 5
	}
	return base
}

// ToSubtaskBudget 将当前分配转为 SubtaskBudget。
func (p *DynamicBudgetPool) ToSubtaskBudget(taskID string) *SubtaskBudget {
	turns := p.AllocationFor(taskID)
	if turns <= 0 {
		return nil
	}
	return &SubtaskBudget{
		MaxTurns: turns,
	}
}
