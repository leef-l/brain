# MACCS — 中央大脑智能化编排规范

> **版本**: v2.0.0  
> **日期**: 2026-04-29  
> **依赖**: `MACCS-架构总纲-v2.md`  
> **范围**: Brain SDK 编排层 + EasyMVP 调度层

---

## 1. 编排不是委派，是战略指挥

### 1.1 旧模式 vs 新模式

**旧模式（当前）**：
```
Central: "你去写个文件" → Code: 写好返回 → Central: "再验证一下" → Verifier: 验证返回
  ↑                                                                  │
  └──────────────────────────────────────────────────────────────────┘
问题：Central 只是传话筒，没有全局视野，不会根据反馈调整策略
```

**新模式（目标）**：
```
Central: 分析任务 → 制定计划 → 同时启动 Code + Verifier（Verifier 待命）
  │
  ▼
Code: 执行中……（每轮汇报进度）
  │
  ▼
Central: 发现 Code 进度 80%，但测试覆盖率不足 → 动态追加测试任务
  │
  ▼
Code: 完成 → Central: 激活 Verifier → Verifier: 审核报告（3 个问题）
  │
  ▼
Central: 分析问题 → 2 个给 Code 修复，1 个自己改配置 → 重新调度
  │
  ▼
（循环直到通过）
```

### 1.2 编排器的核心能力

| 能力 | 说明 | 当前状态 | 目标 |
|------|------|----------|------|
| 任务分解 | 将大任务拆分为可并行子任务 | ❌ 无 | ✅ 结构化 TaskPlan |
| 能力匹配 | 根据任务特征选择最佳 brain | ⚠️ 简单 capMatcher | ✅ 学习增强匹配 |
| 依赖分析 | 构建 DAG，识别并行机会 | ✅ Kahn 算法 | ✅ 动态调整 DAG |
| 进度感知 | 实时追踪所有并发任务进度 | ❌ 无 | ✅ ProjectProgress |
| 动态预算 | 根据复杂度分配 turn/token | ❌ 固定 20/50 | ✅ 智能预估 |
| 中断重排 | 任务中途调整方案 | ❌ 不支持 | ✅ InterruptSignal |
| 冲突仲裁 | 解决并发任务冲突 | ❌ 无 | ✅ 资源锁 + 仲裁 |
| 质量闭环 | 审核-修正-确认自动化 | ❌ 单次审核 | ✅ 循环直到通过 |

---

## 2. TaskPlan — 结构化任务规划

### 2.1 数据结构

```go
package kernel

import (
    "time"
    "github.com/leef-l/brain/sdk/agent"
)

// TaskPlan 是中央大脑制定的执行计划
type TaskPlan struct {
    PlanID      string    `json:"plan_id"`
    Version     int       `json:"version"`      // 版本号，每次调整+1
    ProjectID   string    `json:"project_id"`
    Goal        string    `json:"goal"`         // 总体目标描述
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
    
    SubTasks    []SubTask         `json:"sub_tasks"`
    Dependencies map[string][]string `json:"dependencies"` // task_id -> 依赖的 task_ids
    ParallelLayers [][]string     `json:"parallel_layers"` // 拓扑分层
    
    Budget      PlanBudget        `json:"budget"`
    Checkpoints []Checkpoint      `json:"checkpoints"`
    
    // 中断控制
    Interrupt   *InterruptSignal  `json:"interrupt,omitempty"`
    
    // 学习数据
    Estimation  ComplexityEstimation `json:"estimation"`
}

// SubTask 单个子任务
type SubTask struct {
    TaskID           string        `json:"task_id"`
    Name             string        `json:"name"`
    Kind             agent.Kind    `json:"kind"`              // 目标大脑类型
    Instruction      string        `json:"instruction"`       // 给大脑的指令
    EstimatedTurns   int           `json:"estimated_turns"`   // 预估 turn 数
    EstimatedTokens  int           `json:"estimated_tokens"`  // 预估 token 数
    VerificationCriteria []string  `json:"verification_criteria"` // 验收标准
    
    // 执行控制
    Status           TaskStatus    `json:"status"`
    RunID            string        `json:"run_id,omitempty"`  // 关联的 brain run
    StartedAt        *time.Time    `json:"started_at,omitempty"`
    CompletedAt      *time.Time    `json:"completed_at,omitempty"`
    
    // 结果
    Result           *TaskResult   `json:"result,omitempty"`
    
    // 重试策略
    RetryPolicy      RetryPolicy   `json:"retry_policy"`
    RetryCount       int           `json:"retry_count"`
}

type TaskStatus string
const (
    TaskPending    TaskStatus = "pending"
    TaskRunning    TaskStatus = "running"
    TaskCompleted  TaskStatus = "completed"
    TaskFailed     TaskStatus = "failed"
    TaskBlocked    TaskStatus = "blocked"
    TaskCancelled  TaskStatus = "cancelled"
    TaskInterrupted TaskStatus = "interrupted"
)

type TaskResult struct {
    Output      string   `json:"output"`       // 文本输出
    Artifacts   []string `json:"artifacts"`    // 产出物列表（文件路径等）
    Confidence  float64  `json:"confidence"`   // 完成度置信度 0-1
    Issues      []Issue  `json:"issues"`       // 发现的问题
}

type Issue struct {
    Severity    string `json:"severity"`    // critical/warning/info
    Category    string `json:"category"`    // code/security/performance/style
    Description string `json:"description"`
    SuggestedFix string `json:"suggested_fix,omitempty"`
}

// PlanBudget 计划级预算
type PlanBudget struct {
    TotalTurns      int     `json:"total_turns"`       // 总 turn 预算
    TotalTokens     int     `json:"total_tokens"`      // 总 token 预算
    TotalCostUSD    float64 `json:"total_cost_usd"`    // 总成本预算
    
    // 动态调整记录
    Adjustments     []BudgetAdjustment `json:"adjustments"`
}

type BudgetAdjustment struct {
    At        time.Time `json:"at"`
    Reason    string    `json:"reason"`
    TurnDelta int       `json:"turn_delta"`
}

// Checkpoint 关键检查点
type Checkpoint struct {
    Name      string   `json:"name"`
    Condition string   `json:"condition"` // 到达条件描述
    Required  bool     `json:"required"`  // 是否必须完成
    Passed    bool     `json:"passed"`
}

// ComplexityEstimation 复杂度预估（基于学习数据）
type ComplexityEstimation struct {
    Source          string  `json:"source"`          // "learning" / "heuristic" / "llm"
    Confidence      float64 `json:"confidence"`      // 预估置信度
    HistoricalAvg   float64 `json:"historical_avg"`  // 历史平均 turn 数
    HistoricalStd   float64 `json:"historical_std"`  // 历史标准差
}
```

### 2.2 任务分解策略

**分解原则**：

1. **单一职责**: 每个子任务只做一件事
2. **可验证性**: 每个子任务有明确的验收标准
3. **独立性**: 尽量减少子任务间的依赖
4. **粒度适中**: 不太大（避免一个任务耗尽预算），不太小（避免调度开销）

**分解模板（按项目类型）**：

```
Web 应用项目：
  1. 数据库设计（Data/Architect）
  2. 后端 API 实现（Code）
  3. 前端页面实现（Code）
  4. 端到端测试（Browser）
  5. 安全审核（Verifier）
  6. 性能测试（Verifier）

算法/数据处理项目：
  1. 数据获取与清洗（Data）
  2. 算法实现（Code）
  3. 单元测试（Code）
  4. 结果验证（Verifier）

交易系统项目：
  1. 策略开发（Quant）
  2. 回测验证（Quant）
  3. 风控配置（Quant）
  4. 实盘监控（Data）
```

---

## 3. ProjectProgress — 全局进度追踪

### 3.1 数据结构

```go
// ProjectProgress 项目级进度
type ProjectProgress struct {
    ProjectID       string         `json:"project_id"`
    UpdatedAt       time.Time      `json:"updated_at"`
    
    // 总体进度
    OverallPercent  float64        `json:"overall_percent"`  // 0-100
    Phase           ProjectPhase   `json:"phase"`
    
    // 活跃运行
    ActiveRuns      []RunProgress  `json:"active_runs"`
    
    // 已完成
    CompletedTasks  []TaskSummary  `json:"completed_tasks"`
    
    // 被阻塞
    BlockedTasks    []BlockedInfo  `json:"blocked_tasks"`
    
    // 质量门
    QualityGates    []QualityGate  `json:"quality_gates"`
    
    // 资源使用
    ResourceUsage   ResourceUsage  `json:"resource_usage"`
}

type ProjectPhase string
const (
    PhaseAnalyzing   ProjectPhase = "analyzing"    // 需求分析
    PhaseDesigning   ProjectPhase = "designing"    // 方案设计
    PhaseReviewing   ProjectPhase = "reviewing"    // 方案审核
    PhaseExecuting   ProjectPhase = "executing"    // 任务执行
    PhaseAccepting   ProjectPhase = "accepting"    // 验收测试
    PhaseDelivered   ProjectPhase = "delivered"    // 已交付
    PhaseReworking   ProjectPhase = "reworking"    // 返工中
)

type RunProgress struct {
    RunID        string      `json:"run_id"`
    TaskID       string      `json:"task_id"`
    TaskName     string      `json:"task_name"`
    BrainKind    agent.Kind  `json:"brain_kind"`
    
    Status       string      `json:"status"`
    CurrentTurn  int         `json:"current_turn"`
    MaxTurns     int         `json:"max_turns"`
    TurnUsage    float64     `json:"turn_usage"`     // 0-1
    
    // 进度摘要（由专精大脑汇报）
    LastSummary  string      `json:"last_summary"`
    Confidence   float64     `json:"confidence"`     // 完成度 0-1
    
    // 时间
    StartedAt    time.Time   `json:"started_at"`
    EstimatedEnd *time.Time  `json:"estimated_end,omitempty"`
}

type BlockedInfo struct {
    TaskID       string   `json:"task_id"`
    Reason       string   `json:"reason"`
    BlockedBy    []string `json:"blocked_by"`     // 被哪些任务阻塞
    Since        time.Time `json:"since"`
}

type QualityGate struct {
    Name      string `json:"name"`
    Required  bool   `json:"required"`
    Status    string `json:"status"`      // pending/passed/failed
    Details   string `json:"details"`
}

type ResourceUsage struct {
    TotalTurnsUsed   int     `json:"total_turns_used"`
    TotalTokensUsed  int     `json:"total_tokens_used"`
    TotalCostUSD     float64 `json:"total_cost_usd"`
    ActiveBrains     int     `json:"active_brains"`
}
```

### 3.2 进度汇报机制

**专精大脑 → 中央大脑**：

每个专精大脑在执行过程中，通过 `brain/progress` RPC 定期汇报进度：

```json
{
  "task_id": "task_001",
  "run_id": "run_abc123",
  "progress": {
    "percent": 65,
    "current_action": "writing unit tests",
    "completed_items": ["setup", "core_logic", "error_handling"],
    "remaining_items": ["unit_tests", "documentation"],
    "confidence": 0.8,
    "issues_found": []
  }
}
```

**中央大脑聚合**：

```go
func (p *ProjectProgress) UpdateFromRun(run RunProgress) {
    // 更新活跃运行
    p.ActiveRuns = updateOrAppend(p.ActiveRuns, run)
    
    // 重新计算总体进度
    p.OverallPercent = p.calculateOverallPercent()
    
    // 检查是否有任务完成
    if run.Status == "completed" {
        p.moveToCompleted(run)
    }
    
    // 检查是否有阻塞解除
    p.checkUnblocked()
    
    p.UpdatedAt = time.Now().UTC()
}
```

---

## 4. InterruptSignal — 中断与重排

### 4.1 设计哲学

**不是强制 kill，而是优雅暂停**。

就像交响乐团演奏中指挥家举起手——乐手不会立刻扔掉乐器，而是完成当前小节，然后停下。

### 4.2 信号类型

```go
type InterruptType string

const (
    // 方案变更：计划有调整，需要按新方案执行
    InterruptPlanChanged      InterruptType = "plan_changed"
    
    // 紧急停止：出现严重错误，立即停止所有任务
    InterruptEmergencyStop    InterruptType = "emergency_stop"
    
    // 优先级覆盖：新任务优先级更高，暂停当前任务
    InterruptPriorityOverride InterruptType = "priority_override"
    
    // 依赖变化：前置任务结果变了，当前任务需要重新评估
    InterruptDependencyChange InterruptType = "dependency_change"
    
    // 预算耗尽：当前预算不够，需要重新分配
    InterruptBudgetExhausted  InterruptType = "budget_exhausted"
)

type InterruptSignal struct {
    SignalID      string        `json:"signal_id"`
    Type          InterruptType `json:"type"`
    
    // 影响范围
    AffectedTasks []string      `json:"affected_tasks"` // 空表示全部
    
    // 新计划（PlanChanged 时必填）
    NewPlan       *TaskPlan     `json:"new_plan,omitempty"`
    
    // 暂停还是终止
    Action        InterruptAction `json:"action"`
    
    Reason        string        `json:"reason"`
    IssuedAt      time.Time     `json:"issued_at"`
    IssuedBy      string        `json:"issued_by"` // "central" / "user" / "system"
}

type InterruptAction string
const (
    ActionPause    InterruptAction = "pause"    // 暂停，保存状态，可恢复
    ActionStop     InterruptAction = "stop"     // 停止，保存 checkpoint，不恢复
    ActionRestart  InterruptAction = "restart"  // 重启，从 checkpoint 或从头开始
)
```

### 4.3 执行流程

```
1. 触发中断
   Central 发现问题 → 创建 InterruptSignal → 写入共享状态
   
2. 传播中断
   Runner 在每一 turn 开始前检查中断信号
   
3. 响应中断
   if 当前任务在 AffectedTasks 中:
     - 保存当前 checkpoint
     - 优雅停止当前 turn
     - 返回中断响应（含已保存的 checkpoint ID）
   
4. 重新调度
   Central 收集所有响应 → 基于 NewPlan 重新创建 runs
   - 如果是 ActionRestart: 尝试从 checkpoint 恢复，否则从头开始
   - 如果是 ActionPause: 等待后续恢复指令
   
5. 恢复执行
   新 runs 启动 → 如有 checkpoint 则恢复 → 继续执行
```

### 4.4 中断检查点

```go
// 在 runner 的每一 turn 开始前检查
func (r *Runner) checkInterrupt(ctx context.Context, run *Run) *InterruptSignal {
    if r.InterruptChecker == nil {
        return nil
    }
    sig := r.InterruptChecker.Check(run.ID)
    if sig != nil {
        // 记录中断
        run.InterruptedBy = sig.SignalID
        run.InterruptReason = sig.Reason
    }
    return sig
}
```

---

## 5. 动态预算分配

### 5.1 不是固定预算，是流动预算

**旧模式**：每个任务固定 50 turns，用完了就失败。

**新模式**：总预算池，按实际需要动态分配。

```go
type DynamicBudgetPool struct {
    TotalTurns    int     // 项目总 turn 预算
    RemainingTurns int    // 剩余
    
    // 分配记录
    Allocations   map[string]int // task_id -> allocated turns
    
    // 学习增强
    Estimator     *ComplexityEstimator
}

func (p *DynamicBudgetPool) Allocate(task SubTask) int {
    // 1. 基于学习数据预估
    est := p.Estimator.Estimate(task)
    
    // 2. 加安全边际
    allocated := int(float64(est) * 1.5)
    
    // 3. 检查剩余预算
    if allocated > p.RemainingTurns {
        allocated = p.RemainingTurns
    }
    
    // 4. 分配
    p.Allocations[task.TaskID] = allocated
    p.RemainingTurns -= allocated
    
    return allocated
}

func (p *DynamicBudgetPool) Reclaim(taskID string, unused int) {
    // 任务完成后，回收未用预算
    p.RemainingTurns += unused
    delete(p.Allocations, taskID)
}

func (p *DynamicBudgetPool) EmergencyAllocate(reason string, minTurns int) int {
    // 紧急分配：即使预算紧张也要保证关键任务
    if p.RemainingTurns >= minTurns {
        p.RemainingTurns -= minTurns
        return minTurns
    }
    // 预算不足时，从非关键任务回收
    return p.ReclaimFromNonCritical(minTurns)
}
```

### 5.2 复杂度预估器

```go
type ComplexityEstimator struct {
    learner *LearningEngine
}

func (e *ComplexityEstimator) Estimate(task SubTask) int {
    // 1. 查学习数据
    if profile := e.learner.Profiles()[task.Kind]; profile != nil {
        if historical := profile.AverageTurnsFor(task.Instruction); historical > 0 {
            return historical
        }
    }
    
    // 2. 启发式估算
    base := 10
    
    // 根据指令特征调整
    if strings.Contains(task.Instruction, "implement") || strings.Contains(task.Instruction, "实现") {
        base += 15
    }
    if strings.Contains(task.Instruction, "refactor") || strings.Contains(task.Instruction, "重构") {
        base += 10
    }
    if strings.Contains(task.Instruction, "test") || strings.Contains(task.Instruction, "测试") {
        base += 8
    }
    
    // 根据历史文件数调整
    if len(task.VerificationCriteria) > 3 {
        base += 5 * (len(task.VerificationCriteria) - 3)
    }
    
    return base
}
```

---

## 6. 质量闭环 — 审核-修正-确认

### 6.1 自动化审核循环

```
┌─────────────────────────────────────────────────────────────┐
│                      Review Loop                             │
│                                                              │
│   ┌──────────┐    ┌──────────┐    ┌──────────┐             │
│   │ 提交审核  │───►│ 等待结果  │───►│ 检查结果  │             │
│   └──────────┘    └──────────┘    └────┬─────┘             │
│                                         │                  │
│                              ┌──────────┴──────────┐       │
│                              │                     │       │
│                              ▼                     ▼       │
│                         [通过]                 [不通过]     │
│                            │                       │       │
│                            ▼                       ▼       │
│                      流程继续               生成修复任务     │
│                                                │           │
│                                                ▼           │
│                                          执行修复          │
│                                                │           │
│                                                └────► 重新审核 │
│                                                              │
│   最大迭代次数: 5                                              │
│   收敛条件: 问题数减少 80% 或问题数 <= 2                       │
└─────────────────────────────────────────────────────────────┘
```

### 6.2 Verifier 审核报告格式

```go
type ReviewReport struct {
    ReviewID    string       `json:"review_id"`
    TaskID      string       `json:"task_id"`
    Passed      bool         `json:"passed"`
    Score       float64      `json:"score"`       // 0-100
    
    Issues      []ReviewIssue `json:"issues"`
    
    // 通过标准
    Criteria    []Criterion   `json:"criteria"`
    
    // 建议
    Suggestions []string      `json:"suggestions"`
}

type ReviewIssue struct {
    ID          string `json:"id"`
    Severity    string `json:"severity"`    // blocker/critical/warning/info
    Category    string `json:"category"`    // security/performance/style/bug/design
    File        string `json:"file,omitempty"`
    Line        int    `json:"line,omitempty"`
    Title       string `json:"title"`
    Description string `json:"description"`
    SuggestedFix string `json:"suggested_fix,omitempty"`
    
    // 自动修复可能性
    AutoFixable bool   `json:"auto_fixable"`
}

type Criterion struct {
    Name    string `json:"name"`
    Weight  int    `json:"weight"`
    Passed  bool   `json:"passed"`
    Score   float64 `json:"score"`
}
```

### 6.3 修复任务生成

```go
func GenerateFixTasks(report ReviewReport, original SubTask) []SubTask {
    var fixes []SubTask
    
    for _, issue := range report.Issues {
        if issue.Severity == "blocker" || issue.Severity == "critical" {
            // 必须修复
            fixes = append(fixes, SubTask{
                Name:        fmt.Sprintf("Fix %s: %s", issue.ID, issue.Title),
                Kind:        original.Kind, // 通常由原 brain 修复
                Instruction: fmt.Sprintf("Fix the following issue in the code you just wrote:\n\n%s: %s\n\nSuggested fix:\n%s", 
                    issue.Severity, issue.Description, issue.SuggestedFix),
                VerificationCriteria: []string{
                    fmt.Sprintf("Issue %s is resolved", issue.ID),
                },
            })
        }
    }
    
    return fixes
}
```

---

## 7. 并发控制与冲突仲裁

### 7.1 资源冲突检测

```go
type ResourceConflict struct {
    TaskA    string   `json:"task_a"`
    TaskB    string   `json:"task_b"`
    Resource string   `json:"resource"`    // 冲突资源（文件路径、端口等）
    Type     string   `json:"type"`        // read_write / write_write
}

func DetectConflicts(tasks []SubTask) []ResourceConflict {
    var conflicts []ResourceConflict
    
    // 检测文件冲突
    fileAccess := make(map[string][]string) // path -> []task_id
    for _, task := range tasks {
        for _, artifact := range task.Result.Artifacts {
            fileAccess[artifact] = append(fileAccess[artifact], task.TaskID)
        }
    }
    
    for path, taskIDs := range fileAccess {
        if len(taskIDs) > 1 {
            // 多个任务访问同一文件
            for i := 0; i < len(taskIDs); i++ {
                for j := i + 1; j < len(taskIDs); j++ {
                    conflicts = append(conflicts, ResourceConflict{
                        TaskA:    taskIDs[i],
                        TaskB:    taskIDs[j],
                        Resource: path,
                        Type:     "write_write",
                    })
                }
            }
        }
    }
    
    return conflicts
}
```

### 7.2 仲裁策略

```go
func ArbitrateConflicts(conflicts []ResourceConflict, plan *TaskPlan) *TaskPlan {
    for _, conflict := range conflicts {
        // 策略 1: 如果两个任务在同一并行层，串行化
        layerA := findLayer(plan, conflict.TaskA)
        layerB := findLayer(plan, conflict.TaskB)
        
        if layerA == layerB {
            // 添加依赖：TaskA 先执行，TaskB 后执行
            plan.Dependencies[conflict.TaskB] = append(
                plan.Dependencies[conflict.TaskB], 
                conflict.TaskA,
            )
        }
        
        // 策略 2: 如果 TaskB 优先级更高，交换顺序
        taskA := findTask(plan, conflict.TaskA)
        taskB := findTask(plan, conflict.TaskB)
        if taskB.Priority > taskA.Priority {
            swapOrder(plan, conflict.TaskA, conflict.TaskB)
        }
    }
    
    // 重新计算拓扑分层
    plan.ParallelLayers = computeTopologicalLayers(plan.Dependencies)
    
    return plan
}
```

---

## 8. 与 EasyMVP 的集成

### 8.1 EasyMVP 调用 Brain 的新模式

**旧模式**（当前）：
```go
// EasyMVP 为每个 domain task 创建一个独立的 central brain run
// 每个 run 独立执行，互不感知
```

**新模式**（目标）：
```go
// EasyMVP 创建一个项目级的 Central Brain Session
// Central Brain 负责整个项目的编排

// 1. EasyMVP 发送项目需求
project := easymvp.CreateProject(request)

// 2. Central Brain 制定完整计划
plan := centralBrain.PlanProject(project)

// 3. Central Brain 开始执行，自行调度各专精大脑
// EasyMVP 只需监听进度事件

events.Subscribe(project.ID, func(evt ProjectEvent) {
    switch evt.Type {
    case "phase_changed":
        updateProjectPhase(project.ID, evt.Phase)
    case "task_completed":
        updateTaskStatus(evt.TaskID, "completed")
    case "review_failed":
        // 审核失败，进入修正循环
        startReviewLoop(evt.TaskID, evt.Issues)
    case "interrupt":
        // 方案调整，通知用户
        notifyUserPlanChanged(evt.Reason)
    }
})
```

### 8.2 EasyMVP 项目状态机

```go
type EasyMVPProject struct {
    ID          string
    Name        string
    Status      EasyMVPStatus
    
    // Brain 关联
    CentralSessionID string
    CurrentPlan      *kernel.TaskPlan
    Progress         *kernel.ProjectProgress
    
    // 审核闭环
    ReviewHistory    []ReviewCycle
}

type EasyMVPStatus string
const (
    EasyMVPPending      EasyMVPStatus = "pending"
    EasyMVPAnalyzing    EasyMVPStatus = "analyzing"
    EasyMVPDesigning    EasyMVPStatus = "designing"
    EasyMVPReviewing    EasyMVPStatus = "reviewing"      // 方案审核中
    EasyMVPExecuting    EasyMVPStatus = "executing"
    EasyMVPAccepting    EasyMVPStatus = "accepting"      // 验收测试中
    EasyMVPDelivered    EasyMVPStatus = "delivered"
    EasyMVPReworking    EasyMVPStatus = "reworking"
    EasyMVPBlocked      EasyMVPStatus = "blocked"
)

type ReviewCycle struct {
    CycleNumber int
    PlanVersion int
    Reviewer    string        // verifier / user
    Passed      bool
    Issues      []kernel.Issue
    FixedAt     *time.Time
}
```

---

## 9. 生产级要求

### 9.1 可观测性

```go
// 每个编排决策都必须可追踪
type OrchestrationDecision struct {
    DecisionID  string    `json:"decision_id"`
    Timestamp   time.Time `json:"timestamp"`
    Type        string    `json:"type"`      // delegate / interrupt / budget_adjust / conflict_arbitrate
    Input       string    `json:"input"`     // 决策输入（状态摘要）
    Output      string    `json:"output"`    // 决策输出
    Reasoning   string    `json:"reasoning"` // 决策理由（LLM 或规则生成）
    Confidence  float64   `json:"confidence"`
}
```

### 9.2 容错设计

| 故障场景 | 处理策略 |
|----------|----------|
| 专精大脑崩溃 | 自动重启 + 从 checkpoint 恢复 |
| LLM API 不可用 | 切换到备用 provider / 降级到本地模型 |
| 任务超时 | 分析原因（预算不足/指令不清）→ 调整重试 |
| 无限循环 | LoopDetector 检测 → 强制终止 → 人工介入 |
| 预算耗尽 | 紧急预算分配 / 请求用户确认追加 |
| 并发冲突 | 冲突仲裁 → 串行化 / 合并 |

### 9.3 性能目标

| 指标 | 目标 |
|------|------|
| 任务分解延迟 | < 5s |
| 进度同步延迟 | < 1s |
| 中断响应时间 | < 2 turns |
| 并发任务数 | ≥ 8 个 |
| 审核闭环收敛 | ≤ 3 轮 |

---

*本文档定义了 MACCS 中央大脑的智能化编排规范。所有实现必须遵循本文档。*
