# Agent Teams 开发 Backlog

> 可执行任务单 — P0 / P1 / P2 · 依赖 · 验收 · 交付物

**上级文档**:
- [40-核心逻辑与开发护栏](40-核心逻辑与开发护栏.md)
- [41-Agent Teams并行开发规划](41-Agent Teams并行开发规划.md)

---

## 一、文档目的

这份文档把 Doc 41 的 team 拆分，进一步落成**可直接派工的开发 backlog**。

使用方式：
- `P0` = 不做就无法进入下一波并行
- `P1` = 主链路落地必须完成
- `P2` = 不阻塞主链路，但应在 live 前补齐

## 当前状态快照（2026-04-14）

已落到代码并通过回归：

- release bundle / `health-check.sh` / `brain doctor` / `MANIFEST.SHA256SUMS` gate 已可重复执行，且 bundle 模式不再错误回退到源码树
- Quant sidecar 已默认接 file-backed trace 持久化，并可在重启后恢复 `pause_trading`、`pause_instrument` 与最近一次 recovery 摘要
- `MemResumeCoordinator.Resume()` 已拒绝终态 checkpoint，和 `CanResume()` 保持一致
- Central `daily_review_run` 已透传调用方 `ctx`，`emergency_action` / `update_config` 已从假 ack 改为真实状态变更
- Central sidecar 已默认接 file-backed `CentralStateStore`，可在重启后恢复 control/review 状态
- Data sidecar 已默认接 file-backed `DataStateStore`，可在重启后恢复最近 snapshot、validator 去重上下文与最近 provider health 摘要
- Data service 已具备 `DrainProviders()` 骨架，`brain-data` 可通过静态 fixture provider 在启动时灌入事件并写入 ring

当前仍是生产阻塞的 open item：

- live execution / real exchange lifecycle 仍未闭环
- Data provider registry 生命周期与真实 ingest 主链仍未接入；当前只有静态 fixture bootstrap 和 provider drain 骨架，不是 live feed attach

交付规则：
- 每个任务都要有明确落位目录
- 每个任务都要有最小验收条件
- 每个任务都要注明依赖 team

## 二、里程碑

| 里程碑 | 目标 | 必要完成项 |
|--------|------|------------|
| `M0` | 合同面冻结 | A-P0 全部 |
| `M1` | Paper 快路径闭环 | B-P0/P1、C-P0/P1、F-P0 |
| `M2` | 慢路径闭环 | E-P0/P1、F-P1 |
| `M3` | 恢复与执行闭环 | D-P0/P1、C-P2、F-P2 |
| `M4` | Live Ready | B/C/D/E/F 的 P2 收口 |

里程碑顺序不能反过来：
- 没有 `M0`，不要大规模并行编码
- 没有 `M1`，不要推进复杂 LLM 审查和 live 执行
- 没有 `M3`，不要宣称系统可实盘

## 三、建议代码落位

为避免并行开发时目录乱长，先固定建议落位：

```text
cmd/
  brain-data/
  brain-quant/
  brain-central/

internal/
  quantcontracts/        # 共享 DTO / schema / tool contract 常量
  data/
    provider/
    validator/
    processor/
    ring/
    service/
  quant/
    view/
    router/
    risk/
    audit/
    execution/
    recovery/
    service/
  central/
    review/
    control/
    reviewrun/
```

约束：
- 共享对象优先放 `internal/quantcontracts`
- sidecar 对外入口放 `cmd/brain-*`
- 对外 `tools/call` 收口放各自 `service/`
- 不把跨 brain 的公共对象分散到 `data/quant/central` 各自目录

## 四、Team A Backlog: 合同面与骨架

### A-P0-01 冻结共享 DTO

- 目标: 固定 `stdID`、`MarketSnapshot`、`DispatchPlan`、`ReviewDecision`、`SignalTrace`
- 落位: `internal/quantcontracts/`
- 依赖: 无
- 交付物:
  - DTO struct
  - 字段注释
  - 版本说明
- 验收:
  - Doc 40/41/42 与 DTO 字段一致
  - 其他 team 不再自定义同名结构

### A-P0-02 冻结 tool 与 instruction 常量

- 目标: 固定 `data.*` / `quant.*` / `central.*` 与 `brain/execute` 白名单
- 落位: `internal/quantcontracts/tools.go`、`internal/quantcontracts/instructions.go`
- 依赖: 无
- 交付物:
  - tool name 常量
  - instruction 常量
  - 参数/返回结构
- 验收:
  - `review_trade` 不再以 instruction 形式出现
  - 无“同名 tool + instruction”双入口

### A-P0-03 注册 kernel authorizer

- 目标: 落 `SpecialistToolCallAuthorizer` 静态规则
- 落位: `kernel/`
- 依赖: A-P0-02
- 交付物:
  - quant/data/central 的授权规则
  - 对应测试
- 验收:
  - `Quant -> Central -> review_trade` 可授权
  - `Quant -> Central -> subtask.delegate` 仍不可用

### A-P0-04 搭最小 sidecar 骨架

- 目标: 提供 `cmd/brain-data`、`cmd/brain-quant` 最小可启动入口
- 落位: `cmd/brain-data/`、`cmd/brain-quant/`
- 依赖: A-P0-01、A-P0-02
- 交付物:
  - `Kind()`
  - `Tools()`
  - `HandleMethod()`
  - 健康启动配置
- 验收:
  - 三个 brain 都能注册到 Kernel
  - 未实现能力返回显式 `ErrMethodNotFound` 或结构化 stub

### A-P1-01 固定配置 schema

- 目标: 固定 YAML 中 `runtime / validation / vector / llm / accounts`
- 落位: `docs/quant/39-*`、共享 config struct
- 依赖: A-P0 全部
- 验收:
  - 字段命名不再频繁调整
  - Team B/C/D/E/F 都能按同一 config 开发

### A-P1-02 固定错误码和审计枚举

- 目标: 固定 `rejected_stage`、`degraded_reason`、`review_reason_code`
- 落位: `internal/quantcontracts/errors.go`
- 依赖: A-P0-01
- 验收:
  - `SignalTrace` 和 integration tests 可复用同一枚举

### A-P2-01 版本兼容策略

- 目标: 定义 DTO 版本升级规则和兼容窗口
- 落位: `internal/quantcontracts/versioning.go`、文档
- 依赖: A-P1 完成
- 验收:
  - 字段新增/弃用有规则，不靠口头约定

## 五、Team B Backlog: Data 快路径

### B-P0-01 Provider 接入骨架

- 目标: 建立 provider registry、生命周期、状态机
- 落位: `internal/data/provider/`
- 依赖: A-P0-01、A-P1-01
- 验收:
  - provider 状态使用 `starting/syncing/active/degraded/stopped`
  - 至少能注册一个 OKX provider stub

### B-P0-02 DataValidator 主链路

- 目标: 去重、顺序检查、checksum/gap 检测
- 落位: `internal/data/validator/`
- 依赖: A-P0-01
- 验收:
  - 去重维度为 `provider + topic + symbol`
  - same-ts realtime 规则可配置

### B-P0-03 Ring Buffer Writer

- 目标: 稳定写入 `MarketSnapshot` 和 `WriteSeq`
- 落位: `internal/data/ring/`
- 依赖: A-P0-01、B-P0-02
- 验收:
  - `WriteSeq` 单调增长
  - Quant 可按 seq 增量消费

### B-P1-01 Snapshot Processor

- 目标: 标准化原始事件，构造 `MarketSnapshot`
- 落位: `internal/data/processor/`
- 依赖: A-P0-01、B-P0-02
- 验收:
  - 不向 Quant 暴露原始 provider payload
  - snapshot 字段覆盖策略最小所需集

### B-P1-02 data.get_* 服务层

- 目标: 暴露 `data.get_snapshot`、`data.get_feature_vector`、`data.get_candles`、`data.provider_health`
- 落位: `internal/data/service/`、`cmd/brain-data/`
- 依赖: A-P0-02、B-P1-01
- 验收:
  - `tools/call` 路由可用
  - 查询型接口不阻塞 ring writer

### B-P1-03 Fixture Snapshot 输出

- 目标: 提供 Team C/F 可复用的标准 snapshot fixture
- 落位: `test/fixtures/data/` 或 `testing/fixtures/data/`
- 依赖: A-P0-01、B-P1-01
- 验收:
  - fixture 可稳定驱动 Quant 决策测试

### B-P2-01 Gap 回填与恢复

- 目标: provider 断线后支持补 gap 和恢复 active
- 落位: `internal/data/provider/`、`internal/data/validator/`
- 依赖: B-P0/P1
- 验收:
  - gap 回填不污染快路径
  - 恢复前不会错误宣称 ready

### B-P2-02 向量检索接入

- 目标: 落 `data.get_similar_patterns`
- 落位: `internal/data/service/`、`persistence/`
- 依赖: A-P1-01、F-P1-02
- 验收:
  - 未启用 pgvector 时返回显式 unsupported
  - 启用后可跑最小 ANN 查询

## 六、Team C Backlog: Quant 决策主链路

### C-P0-01 Ring Consumer 与 MarketView

- 目标: 按 `WriteSeq` 增量消费 snapshot，适配策略输入
- 落位: `internal/quant/view/`
- 依赖: A-P0-01
- 验收:
  - 不使用数据库轮询
  - 能使用 fixture snapshot 驱动

### C-P0-02 Aggregator 对接现有策略

- 目标: 接现有 `internal/strategy` 输出 `AggregatedSignal`
- 落位: `internal/quant/router/` 或 `internal/quant/service/`
- 依赖: C-P0-01
- 验收:
  - 至少能跑一条 paper 信号生成路径

### C-P0-03 DispatchPlan 构建

- 目标: 账号过滤、per-account 风控预检查、候选单生成
- 落位: `internal/quant/router/`
- 依赖: A-P0-01、A-P1-01、C-P0-02
- 验收:
  - 执行层不再直接消费 `AggregatedSignal`
  - 不同账号候选单可分别追踪

### C-P1-01 GlobalRiskGuard

- 目标: 用“已有持仓 + 本次候选单”评估全局风险
- 落位: `internal/quant/risk/`
- 依赖: C-P0-03
- 验收:
  - 审查前跑一次
  - 审查缩量后重跑一次

### C-P1-02 SignalTrace 主链路

- 目标: 打通通过、拒绝、缩量、失败、不动作的审计
- 落位: `internal/quant/audit/`
- 依赖: A-P0-01、A-P1-02、C-P0-03
- 验收:
  - `SignalTrace` 能解释“为什么没下单”
  - 失败路径有 `rejected_stage`

### C-P1-03 Quant tools 服务层

- 目标: 暴露 `quant.global_portfolio`、`quant.trace_query`、`quant.pause_trading`、`quant.pause_instrument`
- 落位: `internal/quant/service/`、`cmd/brain-quant/`
- 依赖: A-P0-02、C-P1-02
- 验收:
  - Central 能通过 `specialist.call_tool` 调用

### C-P1-04 Stub Review 接口对接

- 目标: 预留 `central.review_trade` 接口位，先接固定 approve/stub
- 落位: `internal/quant/service/` 或 `internal/quant/router/`
- 依赖: A-P0-02、C-P0-03
- 验收:
  - 调用路径使用 `specialist.call_tool`
  - 超时 fallback 也能落 trace

### C-P2-01 审查缩量正式接入

- 目标: 正式处理 `size_factor`
- 落位: `internal/quant/router/`、`internal/quant/risk/`、`internal/quant/audit/`
- 依赖: E-P1-01、C-P1-01、C-P1-02
- 验收:
  - `size_factor` 应用后重跑全局风控
  - trace 中完整记录 review 决策

### C-P2-02 Quant 运行态状态机

- 目标: 收口 `booting/recovering/active/degraded/paused/stopped`
- 落位: `internal/quant/service/`、`cmd/brain-quant/`
- 依赖: D-P0-02、B-P2-01
- 验收:
  - Data stale 会进入 degraded
  - recover 后才能回到 active

## 七、Team D Backlog: 执行与恢复

### D-P0-01 Paper Executor 对接

- 目标: 把现有 paper backend 接到 `ExecutePlan`
- 落位: `internal/quant/execution/`、`internal/execution/`
- 依赖: C-P0-03
- 验收:
  - 能执行 paper 开平仓
  - 能回写 `MemoryState`

### D-P0-02 Account 状态机

- 目标: 收口 `active/paused/error/recovering`
- 落位: `internal/quant/execution/`、`internal/quant/recovery/`
- 依赖: A-P0-01
- 验收:
  - 执行错误进入 `error`
  - reconcile 期间进入 `recovering`

### D-P1-01 Reconcile 与崩溃恢复

- 目标: 重启后先对账，再恢复开仓
- 落位: `internal/quant/recovery/`
- 依赖: D-P0-01、D-P0-02
- 验收:
  - 恢复流程不可跳过
  - 状态不一致时禁止直接进入 active

### D-P1-02 Private WS 状态同步

- 目标: live 账号的订单、持仓、账户变动同步
- 落位: `internal/quant/execution/`
- 依赖: D-P0-02
- 验收:
  - 实盘真值以交易所为准
  - 断连后状态可恢复

### D-P1-03 Reduce-only / Protective Action

- 目标: degraded 或 paused 时只允许保护性动作
- 落位: `internal/quant/execution/`
- 依赖: C-P2-02、D-P0-02
- 验收:
  - 状态机与执行器限制一致

### D-P2-01 Exchange Stop Loss 落地

- 目标: live 模式下交易所侧保护单
- 落位: `internal/quant/execution/`
- 依赖: D-P1-02
- 验收:
  - 没有保护单的开仓请求不能落地

### D-P2-02 Fake Exchange Harness

- 目标: 提供 live 前压测和恢复演练环境
- 落位: `testing/`、`test/`
- 依赖: D-P1-01、F-P1-01
- 验收:
  - 能演练断线、错单、成交回放

## 八、Team E Backlog: Central 慢路径

### E-P0-01 Tool 路由收口

- 目标: 在 `brain-central` 中收口 `central.review_trade`、`central.data_alert`、`central.account_error`、`central.macro_event`
- 落位: `cmd/brain-central/`、`internal/central/control/`
- 依赖: A-P0-02
- 验收:
  - 公开能力都走 `tools/call`
  - `brain/execute` 只承载内部 instruction

### E-P0-02 review_trade 输出 schema

- 目标: 固定 `Approved/Reason/SizeFactor/Actions`
- 落位: `internal/central/review/`
- 依赖: A-P0-01、A-P1-02
- 验收:
  - 可先用 deterministic mock
  - 超时返回 `Approved=true, SizeFactor=1.0`

### E-P1-01 review_trade 正式实现

- 目标: 接 LLM 审查链路
- 落位: `internal/central/review/`
- 依赖: E-P0-02
- 验收:
  - 输出稳定 JSON
  - 不直接调用 Quant 执行器

### E-P1-02 data_alert / account_error 编排

- 目标: 根据告警调用 `quant.pause_*` 或触发人工介入建议
- 落位: `internal/central/control/`
- 依赖: E-P0-01、C-P1-03
- 验收:
  - 中央脑只走 Quant 公开接口

### E-P1-03 daily review 编排

- 目标: 用公开接口收集数据，生成每日复盘结果
- 落位: `internal/central/reviewrun/`
- 依赖: A-P0-02、F-P1-01
- 验收:
  - 复盘只读公开接口
  - 不直接读 Quant 内存态

### E-P2-01 宏观事件与配置更新

- 目标: 落 `central.macro_event` 与 `update_config` 内部流程
- 落位: `internal/central/control/`
- 依赖: B-P1-02、E-P0-01
- 验收:
  - 宏观事件走 tool
  - 配置更新走 `brain/execute` 内部指令

## 九、Team F Backlog: 持久化与集成测试

### F-P0-01 Migration 基线

- 目标: 落 `signal_traces`、`daily_reviews`、`account_snapshots` 等 migration
- 落位: `persistence/`、migration 目录
- 依赖: A-P0-01、A-P1-02
- 验收:
  - schema 变化不靠手工 SQL patch

### F-P0-02 Fixture 基线

- 目标: 固定 snapshot、review、execution fixtures
- 落位: `test/fixtures/`、`testing/fixtures/`
- 依赖: A-P0-01、E-P0-02
- 验收:
  - Team B/C/E/D 都可复用

### F-P1-01 Paper E2E 测试

- 目标: 跑通 `snapshot -> signal -> dispatch -> execute -> trace`
- 落位: `test/`、`testing/`
- 依赖: B-P1-03、C-P1-02、D-P0-01
- 验收:
  - 至少 1 条成功路径和 1 条拒绝路径

### F-P1-02 pgvector / vector 测试

- 目标: 验证 `embedding` 列和 ANN 查询
- 落位: `persistence/`、`test/`
- 依赖: B-P2-02、F-P0-01
- 验收:
  - 未启用 pgvector 时有明确行为
  - 启用时查询可用

### F-P2-01 恢复与降级回归

- 目标: 固化 degraded、recovering、account error、data stale 回归
- 落位: `test/`
- 依赖: B-P2-01、C-P2-02、D-P1-01、E-P1-02
- 验收:
  - 这些路径不靠人工点测

### F-P2-02 审查缩量回归

- 目标: 固化 `size_factor < 1` 路径
- 落位: `test/`
- 依赖: C-P2-01、E-P1-01
- 验收:
  - 审查后复核和 trace 都被覆盖

## 十、每个 Team 的首周目标

如果要立即开工，建议第一周只要求这些：

| Team | 首周目标 |
|------|----------|
| A | 完成 DTO、tool 常量、instruction 常量、authorizer 规则 |
| B | 完成 provider 骨架、validator、ring writer stub |
| C | 完成 MarketView、DispatchPlan、SignalTrace 骨架 |
| D | 完成 paper executor 接口适配和 account 状态机 |
| E | 完成 central tool 路由和 `review_trade` stub |
| F | 完成 migration 骨架、fixture 骨架、paper e2e 测试壳 |

首周结束的验收口径：
- Kernel 能拉起三个 brain
- fixture snapshot 能流入 Quant
- Quant 能生成 `DispatchPlan`
- paper executor 能回写最小状态
- `review_trade` 和 `trace_query` 至少有 stub 可联调

## 十一、推荐派工顺序

按这个顺序最稳：

1. 先派 Team A，卡死共享合同面
2. Team A 完成后，同时派 B/C/E/F
3. 当 C 的 `DispatchPlan` 和 D 的状态机冻结后，再派 D 深入实现
4. Team F 贯穿全程，不要放到最后补测试

## 十二、延期也不能砍掉的项

即使项目赶进度，这些也不能删，只能降级实现：
- `DispatchPlan` 作为唯一执行意图对象
- `SignalTrace` 作为唯一审计对象
- `GlobalRiskGuard` 审查前后双检查
- `review_trade` 走 `specialist.call_tool`
- recover 前禁止重新开仓
- degraded 状态下禁止新开仓

这些项一旦砍掉，系统不是“少一个功能”，而是主链路失真。
