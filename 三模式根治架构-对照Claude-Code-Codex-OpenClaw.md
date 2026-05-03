# 三模式根治架构 — 对照 Claude Code / Codex / OpenClaw

**日期**:2026-05-03
**作者**:Claude(基于用户「不要打补丁,要根治」要求)
**目标**:以 OpenClaw 源码 + Claude Code / Codex 公开行为为参照,定型 brain-v3 的 chat / run / serve 三模式根治方案,**先设计后实施**。

---

## 0. 病灶现场回放

用户场景(贪吃蛇彩蛋版项目,Windows brain chat):
1. 用户输入:`看下做完了没` (6 字)
2. central brain 进入"思考-宣告"循环 turn 1~7,每 turn 都是:
   - 模型输出 600~5000 字纯文本(自言自语 / 计划 / 状态汇总)
   - `tool_use_count=0` 或 `tool_use_count=1` 反复读同一文件
3. nudge 在 turn=6 触发,turn=7 仍 0 工具 + 648 字纯文本,**nudge 失效**
4. 一次 `submit_workflow` 失败(单节点 budget 耗尽),central 没有重新规划能力,退到「再调研」模式
5. 用户多次催「为什么不开始」「干嘛呢」,central 持续宣告

## 1. 病根的 4 层结构(已在前一轮分析)

| 层 | 现象 | 当前修法 |
|----|------|----------|
| 表层 | 模型输出 `stop_reason=""` 长文本独白 | 无 |
| 中层 | nudge 是「提示」不是「中断」 | 注入 reminder,模型可无视 |
| 中层 | LoopDetector 跨 turn 失防(每 turn 新 runID) | runID 维度 Forget,功能丢失 |
| 深层 | 短指令走 IntentSimple → central 自由发挥 → 拼 DAG 失败无人接住 | 单层 LLM 同时当裁判+运动员 |

**根因**:**central brain 在单 LLM context 内同时承担「需求理解 / 状态评估 / 任务拆分 / 调度 / 错误恢复 / 直接执行」6 个角色**,任何一步失败它退回「最简动作」(再读文件),陷入循环。

---

## 2. 对照 Claude Code / Codex / OpenClaw

### 2.1 Claude Code(我自己,公开行为可引用)

- **单 agent 单 context**:没有「central → specialist」的 sidecar 派发,所有工具在同一 context 内调
- **TodoWrite 工具是结构化思考**:不是给用户看,是模型用来「自我承诺 + 跨 turn 状态」
- **subagent 通过 Agent 工具异步派出**:返回**单条 result text** 给主 context,主 agent 不能干预 subagent 内部
- **看门狗**:用户 Ctrl+C 是主要中断手段;模型自身有「我已重复尝试 X 次,改方案」的训练偏置
- **task 不可见的硬约束**:TaskCreate / TaskUpdate 不是 LLM 能直接「绕过」的—— harness 在每 N turn 后注入 task 状态 reminder

**核心观察**:Claude Code 不依赖外部 plan orchestrator,全靠**模型自己 + harness 工具**。harness 工具承担了「结构化认知」职责。

### 2.2 OpenAI Codex(公开行为)

- **codex-cli**:类似 Claude Code 但简化版,纯 single-agent
- **codex 多步任务**:用 system prompt 引导模型先写 todo,再逐项完成
- **没有 plan/execute 分离**:用 system prompt 控制,不用代码层强制
- **关键机制**:`exec` 工具受 sandbox 包裹,失败有结构化错误回 LLM

### 2.3 OpenClaw(已读源码,2.6w 行 TS)

OpenClaw 是**多通道个人助手 Gateway**(WhatsApp/Telegram/Discord 等),不是 coding agent。但它的内部 agent loop 设计极有参考价值。

**核心架构**(对照 brain-v3):

| 概念 | OpenClaw | brain-v3 现状 | 缺什么 |
|------|----------|---------------|--------|
| **Lane(序列化通道)** | `CommandLane = Main / Cron / CronNested / Subagent / Nested` 5 种,每 lane 一个串行队列;subagent 必须用专属 lane,避免和 cron 死锁 | 无概念;run 是直连,chat 是单 goroutine;workflow 用 ExecutionScheduler | **缺 lane 抽象** — chat 和 cron / workflow 工作在同一调用栈,容易互锁 |
| **enqueue 模型** | 所有 LLM 调用走 `enqueueCommandInLane(lane, task, opts)`;subagent spawn 必须 `enqueueGlobal`,session 内调用必须 `enqueueSession` | 直接调 `Runner.Execute`/`Delegate` | **缺队列调度** — 没有「先排队再执行」,中断半个 LLM call 副作用满天飞 |
| **AbortSignal 全链贯穿** | 每个 task 携带 `abortSignal: AbortSignal`,LLM stream / tool call / nested run 都 check;timeout 自己派生新 signal | `context.Context` 部分贯穿,但 ChatHumanCoordinator / nudge / loop detect 都没接 ctx | **缺统一 abort 协议** — 我们各模块各搞各的 cancel |
| **Announce 协议(子 → 父)** | subagent 用 `subagent-announce-output.ts` 把结果**结构化**发回父:`{status: ok/error/timeout, latestAssistantText, toolCallCount, waitingForContinuation}`。父读结构化字段,**不读子的 LLM 文本** | Delegate 返回 `DelegateResult{Output: json.RawMessage}`,父需要解析 LLM 输出找答案 | **缺结构化子结果契约** — central 把 sub LLM 的文本当语料读,容易自我循环 |
| **Idempotency Key** | 每个 announce 有 `v1:<sessionKey>:<runId>` 幂等键;父收到重复 announce 自动去重 | 无 | **缺幂等** — 重连 / 重试容易触发重复 delegate |
| **Compaction 自管理** | `runEmbeddedPiAgent` 内部检测 token 接近限,自动 compact 历史(摘要旧消息);compact 期间 abortSignal 仍能中断 | `Compress` 在 ContextEngine 里,但何时触发由调用方决定 | **缺自动 compaction 触发点** — 现在靠 chat 模式 32K budget 阈值,长 chat 仍能溢 |
| **Approval Classifier** | `acp/approval-classifier.ts` 把工具调用按风险分级(read/write/exec/network),IDE 客户端按级别决定是否 prompt | 我们有 `DefaultSemanticApprover` 5 级,但**没接到 chat REPL 的 approval prompt** | **缺 approval 触达通道** — 5 级分类做了,UI 不展示 |
| **Hooks 多层** | `before_agent_reply` / `agent_end` / `llm_input` / `llm_output` 4 个 hook 点,每个都可中断 | 我们有 hooks 但只在 settings 里配,kernel 内部没 hook 点 | **缺内部 hook** |
| **session-key 路由** | `agentId + sessionKey` 二元组定位 session,所有持久化按这个 key 索引 | runID + brainID;ProjectID 是后加的 | 大致够用 |

**OpenClaw 没有的(我们有的优势)**:
- 没有「central / specialist 多 brain」概念 —— 它是单 agent + 多 channel
- 没有 ExecuteTaskPlan / 拓扑分层 / Welsh-Powell 着色等多任务调度
- 没有 LearningEngine / 因果学习 / 路由权重

**OpenClaw 比我们强的(关键)**:
1. **结构化子结果契约** —— SubagentRunOutcome 含 status/error/timeout 显式字段
2. **lane 队列模型** —— 每个 sessionKey 串行,subagent 走专属 lane
3. **AbortSignal 全链** —— 任何层都能中断,中断是「干净」的(回滚 / 释放)
4. **announce idempotency** —— 防重复触达
5. **自动 compaction** —— 内部决定何时压缩,不靠调用方

### 2.4 三家共性

| 共性 | 含义 |
|------|------|
| **single-agent in single-context** | 一次 LLM call 内只有一个 agent role,不"多角色串场" |
| **subagent 异步通信** | 派出 subagent 后,父和子在不同 context,通过结构化消息通信 |
| **abort 必须能干净停下** | 无论用户主动中断、超时、loop detect,都能让所有正在运行的工具调用、子 agent 干净停下 |
| **结构化错误高于自然语言错误** | tool / subagent 失败必须返回 `{status, error, code}`,而不是「让 LLM 读 stderr」 |
| **harness 强制结构化思考** | TodoWrite / subagent announce / approval 都是工具/事件,不是模型自己写文字 |

---

## 3. brain-v3 的根治架构

不是「打补丁」,而是**全面引入 OpenClaw 的 5 个核心机制**,让 chat/run/serve 三模式都有同一套底盘。

### 3.1 Lane 队列(对应 OpenClaw `CommandLane`)

```go
// sdk/kernel/lane.go (新增)
type LaneKind string
const (
    LaneMain     LaneKind = "main"      // chat REPL 主队列
    LaneSubagent LaneKind = "subagent"  // delegate 出去的子任务
    LaneCron     LaneKind = "cron"      // 后台定时
    LaneWorkflow LaneKind = "workflow"  // serve workflow 调度
)

type LaneRegistry struct {
    lanes map[string]*lane // key = "kind:sessionKey"
}

type lane struct {
    queue chan *task       // 串行队列
    abort context.CancelFunc
    name  string
}

func (lr *LaneRegistry) Enqueue(kind LaneKind, sessionKey string,
    task TaskFunc, opts EnqueueOpts) (<-chan TaskResult, error)
```

**作用**:
- chat session 一个 main lane,user turn 串行,**同一时刻只一个 turn 在跑**
- 用户连发 follow-up,新 task 进 lane 排队,**不并发**
- subagent 走专属 lane,abort 时 main lane 不受影响

**对应 brain-v3 修复**:
- 删除 chat REPL 的 launchRun 并发模型 —— follow-up 必须排队
- ExecutionScheduler 内部用 lane 替代 wg/sync.Map
- workflow node 跑在自己的 workflow lane,与 user turn 隔离

### 3.2 AbortSignal 全链(对应 OpenClaw `params.abortSignal`)

```go
// sdk/kernel/abort.go (新增)
type AbortSignal struct {
    ctx    context.Context
    cancel context.CancelCauseFunc  // Go 1.20+ cancel with cause
    parent *AbortSignal             // 链式
}

func NewAbortSignal(parent context.Context) *AbortSignal
func (s *AbortSignal) Aborted() bool
func (s *AbortSignal) Cause() error  // "user_ctrl_c" / "timeout" / "loop_detected" / "budget_exhausted"
func (s *AbortSignal) Derive(reason string) *AbortSignal  // 派生子信号
func (s *AbortSignal) AbortAll(reason string)              // 触发所有派生
```

**作用**:
- chat REPL Esc → 调 `abort.AbortAll("user_cancel")`
- nudge 拦截二次失败 → 调 `abort.AbortAll("announcement_loop")`
- LoopDetector 命中 → `abort.AbortAll("loop_detected:" + pattern)`
- budget 耗尽 → `abort.AbortAll("budget_exhausted:turns")`

**统一通道**:Runner / Delegate / Tool / ContextEngine 全部 select `<-abort.Done()`,**没有任何"半中断"状态**。

### 3.3 SubagentOutcome 结构化契约(对应 OpenClaw `SubagentRunOutcome`)

```go
// sdk/kernel/subagent_outcome.go (新增)
type SubagentOutcome struct {
    Status            OutcomeStatus      // "ok" | "error" | "timeout" | "aborted" | "loop_detected"
    LatestAssistant   string             // 子 agent 最后一段 assistant 文本(可读)
    ToolCallCount     int                // 子 agent 调了多少工具
    SuccessfulTools   []string           // 哪些工具成功
    FailedTools       []ToolFailure      // 哪些失败 + 原因
    WaitingForFollow  bool               // 是否等用户 follow-up
    ErrorCode         string             // "" | "budget_exhausted" | "loop_detected" | ...
    Hint              string             // "下次试试拆成 2 步" 类的结构化建议
    Usage             SubtaskUsage
}

type OutcomeStatus string
const (
    OutcomeOK              OutcomeStatus = "ok"
    OutcomeError           OutcomeStatus = "error"
    OutcomeTimeout         OutcomeStatus = "timeout"
    OutcomeAborted         OutcomeStatus = "aborted"
    OutcomeLoopDetected    OutcomeStatus = "loop_detected"
    OutcomeBudgetExhausted OutcomeStatus = "budget_exhausted"
)
```

**关键**:**parent 不应该 read sub 的 LLM 文本去猜「成功了吗」**。当前 chat plan 路径让 central 自己读 sub 输出文本,导致 central 把 sub 的"我建议..."当成自己要做的事。

新契约:Delegate 返回 `*SubagentOutcome` 而不是 `*DelegateResult{Output: rawJSON}`。central 只读 `outcome.Status` + `outcome.Hint`,**禁止把 outcome.LatestAssistant 当指令**。

### 3.4 Announce Idempotency(对应 OpenClaw `buildAnnounceIdempotencyKey`)

```go
// sdk/kernel/announce.go (新增)
func BuildAnnounceID(sessionKey, runID string) string {
    return fmt.Sprintf("v1:%s:%s", sessionKey, runID)
}

type AnnounceTracker struct {
    seen sync.Map  // announceID → time.Time
}

func (a *AnnounceTracker) Seen(id string) bool
func (a *AnnounceTracker) Mark(id string)
```

**作用**:防重复 delegate / 重连后重复触达。当前 PlanOrchestrator 对一个 SubTask 失败重试,会发两次 brain.feedback.requested 事件 —— 没有幂等就累积 lessons,假学习。

### 3.5 自动 Compaction(对应 OpenClaw 内部决定时机)

当前:`MessageCompressor` 由调用方传 `TokenBudget=32000` 触发。**问题**:32K 是 LLM 输入上限的"软触发",但 chat session 长跑 50+ turn 后,先达到的不是 token 上限而是 **prompt cache 失效** —— OpenClaw 处理这个用 `cacheTtlState` 跟踪 cache age,接近 5min TTL 就主动 compact rotate 一次,让下次 prompt 重新 hit cache。

```go
// sdk/kernel/auto_compact.go (新增)
type AutoCompactDecider struct {
    TokenSoftLimit int           // 默认 32K(≈输入 50% 时触发)
    TokenHardLimit int           // 默认 96K(≈输入 90% 时强压)
    CacheMaxAge    time.Duration // 默认 4min(早于 5min TTL 一截)
    CompactCooldown time.Duration // 默认 30s,防止 compact 风暴
    lastCompactAt   time.Time
}

func (d *AutoCompactDecider) ShouldCompact(state CompactState) (yes bool, reason string)
```

**调用方**:Runner 每 turn 末调一次 `decider.ShouldCompact()`,yes 就同步 compact,**不让 LLM 知道 compact 发生**(无 user-visible 副作用)。

---

## 4. 三模式如何用这套底盘

### 4.1 chat 模式(交互式)

```
chat.REPL
  └─ LaneRegistry.Enqueue(LaneMain, sessionKey, task)
       └─ task = func() {
            abort := NewAbortSignal(ctx)
            outcome := agentpipe.RunOnLane(abort, input, state)
            // outcome 是 SubagentOutcome,严格结构化
            renderToTerminal(outcome)
          }
```

**关键变化**:
- chat REPL 不再 launchRun 并发,user 输入入 lane 排队
- Esc 直接 `abort.AbortAll("user_cancel")`,lane 当前 task 立即停
- 跨 turn LoopDetector 改成「按 sessionKey 维度」累积,不用 runID
- nudge 后下一 turn 仍 0 工具 → `abort.AbortAll("announcement_loop")` 直接终止本 turn,渲染 SubagentOutcome{Status: aborted, Hint: "我陷入了宣告循环,请重新输入指令或换说法"}

### 4.2 run 模式(单次)

```
run.cmd_run
  └─ LaneRegistry.Enqueue(LaneMain, "run-"+timestamp, task)
       └─ outcome := agentpipe.RunOnLane(...)
       └─ exit code := outcome.Status → ExitCode 映射
            ok                → 0
            aborted (canceled)→ 2
            budget_exhausted  → 3
            loop_detected     → 4 (新)
            其他              → 1
```

### 4.3 serve 模式(HTTP)

```
serve.handleCreateRun
  └─ LaneRegistry.Enqueue(LaneMain, req.SessionKey, task)
       └─ outcome := ...
       └─ writeJSON(outcome)  // 含结构化 status/hint
serve.handleCreateWorkflow
  └─ for each node: LaneRegistry.Enqueue(LaneWorkflow, wfID+":"+nodeID, task)
       └─ 节点间用 streaming edge 通信,不是 LLM 文本
```

---

## 5. agentpipe.RunOnLane 是新核心

替换当前的 `Invocation.Execute` + `PlanRunner.Execute` 双入口,统一为单入口:

```go
// cmd/brain/agentpipe/run_on_lane.go (新增)
func RunOnLane(
    abort *AbortSignal,
    input string,
    state *ChatState,
) (*kernel.SubagentOutcome, error) {

    // 1. Intent 分流(已有,但增强)
    intent := classifier.Classify(input, state.IntentContext())
    //                                  ↑ 新增:已选项目 / 上次失败原因 / 历史模式

    // 2. 三选一执行
    switch intent {
    case IntentSimple:
        return runSimpleOnLane(abort, input, state)
    case IntentProject:
        return runPlanOnLane(abort, input, state)
    case IntentResume:  // 新增意图:恢复上次未完成项目
        return runResumeOnLane(abort, input, state)
    }
}

func runSimpleOnLane(abort, input, state) (*SubagentOutcome, error) {
    // Invocation.Execute,但:
    // - tool registry 按 mode 严格收窄
    // - 每 turn 末调 LoopDetector + AutoCompactDecider
    // - announce-without-action 二次失败 → abort.AbortAll
    // - 返回 SubagentOutcome 而非 RunResult
}

func runPlanOnLane(abort, input, state) (*SubagentOutcome, error) {
    // PlanOrchestrator.ExecuteProject,但:
    // - 每个 SubTask 走 Delegate → 走自己的 LaneSubagent
    // - SubTask 失败带结构化 outcome,PlanOrchestrator 按 outcome.Hint 决定 retry / replan / fail
    // - 整体 abort.AbortAll → 所有正在跑的 SubTask 都收到 abort
    // - 返回汇总 SubagentOutcome
}
```

---

## 6. 新意图:IntentResume

针对用户「看下做完了没」「继续干」这类**已选项目 + 短指令**:

```go
// agentpipe/intent.go 增强
type IntentContext struct {
    HasCurrentProject bool
    LastTurnOutcome   *SubagentOutcome  // 上一 turn 的结果
    ProjectProgress   *kernel.ProjectProgress  // 项目进度
}

func (c *IntentClassifier) Classify(input string, ctx IntentContext) Intent {
    // 已选项目 + 状态查询动词("看下"/"做完了"/"进度"/"完成度"/"目前")→ IntentResume
    if ctx.HasCurrentProject && hasStatusVerb(input) {
        return IntentResume
    }
    // 已选项目 + 继续动词("继续"/"开始"/"下一步"/"接着")→ IntentResume
    if ctx.HasCurrentProject && hasResumeVerb(input) {
        return IntentResume
    }
    // ... 其他规则保持 ...
}
```

`runResumeOnLane`:
1. 读 ProgressStore 拉项目当前进度
2. 找未完成 SubTask
3. **不让 central LLM 自己决定下一步** —— 直接把第一个未完成 SubTask 的 instruction 包成 user prompt,走 PlanRunner 单 SubTask 路径
4. 完成后写入 progress + 返回 outcome

---

## 7. 改动量估算

| 模块 | 新增 | 重构 | 复杂度 |
|------|------|------|--------|
| sdk/kernel/lane.go | 350 | 0 | 中 |
| sdk/kernel/abort.go | 200 | 0 | 中 |
| sdk/kernel/subagent_outcome.go | 150 | 0 | 低 |
| sdk/kernel/announce.go | 80 | 0 | 低 |
| sdk/kernel/auto_compact.go | 200 | 100 (现 ContextEngine.Compress) | 中 |
| sdk/kernel/orchestrator.go (Delegate 改返 outcome) | 0 | 200 | 高(影响 ExecuteTaskPlan / ReviewLoop / bridge.delegate / Workflow 全部下游) |
| sdk/kernel/plan_orchestrator.go (ExecuteProject 改返 outcome + 用 outcome.Hint 重规划) | 0 | 300 | 高 |
| cmd/brain/agentpipe/(rewrite to RunOnLane + Intent 增强) | 400 | 200 (现 Invocation+PlanRunner 收编) | 中 |
| cmd/brain/chat/repl.go(REPL 改 lane 入队) | 50 | 150 | 中 |
| cmd/brain/cmd_run.go / cmd_serve.go(改 lane 入队 + 结构化退出) | 80 | 150 | 低 |
| sdk/loop/runner.go(每 turn 末 hook AutoCompact + nudge 二次失败 abort) | 60 | 50 | 中 |
| **总计** | **~1570 行新增** | **~1150 行重构** | **2 周集中工作** |

风险:
- Delegate 返回值改了,所有 caller 都得改(包括 ReviewLoop / bridge.delegate / orchestrator_plan.go / cmd_serve workflow 路径)
- Lane 化后 chat 的 follow-up 不再并发,**用户体验变化**(连发两条会等第一条结束)—— 这是正确行为,但用户可能短期不适应,需要 UI 提示

---

## 8. 实施路线(分 4 个 PR,各自可独立验收)

### PR-1:基础设施(无破坏)
- sdk/kernel/lane.go(LaneRegistry,但还不强制使用)
- sdk/kernel/abort.go(AbortSignal,与 context.Context 共存)
- sdk/kernel/subagent_outcome.go(类型定义,但 Delegate 暂不返回)
- sdk/kernel/announce.go(IdempotencyTracker)
- sdk/kernel/auto_compact.go(AutoCompactDecider,但 Runner 暂不调)

**验收**:`go build ./...`,所有现有测试通过,新组件有单测覆盖。

### PR-2:agentpipe 重写为 RunOnLane
- agentpipe/run_on_lane.go(新)
- agentpipe/intent.go 增强 IntentResume
- agentpipe/invocation.go / plan_runner.go 改为内部组件,不再被 chat/run/serve 直接调
- chat/run/serve 改为调 `agentpipe.RunOnLane`

**验收**:三模式 6 路径都跑通(chat/run/serve × simple/project),用户场景"看下做完了没"走 IntentResume 而非 IntentSimple。

### PR-3:Delegate 返回 SubagentOutcome
- 改 Orchestrator.Delegate / DelegateBatch 返回 outcome
- 所有 caller 适配(ReviewLoop / bridge / orchestrator_plan / cmd_serve workflow)
- PlanOrchestrator.ExecuteProject 用 outcome.Hint 决定 retry / replan
- 保留旧 DelegateResult API 一段时间作 compatibility shim

**验收**:central 不再读 sub 的 LLM 文本,workflow 失败有结构化 hint 可见。

### PR-4:看门狗硬中断
- nudge 二次失败 → AbortSignal.AbortAll("announcement_loop")
- LoopDetector 跨 turn 累积(按 sessionKey)+ 命中 → AbortAll("loop_detected:" + pattern)
- AutoCompact 自动触发(无 user-visible 副作用)
- chat REPL Esc / Ctrl+C 全部走 AbortSignal

**验收**:用户场景重现 — 6 turn 内必须中断 + 给结构化 hint,不允许超过 8 turn 0 工具循环。

---

## 9. 决策点(用户拍板后才能动)

我需要确认 3 件事:

### Q1:是否接受**chat 串行化**(同一 session follow-up 排队不并发)
- ✅ 接受 → 走 OpenClaw 模型,稳定优先
- ❌ 不接受 → chat 仍并发,但需要 lane-per-turn 隔离机制(更复杂)

### Q2:Delegate 返回 SubagentOutcome 是否值得动「全部下游」
- ✅ 值得 → PR-3 必做,根治"central 读 sub LLM 文本造成自循环"
- ❌ 不值得(改面太大)→ 折衷:DelegateResult 加 `Outcome *SubagentOutcome` 字段,新代码读 Outcome 旧代码读 Output,过渡期共存

### Q3:IntentResume 触发后是否**绕过 central LLM**
- ✅ 绕过(我推荐)→ 短指令 + 已选项目 → 直接看 progress 找未完成 task,不让 central 自由决策,根治"central 拼 workflow 失败陷入循环"
- ❌ 不绕过 → IntentResume 仍调 central,但 system prompt 改成「严格按 progress 推进」,靠 prompt 工程,容易回归

---

## 10. 不做什么(明确的 anti-goals)

- **不引入**新模型路由层(Q1 阶段不需要 Planner/Executor 分离 LLM,先把 lane + outcome 做对再说)
- **不改**MACCS 架构总纲(已声明完成 48/48 的不动)
- **不删**现有 Invocation / PlanRunner —— 收编为 RunOnLane 内部组件
- **不破**SQLite schema(不加新表)
- **不动**workflow streaming edge 协议(已稳定)

---

**附录 A:OpenClaw 关键文件清单(参考用,非搬运)**

| OpenClaw 文件 | 学到了什么 |
|---------------|-----------|
| `src/process/lanes.ts` (5 行) | LaneKind 枚举设计极简 |
| `src/agents/pi-embedded-runner/run.ts` (2649 行) | 单 agent loop 内置 abort + auto compact + retry,**无 maxTurns 概念** |
| `src/agents/subagent-announce-output.ts` (611 行) | SubagentRunOutcome 结构化字段设计 |
| `src/agents/announce-idempotency.ts` (25 行) | `v1:<sessionKey>:<runId>` 幂等键设计 |
| `src/acp/approval-classifier.ts` | 工具风险分级 → IDE 决定 prompt |
| `src/context-engine/types.ts` | AssembleResult / CompactResult / IngestResult 结构化返回 |

**附录 B:Claude Code 行为对照(我自己,可引用公开特性)**

- **TodoWrite 工具是结构化思考承诺** —— harness 在每 N turn 注入 reminder,模型不能"忘记 task"
- **subagent 通过 Agent 工具异步派出**,父 context 拿到的是 single string result,父不能干预子的 turn-by-turn
- **没有 IntentClassifier** —— 完全靠模型自己读用户输入决定动作,但 system prompt 极强(包含 "if user says X, you MUST do Y" 类指令)
- **不依赖 plan orchestrator** —— 全靠模型 + 工具,不用代码层强制

**附录 C:Codex 行为对照**

- 简化版 Claude Code,纯 single-agent
- exec 工具受 sandbox 包裹(对应 brain-v3 的 sandbox + file_policy)
- 无多 brain delegation
