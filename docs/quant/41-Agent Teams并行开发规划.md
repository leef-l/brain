# Agent Teams 并行开发规划

> 工作流拆分 — 写入边界 · 依赖顺序 · 合并节奏 · 并行开发护栏

**上级文档**:
- [35-量化系统三脑架构总览](35-量化系统三脑架构总览.md)
- [40-核心逻辑与开发护栏](40-核心逻辑与开发护栏.md)

**配套文档**:
- [42-Agent Teams开发Backlog](42-Agent Teams开发Backlog.md) — P0/P1/P2 任务拆分、里程碑、验收条件

---

## 一、文档目的

这份文档回答的不是“架构怎么设计”，而是：

**在当前仓库里，怎么把量化系统拆成多个 agent team 并行开发，同时尽量不互相阻塞、也不把接口面改乱。**

本文默认遵守 Doc 40 的约束：
- 主链路顺序不能改
- 状态机不能各写各的
- 共享对象不能边写边换语义
- 公开 tool / instruction 只能由固定 owner 维护

## 二、拆分原则

并行开发时，按“写入边界”拆，不按“功能想法”拆。

核心原则：
- 同一个共享对象只能有一个 team 负责定版
- 同一个目录尽量只给一个 team 主写
- 快路径和慢路径分 lane，避免互相拖慢
- 未完成的上游能力，用 fixture / stub 顶住，不等待联调
- 除 Team A 外，其他 team 不随意改 `docs/quant/35/39/40/41`
- 除 Team A 外，其他 team 不随意改 `kernel` 的 authorizer 和公开 tool 名

## 三、推荐 Team 编制

推荐按 6 个 coding agent team 拆分，再指定 1 个集成 owner。

| Team | 目标 | 主写入范围 | 复用底座 | 主要产出 | 阻塞关系 |
|------|------|------------|----------|----------|----------|
| Team A | 合同面与骨架 | `docs/quant/35/39/40/41`、`kernel/`、共享对象包 | `protocol/`、`sidecar/`、`tool/` | tool 名、instruction 白名单、共享 DTO、authorizer 规则 | 最先开始，其他 team 先等它定版 |
| Team B | Data 快路径 | `cmd/brain-data/`、`internal/data/` | 共享对象包 | `MarketSnapshot` 产出、ring writer、`data.get_*` | 依赖 Team A |
| Team C | Quant 决策主链路 | `cmd/brain-quant/`、`internal/quant/view`、`internal/quant/router`、`internal/quant/audit`、`internal/strategy/`、`internal/risk/` | 现有 `internal/strategy`、`internal/risk` | `DispatchPlan`、`SignalTrace`、暂停/查询接口 | 依赖 Team A，可先用 fixture snapshot |
| Team D | 执行与恢复 | `internal/quant/execution`、`internal/execution/`、`cmd/exchange-executor/` | 现有 `internal/execution` | paper/live executor、reconcile、账号状态机 | 依赖 Team A 和 Team C |
| Team E | Central 慢路径 | `cmd/brain-central/`、`internal/central/` | 现有 `loop/`、`llm/`、`sidecar/` | `central.review_trade`、`data_alert`、`account_error`、daily review 编排 | 依赖 Team A，可先用 stub request |
| Team F | 持久化与集成测试 | `persistence/`、`test/`、`testing/`、回放夹具、迁移脚本 | 现有 `persistence`、`runtimeaudit` | schema/migration、fixtures、integration tests、replay harness | 依赖 Team A，和 B/C/E 并行联动 |

建议再指定 1 个集成 owner，不单独写主业务，只负责：
- 维护集成分支
- 收敛冲突
- 跑端到端回归
- 卡住未冻结接口变更

## 四、各 Team 详细职责

### 4.1 Team A: 合同面与骨架

这是唯一可以主动改“公共协议”的 team。

主职责：
- 冻结 `stdID`、`MarketSnapshot`、`DispatchPlan`、`ReviewDecision`、`SignalTrace`
- 冻结 `data.*` / `quant.*` / `central.*` 工具名
- 冻结 `brain/execute` instruction 白名单
- 完成 `SpecialistToolCallAuthorizer` 规则
- 补齐 `cmd/brain-data`、`cmd/brain-quant`、`cmd/brain-central` 的最小 sidecar 骨架

禁止事项：
- 不深入实现具体 provider、策略、执行器、LLM 逻辑
- 不在接口未冻结前让其他 team 直接改共享对象定义

完成标志：
- 其他 team 可以只依赖共享 DTO 和 tool contract 开发
- 共享对象不再出现“今天多一个字段，明天换一个含义”的抖动

### 4.2 Team B: Data 快路径

主职责：
- 实现 `internal/data/provider`
- 实现 `internal/data/validator`
- 实现 `internal/data/processor`
- 实现 ring buffer writer 和 `WriteSeq`
- 暴露 `data.get_snapshot` / `data.get_feature_vector` / `data.get_candles` / `data.provider_health`

允许的临时替代：
- provider 可以先接一个交易所
- 向量相似度可以先走 stub，只要接口保留

禁止事项：
- 不触碰账号凭据、私有 WS、下单逻辑
- 不绕过 `DataValidator`
- 不直接为了联调去改 Quant 里的 `MarketView`

完成标志：
- 能稳定产出 `MarketSnapshot`
- `WriteSeq` 单调增长
- 质量异常能带标记进入 degraded 路径

### 4.3 Team C: Quant 决策主链路

主职责：
- 实现 ring consumer / `MarketView`
- 对接 `internal/strategy`
- 对接 `internal/risk`
- 构建 `DispatchPlan`
- 实现 `GlobalRiskGuard`
- 实现 `SignalTrace`
- 暴露 `quant.global_portfolio`、`quant.trace_query`、`quant.pause_trading`、`quant.pause_instrument`

建议复用：
- 直接复用现有 [internal/strategy](/www/wwwroot/project/exchange/codex/brain/internal/strategy)
- 直接复用现有 [internal/risk](/www/wwwroot/project/exchange/codex/brain/internal/risk)

允许的临时替代：
- 在 Team B 未完成前，先用 fixture `MarketSnapshot`
- 在 Team D 未完成前，先接 paper executor stub
- 在 Team E 未完成前，`review_trade` 先返回固定 approve

禁止事项：
- 不直接读数据库替代 ring buffer
- 不把 LLM 调用塞进 Quant
- 不跳过 `DispatchPlan -> GlobalRisk -> review -> GlobalRisk -> ExecutePlan`

完成标志：
- 用 fixture snapshot 就能跑通 paper 决策链
- `SignalTrace` 能覆盖通过、拒绝、缩量、失败、不动作

### 4.4 Team D: 执行与恢复

主职责：
- 实现 paper/live executor
- 实现 `MemoryState`
- 实现 private ws 状态同步
- 实现 reconcile 和 crash recovery
- 实现交易所侧保护单落地

建议复用：
- 直接复用现有 [internal/execution](/www/wwwroot/project/exchange/codex/brain/internal/execution)

允许的临时替代：
- Phase 1 先只做 paper
- live 路径可以先用 fake exchange client 压测

禁止事项：
- 不反向决定策略逻辑
- 不让数据库变成持仓真值来源
- 不跳过账号状态机

完成标志：
- `active / paused / error / recovering` 状态收敛
- 重启后先 reconcile，再恢复开仓

### 4.5 Team E: Central 慢路径

主职责：
- 实现 `central.review_trade`
- 实现 `central.data_alert`
- 实现 `central.account_error`
- 实现 daily review 编排
- 实现 `brain/execute` 内部指令路由

允许的临时替代：
- 先用 deterministic mock LLM
- 先只做 JSON schema 校验和固定审查结果

禁止事项：
- 不直接调执行器
- 不直接改 Quant `MemoryState`
- 不新增未文档化 instruction

完成标志：
- tool 输出稳定 JSON
- 中央脑宕机不会影响快路径

### 4.6 Team F: 持久化与集成测试

主职责：
- 落 `signal_traces`、`daily_reviews`、`account_snapshots` 等 schema
- 管理 migration 和初始化脚本
- 维护 snapshot / review / execution fixtures
- 写端到端集成测试和回放测试
- 把“崩溃恢复、降级、审查缩量”这些路径固化成测试

允许的临时替代：
- `pgvector` 可先在本地测试环境开启，生产接入后补参数优化
- 回放先从 K 线 + snapshot fixture 起步

禁止事项：
- 不擅自改 public DTO 来适配 schema
- 不直接把测试辅助对象变成生产协议

完成标志：
- 关键主链路有自动化回归
- schema 变化有 migration，不靠手工 patch

## 五、共享对象与唯一 Owner

下面这些对象不允许多 team 同时改定义：

| 对象 | Owner | 使用方 |
|------|-------|--------|
| `stdID` | Team A | B/C/D/E/F |
| `MarketSnapshot` | Team A | B/C/F |
| `DispatchPlan` | Team A | C/D/E/F |
| `ReviewDecision` | Team A | C/E/F |
| `SignalTrace` | Team A | C/F/E |
| tool 名与 instruction 名 | Team A | 全部 |
| provider 状态机 | Team B | C/F |
| account 状态机 | Team D | C/E/F |

规则：
- 其他 team 如果想改共享对象，只能提变更申请，由 Team A 改
- 共享对象一旦改动，Team F 必须补 fixture 和回归测试

## 六、并行波次

### 6.1 Wave 0: 先冻结合同面

只允许 Team A 动：
- 共享 DTO
- tool contract
- instruction 白名单
- Kernel authorizer
- 三个 brain 的最小骨架

Wave 0 完成前，不建议启动大规模并行编码。

### 6.2 Wave 1: 四条主线并行

Wave 0 完成后，同时启动：
- Team B: Data 快路径
- Team C: Quant 决策主链路
- Team E: Central 慢路径
- Team F: schema / fixtures / integration harness

这四个 team 的协作方式：
- B 不等 C，先把 snapshot fixture 和 ring writer 做出来
- C 不等 B，先用 fixture snapshot 驱动策略和风控
- E 不等 C，先把 `review_trade` 输出 schema 固定住
- F 不等任何 team，先把 fixtures、测试骨架、migration 框架搭起来

### 6.3 Wave 2: 执行与恢复接入

Team D 在以下条件成立后进入主线：
- `DispatchPlan` 已冻结
- account 状态机已冻结
- Quant paper 主链路可跑

这时做：
- paper executor 真接入
- live executor / private ws / reconcile
- Team C 与 Team D 联调 `ExecutePlan`

### 6.4 Wave 3: 端到端收敛

此阶段不再新增大能力，只做：
- crash recovery 回归
- degraded / paused / recovering 路径回归
- `size_factor` 缩量复核回归
- `central.data_alert` / `central.account_error` 编排回归
- 回放与 paper 对齐

## 七、禁止并行的事项

下面这些事情不要让多个 team 同时推进：
- 同时改 `MarketSnapshot` 字段
- 同时改 `DispatchPlan` 结构
- 同时改 `SignalTrace` schema 和落库逻辑
- 同时改 tool 名、instruction 名、authorizer 规则
- 一边改状态机名字，一边写恢复逻辑

这些项必须单线推进，否则返工成本最高。

## 八、分支与合并策略

建议每个 team 单独 worktree / branch：

```text
feat/quant-contracts
feat/quant-data-fastpath
feat/quant-quant-core
feat/quant-execution-recovery
feat/quant-central-slowpath
feat/quant-persistence-e2e
```

合并顺序建议：
1. `feat/quant-contracts`
2. `feat/quant-data-fastpath` / `feat/quant-quant-core` / `feat/quant-central-slowpath` / `feat/quant-persistence-e2e`
3. `feat/quant-execution-recovery`
4. 集成分支回归后再并主干

合并规则：
- 单个 PR 只解决一个 lane 的主问题
- 跨 lane 改动超过 3 个主目录时，应拆 PR
- 共享 DTO 变更必须先合 Team A，再通知其他 team rebase

## 九、日常协作节奏

建议每天固定两个同步点：

上午同步：
- 是否有共享对象变更
- 哪些 stub 已可替换成真实实现
- 哪些联调改动会影响其他 lane

晚上同步：
- 集成 owner 拉最新分支跑一次端到端
- Team F 更新 fixture 和测试基线
- 未冻结接口一律不允许“先合后改”

## 十、推荐验收顺序

不要按“哪个 team 先写完”验收，要按主链路收口：

1. Team A 验收合同面
2. Team B + Team C 验收 paper 快路径
3. Team E 验收审查和告警入口
4. Team D 验收执行与恢复
5. Team F 验收 schema、fixture、integration tests
6. 最后做跨 team 端到端验收

## 十一、可直接下发给 Agent 的任务模板

### Agent A

你负责合同面与骨架。你的唯一目标是冻结共享对象、tool contract、instruction 白名单和 kernel authorizer。你可以修改 `docs/quant/35/39/40/41`、`kernel/`、共享 DTO 包；不要实现具体业务逻辑，不要改 Data/Quant/Central 的深层业务模块。完成后给出冻结对象清单、tool 清单、instruction 清单和你改过的文件路径。

### Agent B

你负责 Data 快路径。你的写入范围是 `cmd/brain-data/` 和 `internal/data/`。你基于已冻结的 `MarketSnapshot` 和 provider 状态机实现 validator、processor、ring writer 和 `data.get_*`。不要接私有账号接口，不要修改 Quant/Central 模块。完成后给出可用的 snapshot 产出路径、health 路径和你改过的文件路径。

### Agent C

你负责 Quant 决策主链路。你的写入范围是 `cmd/brain-quant/`、`internal/quant/view`、`internal/quant/router`、`internal/quant/audit`、`internal/strategy/`、`internal/risk/`。你可以先使用 fixture snapshot 和 stub review。不要实现 live executor，不要修改 Central。完成后给出 `DispatchPlan`、`GlobalRiskGuard`、`SignalTrace` 的打通情况和你改过的文件路径。

### Agent D

你负责执行与恢复。你的写入范围是 `internal/quant/execution`、`internal/execution/`、`cmd/exchange-executor/`。你实现 account 状态机、paper/live executor、private ws、reconcile 和恢复逻辑。不要改策略和共享 DTO 语义。完成后给出状态机、恢复流程和你改过的文件路径。

### Agent E

你负责 Central 慢路径。你的写入范围是 `cmd/brain-central/` 和 `internal/central/`。你实现 `central.review_trade`、`central.data_alert`、`central.account_error`、daily review 编排和内部 `brain/execute` 指令。不要直接调用执行器，不要修改 Quant 内存状态。完成后给出可用 tool、内部 instruction 和你改过的文件路径。

### Agent F

你负责持久化与集成测试。你的写入范围是 `persistence/`、`test/`、`testing/`、migration、fixtures。你需要把 `SignalTrace`、审查缩量、崩溃恢复和 degraded 路径固化成自动化测试。不要擅自改共享 DTO。完成后给出 migration、fixtures、integration tests 和你改过的文件路径。
