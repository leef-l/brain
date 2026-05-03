# Replan 总体路线 — 基于现有持久化与 EventBus 的渐进式实施

**日期**:2026-05-03
**作者**:Claude(基于用户「不打补丁要根治 + 不重复造已有」要求)
**前置文档**:`三模式根治架构-对照Claude-Code-Codex-OpenClaw.md`(对照分析)
**本文档定位**:**实施路线书** — 通读了 brain-v3 全部相关代码后,以最小改动量串通既有设施实现「Stop-the-World Replan」能力。

---

## 0. 目标场景(用户拍板的)

```
chat 中正在跑一个项目级任务(N 个 sub agent 并发),用户随时输入 ──┐
                                                              ↓
                          关联性判断
                          ├─ 无关(闲聊/问别的) → 当前任务继续,正常回复
                          └─ 有关(改/补/质疑/追加) ─→ STOP 所有 sub
                                                       ↓
                                                  收集快照
                                                       ↓
                                                  Replan(复用现有 Parser+Designer)
                                                       ↓
                                                  启动新 plan

子 agent 反馈/错误也走同一通道:
sub: "我做不到 X" / "出错了 Y"  ─→ EventBus → 同上判定 → 局部 retry / STOP+REPLAN
```

**关键约束**(不可让步):
1. 不打补丁,要根治 — chat / run / serve 三模式同一套底盘
2. 不重复造已有 — brain-v3 的持久化 / 学习 / 事件总线 / 项目记忆都已就绪
3. 用户输入「看下做完了没」「继续干」「改成 X」等指令必须能正确触发 replan,**不能继续陷入"宣告循环"**

---

## 1. 现有设施盘点(通读结论)

### 1.1 可直接复用,不动一行代码

| 设施 | 位置 | 给 Replan 的角色 |
|------|------|-----------------|
| `MemoryEntry{Type: MemoryDecision}` | `sdk/kernel/project_memory.go:29` | Replan 决策记录("用户中途要求改 X")直接 `Memory.Store` |
| `Checkpoint` turn 级 CAS | `sdk/persistence/run_checkpoint.go` | sub 上次做到哪的精确数据,**不必让 sub 单独上报** |
| `RunStore.Events` | `sdk/persistence/run_store.go:32` | 完整 lifecycle 事件查询 |
| `LearningEngine.RankBrains` | `sdk/kernel/learning.go:732` | Replan LLM 决定 rerun 时选哪个 brain |
| `EventBus` + 慢消费者丢事件 | `sdk/events/mem_bus.go` | 事件总线,环形 10K 缓冲,Subscribe 支持 executionID 过滤 |
| `RunHandle.Cancel context.CancelFunc` | `cmd/brain/chat/state.go:103` | chat 已有 abort 机制 |
| `RequirementParser` + `DesignGenerator` | `cmd/brain/agentpipe/plan_runner.go:62` | **现成的 plan 生成 LLM 角色**,Replan 直接复用 |
| ctx → rpc.Call → sidecar.RunAgentLoopFull 全链 | `sdk/kernel/orchestrator.go:1178` + `sdk/shared/thin_brain.go:216` | abort 链路完整,sub LLM stream 会响应 cancel |

### 1.2 已声明但未启用的状态(直接拿来用)

| 状态 | 位置 | 启用方式 |
|------|------|---------|
| `PlanPaused` PlanStatus | `task_plan.go:20` | Replan 暂停期间 plan.Status = PlanPaused |
| `PlanTaskInterrupted` PlanTaskStatus | `task_plan.go:35` | sub 被 abort 时 SubTask.Status = PlanTaskInterrupted |
| `PlanTaskCancelled` | `task_plan.go:34` | 用户主动取消整个 task |
| `TaskPlan.Version int` | `task_plan.go:42` | **已有字段,目前只在 NewTaskPlan 时设 1**;Replan 时自增 |

### 1.3 现有架构里的两个洞(必须修补)

| 洞 | 位置 | 影响 |
|----|------|-----|
| `DelegateBatch.wg.Wait()` 不响应 ctx cancel | `orchestrator.go:765` | abort 触发后要等本层最慢任务自然结束,非"立即停" |
| chat REPL launchRun **永远新启 goroutine** 不阻塞 | `repl.go:519-526, 768` | 用户 follow-up 期间产生并发 goroutine 同时改 state.Messages,这是死循环根源 |

---

## 2. 真正需要新增的 8 个组件

完整覆盖目标场景的最小集。每个都给落地点 + 行数 + 验收要点。

### 2.1 RelevanceClassifier(全新,~150 行)

判断 user input 与当前正在跑的 plan / sub feedback 与当前 plan 的**关联性**。

**输入**:
- input 文本(用户字符串 / sub 反馈结构化字段)
- ctx:`{currentPlan: *TaskPlan, runningTasks: []TaskID, lastAssistant: string}`

**输出**:`Relevance` 枚举:
- `Unrelated` — 不停任务,正常处理(闲聊 / 不相关问题)
- `StatusQuery` — 不停任务,但插入"当前进度报告"作为回复("做完了吗"/"进度怎样")
- `Modification` — STOP + REPLAN("改成 X" / "也加 Y" / "等下先做 Z")
- `Cancel` — STOP 不 REPLAN("取消" / "算了")
- `Refine` — 不停,补充指令到当前 sub("再快一点" / "代码风格用 X")

**判定方法**(从弱到强,组合):
1. **Cancel 关键词直通**:命中"取消/停止/cancel/abort/算了"立即 Cancel
2. **StatusQuery 关键词直通**:"做完了吗 / 进度 / 看一下 / 状态 / status"
3. **Modification 关键词触发**:"改成 / 换 / 不要 / 改用 / 替换 / 也加 / 再加 / 改为 / 修改 / 调整 / 等下先 / change to / replace"
4. **疑问句保护**(优先级最高):问号或"什么是 / 为什么 / 怎么 / why / what / how"等 → 永远 `Unrelated`(不停)
5. **LLM 兜底**:关键词都没命中且 input 非空,调一次轻量 LLM(haiku 级)判定 + 输出 JSON

**LLM 兜底 prompt 模板**(直接给设计,不是想法):
```
你是任务关联性分类器。当前正在执行项目:
- 项目目标: {plan.Goal}
- 正在跑: {running tasks 简介}
- 已完成: {completed tasks 名字}

用户刚说: "{input}"

判断该输入与当前任务的关系。只输出 JSON,不解释:
{"relevance": "unrelated"|"status_query"|"modification"|"cancel"|"refine", "confidence": 0.0-1.0, "rationale": "<1 句>"}
```

**保守化**:`confidence < 0.7` 一律降级为 `Unrelated`(宁可漏判不误停)。

**落地点**:`sdk/kernel/relevance_classifier.go`(新文件)。

**验收**:单测覆盖 30 条样本(每个枚举 6 条),正确率 ≥ 90%。

---

### 2.2 ReplanRequest 数据结构 + EventBus 事件类型(新,~50 行)

```go
// sdk/kernel/replan_types.go (新)
type ReplanTrigger string
const (
    TriggerUserModification ReplanTrigger = "user_modification"
    TriggerSubFailure       ReplanTrigger = "sub_failure"
    TriggerSubFeedback      ReplanTrigger = "sub_feedback"
)

type ReplanRequest struct {
    PlanID    string         `json:"plan_id"`
    ProjectID string         `json:"project_id"`
    Trigger   ReplanTrigger  `json:"trigger"`
    UserInput string         `json:"user_input,omitempty"`  // user_modification 时
    SubTask   string         `json:"sub_task,omitempty"`    // sub_* 时
    SubError  string         `json:"sub_error,omitempty"`
    Hint      string         `json:"hint,omitempty"`        // sub 提供的 "我建议拆成 2 步"
    At        time.Time      `json:"at"`
}
```

EventBus 新事件类型:
- `replan.requested` — chat REPL / sub 发布
- `replan.started` — PlanOrchestrator 开始 replan 时发布
- `replan.completed` — replan 完成发布
- `replan.aborted` — replan 失败发布(供 chat UI 渲染)

**落地点**:`sdk/kernel/replan_types.go`(新文件)。

---

### 2.3 PlanOrchestrator.ExecuteProject 改造为 ReplanLoop(改造,~250 行)

**当前主路径**(plan_orchestrator.go:216-363):
```
ExecuteProject(plan):
  ├─ 1. 创建 progress
  ├─ 2. 估算预算 / ModelRouter
  ├─ 3. ExecuteTaskPlan(阻塞)
  ├─ 4. 反思 / 写 lesson
  └─ 5. 异步 PatternExtraction
  return result
```

**改造后**:
```
ExecuteProject(plan):
  parentCtx := ctx
  for plan.Status != Completed && plan.Status != Failed {
      // 派生可中断 ctx
      runCtx, cancel := context.WithCancel(parentCtx)
      
      // 旁路 goroutine 监听 replan.requested 事件
      replanCh := make(chan *ReplanRequest, 1)
      go subscribeReplanRequests(runCtx, plan.ProjectID, replanCh)
      
      // 启动 ExecuteTaskPlan(已有,内部已支持 ctx.Err() check)
      done := make(chan execDone, 1)
      go func() {
          result, err := po.Orchestrator.ExecuteTaskPlan(runCtx, plan, progress, reporter)
          done <- execDone{result, err}
      }()
      
      // 等执行完成 OR 收到 replan
      select {
      case d := <-done:
          // 正常结束
          cancel()
          return assembleFinalResult(d, progress)
          
      case req := <-replanCh:
          // 收到 replan,触发 abort
          cancel()
          <-done  // 等 ExecuteTaskPlan 干净退出(ctx-aware)
          
          // 标记 plan 状态
          plan.Status = PlanPaused
          
          // 收集快照(从 progress + 各 SubTask.Status + Checkpoint)
          snapshot := snapshotState(plan, progress, po.runStore)
          
          // 调 ReplanLLM(复用 RequirementParser + DesignGenerator,prompt 多带 snapshot + req)
          newPlan, err := po.replan(parentCtx, plan, snapshot, req)
          if err != nil {
              po.publishReplanAborted(plan.ProjectID, err)
              return nil, fmt.Errorf("replan failed: %w", err)
          }
          
          // 接续:newPlan 的 SubTasks 可能含"已完成"标记(从 snapshot.completedTasks 复用)
          plan = newPlan
          plan.Version++
          plan.Status = PlanActive
          po.publishReplanCompleted(plan)
          // 进入下一轮 for(用 newPlan 跑)
      }
  }
```

**关键设计**:
1. **ctx 派生**:每轮 for 派生一次 runCtx,这样 cancel() 不影响 parentCtx,replan 完成后再派生新 runCtx
2. **subscribeReplanRequests**:用 EventBus.Subscribe(executionID=plan.ProjectID),只接收本项目相关事件
3. **snapshot 含哪些数据**:
   - 已完成 SubTask 列表(从 progress.CompletedTasks)
   - 正在跑(被 abort 的)SubTask 列表 + 各自最近一个 Checkpoint(messages_ref 的摘要 + 工具调用计数 + partial files 路径,如果有)
   - 用户修改文本(req.UserInput)/ sub 错误(req.SubError + req.Hint)
   - 项目记忆 top-5(po.MemoryRetriever.Retrieve)
4. **replan() 内部**:
   - 不重新调 Parser → ProjectSpec(已有 spec 复用)
   - 直接调 Designer.GenerateWithModification(spec, snapshot, req) — **新增方法**(见 2.4)
   - 调 Designer.ToTaskPlan(newProposal)
5. **新 plan 接续策略**:
   - 已完成的 SubTask **保留**(防止重做)— newPlan 中标记 `Status=PlanTaskCompleted` + 复用原 Result
   - 被 abort 的 SubTask **重新 schedule**(可能换 brain / 换 instruction)
   - 全新加入的 SubTask 走正常 schedule

**落地点**:`sdk/kernel/plan_orchestrator.go`(改 ExecuteProject 函数 + 新增 replan / snapshotState / publishReplan*)。

---

### 2.4 DesignGenerator.GenerateWithModification(扩展现有,~80 行)

`DesignGenerator` 是已有的设计生成 LLM 角色,目前只有 `Generate(spec) → DesignProposal`。新增:

```go
// sdk/kernel/design_generator.go (扩展现有文件)
type GenerateWithModificationInput struct {
    OriginalSpec   *ProjectSpec
    OriginalPlan   *TaskPlan
    Snapshot       *ReplanSnapshot
    UserMod        string  // 可选,user 修改文本
    SubError       string  // 可选,sub 错误
    SubHint        string  // 可选,sub 建议
    MemoryHints    []string  // 项目记忆 top-N 摘要
}

func (d *DefaultDesignGenerator) GenerateWithModification(
    ctx context.Context,
    in GenerateWithModificationInput,
) (*DesignProposal, error)
```

**实现核心**:prompt 模板(直接给):
```
你是软件项目重规划者。原计划遇到问题,需要基于当前状态生成新方案。

【原始目标】
{spec.Goal}

【已完成的子任务】(保持不动,新方案中标记 completed):
{snapshot.completedTasks 每条一行: name + brief output 摘要}

【原方案中正在做被中断的子任务】:
{snapshot.interruptedTasks 每条一行: name + 被打断时的 progress + partial files}

【触发原因】
{if UserMod}用户中途要求: "{UserMod}"{end}
{if SubError}子任务"{snapshot.interruptedTasks[i]}"出错: {SubError}{end}
{if SubHint}子任务建议: {SubHint}{end}

【项目记忆中的相关历史】
{MemoryHints 每条一行}

请输出 DesignProposal JSON,要求:
1. 不重做已完成任务
2. 必要时调整被中断任务的 instruction(尤其是用户修改触发的)
3. 可以新增任务,可以删除原计划但未启动的任务
4. 在 DesignProposal.Rationale 字段说明改动理由

只输出 JSON,不解释。
```

**落地点**:`sdk/kernel/design_generator.go`(扩展)。如果当前没这个文件,放在 `requirement_parser.go` 同目录新建。

---

### 2.5 chat REPL: launchRun 串行化 + UserInputClassifier 路由(改造,~150 行)

**当前问题**(repl.go:519-526, 768):
```go
// 用户输入直接 launchRun,无关联性检查
launchRun(input)
```

**改造**:
```go
// 用户输入分诊:
//  1. 当前没有 running run → 直接 launchRun(走原路径)
//  2. 有 running run + state.CurrentProject != nil(项目模式)→ 调 RelevanceClassifier
//      ├─ Unrelated → 提示 "正在跑 run-X,稍后回复",入队列
//      ├─ StatusQuery → 即时打印 progress 摘要(从 ProjectProgress 读),不打断
//      ├─ Modification → 发 replan.requested 事件(EventBus),由 PlanOrchestrator 接管
//      ├─ Cancel → 调 state.CancelAllRuns,不 replan
//      └─ Refine → 发 brain.feedback.requested 事件(已有通道),sub 内部决定是否吸收
//  3. 有 running run + 无项目模式(纯 simple)→ 提示用户 Esc 取消后再输入(或入队列)
func dispatchUserInput(state *State, input string, eventBus events.EventBus, classifier *RelevanceClassifier) {
    if !state.AnyRunning() {
        launchRun(input)
        return
    }
    
    if state.IsNoProject || state.CurrentProject == nil {
        // 简单模式不支持 replan,提示用户
        renderHint("正在执行,新输入已加入队列。Esc 可取消当前任务。")
        state.Enqueue(input)
        return
    }
    
    // 项目模式:走分诊
    rel := classifier.Classify(ctx, input, state.CurrentRelevanceContext())
    switch rel.Kind {
    case Unrelated:
        renderHint("...")
        state.Enqueue(input)
    case StatusQuery:
        renderProgressSummary(state.CurrentProject.ID, progressStore)
    case Modification:
        eventBus.Publish(ctx, events.Event{
            Type:        "replan.requested",
            ExecutionID: state.CurrentProject.ID,
            Data:        marshalReplanRequest(input, TriggerUserModification),
        })
        renderHint("已收到修改请求,正在重新规划...")
    case Cancel:
        state.CancelAllRuns()
        renderHint("已取消所有任务。")
    case Refine:
        eventBus.Publish(ctx, events.Event{
            Type:        "brain.feedback.requested",
            ExecutionID: state.CurrentProject.ID,
            Data:        marshalRefineHint(input),
        })
        renderHint("已发送补充指令给 sub。")
    }
}
```

**关键 UI 行为**:
- StatusQuery 即时打印**不进对话历史**(避免污染 LLM context)
- Modification / Cancel **进入对话历史并持久化到 ProjectStore**(用户决策必须记录)
- Refine 不进对话历史(只是临时补充指令)

**落地点**:`cmd/brain/chat/repl.go` 改 launchRun 调用点 + 新增 dispatchUserInput;`cmd/brain/chat/state.go` 加 `Enqueue` / `CurrentRelevanceContext()` / `IsNoProject` 已有。

---

### 2.6 DelegateBatch wg.Wait 改成 ctx-aware(改造,~30 行)

当前(orchestrator.go:746-765):
```go
for i, req := range batch.Requests {
    wg.Add(1)
    go func(...) { defer wg.Done(); ... }(...)
}
wg.Wait()  // 不响应 ctx
```

改成:
```go
done := make(chan struct{})
go func() { wg.Wait(); close(done) }()
select {
case <-done:
    // 正常完成
case <-ctx.Done():
    // ctx cancel,但仍要等所有 goroutine 自然 join,避免泄漏
    // 子 goroutine 内的 Delegate 已收到 ctx done,会快速失败
    <-done
}
```

**落地点**:`sdk/kernel/orchestrator.go` DelegateBatch 函数。

**验收**:ctx cancel 后 DelegateBatch 100ms 内返回(此前是等最慢任务结束)。

---

### 2.7 PartialFiles 字段 + Checkpoint flush 触发(改造,~30 行)

**问题**:sub 被 abort 时半个文件留在磁盘上,Replan 不知道。

**改法**:
```go
// task_plan.go PlanSubTask 加字段
type PlanSubTask struct {
    ...
    // PartialFiles 中断时记录已写入但未完成的文件路径,Replan 据此决定保留/丢弃。
    PartialFiles []string `json:"partial_files,omitempty"`
    // AbortReason 中断原因,Replan LLM 看这个调整新方案。
    AbortReason string `json:"abort_reason,omitempty"`
}
```

**写入时机**:`ExecuteTaskPlan` 在层间 ctx.Err() check 时,把当前 running tasks 的状态(从 RunStore.Events 倒序找最近 tool.start 含 fs_write)写入 SubTask.PartialFiles。

**落地点**:`sdk/kernel/task_plan.go`(字段)+ `sdk/kernel/orchestrator_plan.go`(写入)。

---

### 2.8 ProgressView 在 chat REPL 实时渲染(扩展现有 todo 框,~150 行)

chat 已有 todo 框 / activity 概念(repl.go LiveReporter)。扩展:

- 项目模式下 chat REPL 顶部固定一行渲染 ProjectProgress 摘要:
  ```
  📊 项目: 贪吃蛇彩蛋版  阶段: executing  完成度: 40%
     ✓ 设计 schema (data/3s)
     ⟳ 写后端 API (code/15s) [████░░] 40%
     ⟳ 写前端 UI (code/8s)  [██░░░░] 20%
     ○ 跑测试 (verifier/pending)
  ```
- 订阅 EventBus 的 `task.state.completed` / `replan.completed` 事件刷新
- StatusQuery 触发时打印这块的扩展版

**落地点**:`cmd/brain/chat/progress_view.go`(新)+ `repl.go` 在 RenderPromptFrame 里嵌入。

---

## 3. 三阶段实施(总计 ~890 行 + ~310 行重构)

### Phase 1: 基础设施(3-4 天,~360 行)
- [ ] 2.1 RelevanceClassifier(`sdk/kernel/relevance_classifier.go` 新)
- [ ] 2.2 ReplanRequest + 事件类型(`sdk/kernel/replan_types.go` 新)
- [ ] 2.4 DesignGenerator.GenerateWithModification(扩展)
- [ ] 2.6 DelegateBatch ctx-aware
- [ ] 2.7 PartialFiles 字段

**验收**:
- `go build ./...` 通过
- RelevanceClassifier 单测 30 条样本 ≥ 90% 准确
- DelegateBatch ctx cancel 后 100ms 内返回(基准测试)
- 不接入 chat REPL,不改 ExecuteProject

### Phase 2: PlanOrchestrator ReplanLoop(4-5 天,~330 行)
- [ ] 2.3 ExecuteProject 改造为可中断 + 重启循环
- [ ] snapshotState / replan / publishReplan* 内部方法
- [ ] **不接 chat REPL** — 仅 PlanOrchestrator 内部跑通

**验收**:
- 写一个集成测试:模拟 Plan 跑到一半,EventBus.Publish replan.requested,验证:
  - ExecuteTaskPlan 收到 ctx cancel 干净退出
  - 已完成 SubTask 在新 plan 中保留 Status=Completed
  - 被中断 SubTask 的 PartialFiles 字段被填充
  - 新 plan.Version 自增
  - replan.completed 事件被发布

### Phase 3: chat REPL 接入 + UX(3-4 天,~200 行)
- [ ] 2.5 dispatchUserInput 替换 launchRun 直接调用
- [ ] 2.8 ProgressView 实时渲染
- [ ] cooldown:刚 replan 完 30s 内再来的 modification 入队,合并成一次 replan
- [ ] partial 文件备份到 `.brain/partial/<runID>/`(默认丢)

**验收**:
- 用户场景重现:贪吃蛇彩蛋版项目,chat 跑长任务时:
  - 输入"做完了吗" → 即时 progress 摘要,不打断
  - 输入"改成 SQLite" → STOP + REPLAN + 启动新 plan
  - 输入"今天天气怎样" → 入队列,跑完后回复
  - 输入"取消" → CancelAllRuns + 不 replan
  - 6 turn 内不再出现"宣告循环"

**总计**:**10-13 天 = 2 周左右**

---

## 4. 与现有 MACCS 架构的关系

不破坏 MACCS 48/48 的任何已完成项。具体:

- **MACCS 1.10 ReviewLoop**:保留,Replan 不替代 ReviewLoop。Review 仍在 SubTask 完成时调,产物写 SubTask.Result.Review;Replan 在更上层处理"中途打断 + 重新生成 plan"。
- **MACCS 5.3 active learning + brain.feedback.requested**:**复用同一通道**。sub 主动反馈走 brain.feedback.requested(已有);Replan 多加一个 replan.requested 事件类型,PlanOrchestrator 同时订阅两类。
- **MACCS 5.4 PatternExtractor**:Replan 完成后也写一条 ProjectExperience(Trigger=replan),PatternExtractor 能学到"什么样的 user 修改最常见 → 下次 plan 时预防性多加一层"。
- **MACCS 7 ClosedLoopController**:7 阶段闭环不变,Replan 是 Phase 4(执行)的内部 loop,从 ClosedLoopController 视角它仍是单次 ExecuteProject 调用。
- **Wave 7+ 项目级持久化**:Replan 决策 / partial 文件全部走现有 ProjectMemory + Checkpoint 落库,不需要新表。

---

## 5. 安全边界(防 Replan 过载)

| 边界 | 阈值 | 落地 |
|------|------|------|
| Replan 次数硬上限 | 单次 ExecuteProject 内最多 5 次 | plan_orchestrator.go for 循环计数 |
| Replan cooldown | 30s 内多次 modification 合并 | dispatchUserInput 缓冲 + flush |
| 关联性置信度阈值 | < 0.7 降级 Unrelated | RelevanceClassifier 内部 |
| Replan 失败 fallback | 连续 2 次 replan 失败 → 整个项目 fail,不重试 | plan_orchestrator.go 错误计数 |
| 疑问句强保护 | 含问号或疑问词 → 永远 Unrelated(不走 LLM 兜底) | RelevanceClassifier 短路 |

---

## 6. 不做的事(明确)

- **不引入** LivePlan 数据结构(用 TaskPlan.Version 自增足够)
- **不引入** 新的 Lane / AbortSignal 抽象(用现有 ctx + RunHandle.Cancel 足够)
- **不改** sidecar 协议(brain/execute payload 不动)
- **不让** sub LLM 决策"我要 abort 自己" — abort 永远从 host(PlanOrchestrator)发起
- **不重做** ReviewLoop / Reflection — 它们仍管"事后审核 + 写 lesson",Replan 管"事中改方案"
- **不并发**同 session 多 turn — 项目模式下 user input 走分诊,无关入队列,有关触发 replan
- **不持久化** Replan diff log(plan.Version 自增足够审计;若需详细 diff,后续 PR 加)

---

## 7. 决策点(需要你拍板)

### Q1:RelevanceClassifier 的 LLM 兜底用什么模型?
**已决策**:直接用当前 chat 默认 Provider/Model,**不引入独立配置**。
理由:brain-v3 已支持「不同 brain 不同模型」(`BrainRegistration.Model` + `LLMProxy.ModelForKind`),不需要在 RelevanceClassifier 这一层再开配置项 — 它就是 central brain 的一个内部能力,跟 central 用同一模型即可。

### Q2:Replan 是否影响整个 chat session 的对话历史?
- 选项 A:replan 期间用户的 modification 进 chat history(用户能看到自己说了什么)
- 选项 B:不进 chat history,只进项目记忆 MemoryDecision
- **我建议 A 进 chat history + B 项目记忆**(两边都进)

### Q3:partial 文件默认保留还是丢弃?
- 选项 A:默认保留(让 ReplanLLM 决定怎么处理)
- 选项 B:默认备份到 .brain/partial/ 然后从工作目录删除(干净重启)
- 选项 C:默认保留 + 在 chat UI 里渲染"你有 N 个 partial 文件,是否丢弃?"
- **我建议 B 备份 + 删除** — 否则 LLM 会读到半成品造成困惑

### Q4:Phase 1 / 2 / 3 是否合并成单 PR?
- 选项 A:3 个独立 PR,每个独立验收(更安全,但用户要等 2 周才看到效果)
- 选项 B:合并成 1 个 PR(快但风险大)
- **我建议 A 三个 PR** — Phase 1 对当前用户无可见变化,Phase 2 完全在 PlanOrchestrator 内部,Phase 3 才是用户能看到的 UX 变化。出问题可以停在 Phase 1 或 2

---

## 8. 附录:用户场景预演(贪吃蛇彩蛋版)

### 场景 A:启动时项目已存在,用户问"做完了没"

```
T=0  chat 启动,选项目"贪吃蛇彩蛋版"
     LoadProgress(projectID) 拿到上次 ProjectProgress
     ProgressView 渲染:
       📊 进度: 60%  阶段: executing
       ✓ schema, ✓ 后端, ⟳ 前端 (中断), ○ 测试
T=1  user: "做完了没"
     dispatchUserInput → RelevanceClassifier
       关键词"做完了"命中 → StatusQuery (无需 LLM)
     立即打印 ProgressView 扩展版,不打断
     不入对话历史
T=2  user: "继续"
     RelevanceClassifier: "继续" + 项目模式 → IntentResume(Phase 3 加这个意图)
     PlanOrchestrator.ResumeProject(projectID)
       从 progress 找到 interrupted "前端" + pending "测试"
       不走 Parser/Designer,直接构造 plan = {完成保留, 前端继续, 测试 pending}
       启动 ExecuteTaskPlan
```

### 场景 B:chat 跑长任务期间用户改主意

```
T=0  user: "做一个完整博客系统,前后端 React + Go"
     IntentClassifier → IntentProject
     PlanOrchestrator.ExecuteProject 启动
T=10 ExecuteTaskPlan 跑到 layer 1: schema + 前端骨架并发
T=20 user: "改成 Vue 不要 React"
     dispatchUserInput → RelevanceClassifier
       命中"改成"+"不要" → Modification (高 confidence)
     EventBus.Publish replan.requested
     chat UI 显示 "已收到修改请求,正在重新规划..."
T=20.5 PlanOrchestrator 旁路 goroutine 收到事件
     cancel(runCtx)
     ExecuteTaskPlan 收到 ctx done,层间 break
     正在跑的 SubTask:
       - schema (60%) — Checkpoint 显示已写 server/schema.sql
       - 前端骨架 (40%) — Checkpoint 显示已写 client/src/App.tsx (React 版)
     PartialFiles 写入: ["server/schema.sql", "client/src/App.tsx"]
     Status = PlanTaskInterrupted
T=21 snapshot:
       completed=[]
       interrupted=[schema, 前端骨架]
       userMod="改成 Vue 不要 React"
T=22 ReplanLLM 调 GenerateWithModification:
       输出 newPlan:
         schema 标记 Status=Completed (复用)  
         前端骨架 改 instruction 为 "用 Vue 重写" + Status=Pending
         新增 "迁移 React 代码到 Vue" task
         后端 + 测试 task 不变
       plan.Version 1→2
T=23 plan.Status = PlanActive,进入下一轮 for
     ExecuteTaskPlan 跑新 plan
     chat UI 显示 "重规划完成,继续执行"
T=...  正常完成
```

### 场景 C:闲聊不打断

```
T=0 sub 正在跑后端
T=5 user: "为什么用 Go 不用 Java?"
    dispatchUserInput → RelevanceClassifier
      疑问句 "为什么" 短路 → Unrelated
    不打断,显示 "正在执行,新输入已加入队列"
    state.Enqueue("为什么用 Go 不用 Java?")
T=120 后端跑完
    检查队列,有 queued 输入,launchRun 处理
    LLM 答完
```

---

## 9. 总结

- **不重复造**:复用 ProgressStore / Checkpoint / EventBus / RequirementParser / DesignGenerator / RunHandle.Cancel / MemoryEntry,共 8 个核心设施
- **真新增**:8 个组件 ~890 行 + 改造 ~310 行 = **总 1200 行**
- **工期**:**2 周** = 3 个独立 PR
- **风险可控**:Phase 1 对用户无可见变化,Phase 2 仅 PlanOrchestrator 内部,Phase 3 才是 UX 变化
- **不破** MACCS 48/48 任何一项
- **完整覆盖** 你提的"user 中途修改 / sub 反馈 / sub 错误 → STOP + 分析 + Replan"场景
