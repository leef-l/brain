# Brain v3 项目指令

## 语言
始终用中文回复。

## 编译验证
每次修改后必须执行：
```bash
go build ./...
```

**铁律：绝对禁止执行 `go test` 和 `go vet`。** 服务器配置太低，只用 `go build ./...` 验证编译。

## 编译目标
- `cmd/brain/` → brain（kernel 主进程）
- `central/cmd/` → brain-central
- `brains/<kind>/cmd/main.go` → brain-`<kind>`-sidecar (browser/code/desktop/fault/verifier)
- `brains/data/cmd/main.go` → brain-data (独立模式) + `brains/data/cmd/brain-data-sidecar/` → brain-data-sidecar
- `brains/quant/cmd/main.go` → brain-quant + `brains/quant/cmd/brain-quant-sidecar/` → brain-quant-sidecar
- `brains/easymvp/cmd/brain-easymvp-sidecar/` → brain-easymvp-sidecar (无独立模式)

> 完整发布编译：`scripts/release/build-assets.sh 0.7.x` (Linux/macOS) 或 `scripts/release/build-assets.bat 0.7.x` (Windows)，自动产出 12 个二进制 + 装到 `$GOPATH/bin`。

## 架构文档
- ⭐ **单一权威入口**：`docs/README.md`（30 分钟读完整体理解 brain v3）
- 核心架构：`sdk/docs/32-v3-Brain架构.md`（v3 总体 18 节）
- 第三方开发：`sdk/docs/29-第三方专精大脑开发.md`（含双模式、L0、签名）
- 子系统设计稿：`sdk/docs/README.md`（28 份子系统索引）
- MACCS 顶级：`docs/MACCS-架构总纲-v2.md` + `docs/MACCS-实施进度追踪.md`(48/48 = 100%)
- 远程调用：`sdk/docs/37-远程专精大脑调用说明.md`（HTTP/WebSocket/BidirRPC）
- 设计原则：`sdk/docs/钱学森工程控制论-设计原则.md`（反馈闭环、稳定性折衷、不互相影响、时滞、噪声过滤、适应环境、误差控制）

## v3 架构实施状态（2026-04-18 核查）

### Phase A（资源层）✅ 完成
- A-1 BrainPool：✅ ProcessBrainPool 全局注入，修复 per-run fork 缺陷
- A-2 Lease Model：✅ MemLeaseManager AcquireSet 原子租约
- A-3 TaskExecution：✅ 12 状态机 + Mode×Lifecycle×Restart
- A-4 Orchestrator 瘦身：✅ active map 下沉到 pool
- A-5 Dashboard：✅ SSE + WebSocket（E-12 补全 WSHub + /v1/dashboard/ws）
- A-6 EventBus：✅ Pub/Sub + SSE 端点
- A-7 HTTP API：✅ /v1/executions + /v1/runs 别名

### Phase B（调度层）✅ 完成
- B-1 BatchPlanner：✅ Welsh-Powell 着色 + AcquireSet 执行路径打通
- B-2 Scheduler：✅ TaskScheduler 拓扑排序 + L1 brain 选择 + 优先级批次
- B-3 语义审批：✅ 5 级 ApprovalClass + DefaultSemanticApprover
- B-4 Context Engine：✅ LLM 摘要（E-6）+ SharedMessages 持久化（E-7）
- B-5 自适应工具策略：✅ AdaptiveToolPolicy 运行时动态筛选 + Override + 自动禁用低成功率工具
- B-6 四层学习：✅ 5 脑 L0（E-4）+ L1-L3 接入执行路径（E-5）+ 持久化（E-3）
- B-7 MCP Runtime：✅ MCPBrainPool 完整

### Phase C（编排层）✅ 完成
- C-1 Workflow：✅ materialized + streaming edge 完整（E-8 竞态修复）；Orchestrator.ExecuteWorkflow 已接入 chat / run / serve
  - chat：LLM 自动调用 `central.submit_workflow` + `/workflow` slash 命令
  - run：`--workflow dag.json` CLI 入口
  - serve：`POST /v1/workflows` + SSE events
- C-2 Background Job：✅ daemon/watch/restart 已实现
- C-3 Manifest 驱动：✅ 替代硬编码注册
- C-4 Brain CLI：✅ upgrade/rollback 完整实现（E-10）
- C-5 第三方接入：✅ 模板 + 150 项合规测试

### Phase D（分发与远程）✅ 完成
- D-1 Package：✅ 打包/安装 + Ed25519 签名校验（E-11）
- D-2 Marketplace：✅ RemoteMarketplace HTTP 客户端 + 本地缓存 + Sync
- D-3 Remote Runtime：✅ HTTP JSON-RPC + ServiceDiscovery（DNS SRV / Static）+ CircuitBreaker + DiscoverableBrainPool
- D-4 组织级授权：✅ EnterpriseEnforcer（OrgPolicy + PermissionMatrix + RevocationList 三层检查）

### Phase E（统一持久化 + 缺口补全）✅ 完成
持久化统一为 SQLite WAL（`~/.brain/brain.db`），替代 JSON 全量重写 + 纯内存。

Sprint E-1（并行，无依赖）：✅ 全部完成
- E-1: ✅ SQLite 驱动实现（sdk/persistence/driver_sqlite.go）
- E-8: ✅ Streaming Edge 打通到 WorkflowEngine（竞态修复）
- E-10: ✅ upgrade/rollback 命令
- E-11: ✅ Package 签名校验（Ed25519）

Sprint E-2（依赖 E-1）：✅ 全部完成
- E-2: ✅ RunStore 迁移到 SQLite + SQLiteBackend
- E-3: ✅ LearningEngine L1/L2/L3 持久化（自动 Save/Load）
- E-9: ✅ AuditLog 持久化（AuditLogger 接口 + SQLite + Purge）
- E-12: ✅ Dashboard WebSocket（WSHub 广播 + /v1/dashboard/ws 端点）

Sprint E-3（依赖 E-2/E-3）：✅ 全部完成
- E-4: ✅ 5 脑 L0 BrainLearner（data 特化 + code/browser/verifier DefaultBrainLearner + central 特化）
- E-5: ✅ L1-L3 接入执行路径（L2 RecordSequence 接入 Delegate + CollectMetrics 主动拉取）
- E-6: ✅ Context Engine LLM 摘要（Compress 策略 2.5 LLM summarize + NewContextEngineWithLLM）
- E-7: ✅ Context SharedMessages 持久化（shared_messages 表 + 异步写入）

## 目录结构（cmd/brain/ 子包）
```
cmd/brain/
├── main.go, dispatcher.go          # 入口
├── config/     (5 files)            # 配置层
├── chat/       (14 files)           # 交互式 REPL
├── command/    (12 files)           # 子命令
├── cliruntime/ (4 files)            # 运行时基础设施
├── env/        (3 files)            # 执行环境
├── bridge/     (3 files)            # 适配器
├── dashboard/  (2 files + static/)  # Dashboard
├── provider/   (1 file)             # LLM provider
├── term/       (7 files)            # 终端交互
├── diff/       (1 file)             # diff 预览
└── 胶水层 (~15 files)               # 别名 + 薄适配
```

## 依赖方向（严格单向）
```
sdk/kernel/ → sdk/loop/ → sdk/llm/
sdk/kernel/ → sdk/tool/
sdk/kernel/ → sdk/events/
cmd/brain/ → sdk/*
cmd/brain/chat/ → cmd/brain/config/, env/, term/, diff/, cliruntime/, provider/
cmd/brain/command/ → cmd/brain/config/, cliruntime/, provider/
```
禁止：chat/ ↔ command/ 互相引用。

### Phase F（架构重构完成）✅ 完成
- F-1: ✅ ThinBrain 工厂化 — `sdk/shared/thin_brain.go` 统一 4 个瘦大脑启动逻辑，main.go 各 5 行
- F-2: ✅ 工具注册统一 — `RegisterWithPolicy` 集中处理工具注册 + 策略过滤
- F-3: ✅ 权限拦截层 — `RunThinBrainMain` 注入许可证校验 + 可选 PreRun hook
- F-4: ✅ 零重复验证 — 4 个瘦大脑共享同一套 HandleMethod / brain/execute / brain/metrics / brain/learn 逻辑
- F-5: ✅ 文档对齐 — README 工具列表从 brain.json 权威源派生，CLI 命令全部有实际实现

## MACCS v2 实施状态（2026-05-02 完成）✅ 48/48 = 100%

### Wave 0-3 主体（38 项）✅ 全部完成
- Wave 0 (3): task_complete 终止 / LLM 超时 90→180s / serve turn 20→50
- Wave 1 (10): TaskPlan + ProjectProgress + InterruptSignal + Checkpoint + ProgressRPC + DynamicBudget + ReviewLoop
- Wave 2 (7): 项目记忆 + MemoryRetriever + ComplexityEstimator + MetaCognitive + ContextEngine + ModelRouter + Prompt 升级
- Wave 3 (10): ClosedLoopController 7 阶段闭环（需求→方案→审核→执行→验收→交付→复盘）

### Wave 4 并发控制 (5 项) ✅ 全部完成
- 4.1 资源访问追踪 (并入 ExecutionScheduler)
- 4.2 ConflictDetector (sdk/kernel/conflict_detector.go)
- 4.3 DeadlockDetector (Wave 7 接入,绕开 LeaseManager 改造)
- 4.4 Arbiter ResolveDeadlock (Wave 7 接入)
- 4.5 SmartScheduler (拓扑层之上的冲突感知重排)

### Wave 5 学习系统进化 (6 项) ✅ 全部完成
- 5.1 因果学习 + 路由权重 0.35 (orchestrator.go:1591 resolveTargetKind)
- 5.2 迁移学习 ((language, domain, kind) 三元组指纹)
- 5.3 主动学习 + EventBus brain.feedback.requested 订阅 goroutine
- 5.4 项目模式提取 (异步抽取 + ProjectMemory.Store)
- 5.5 自适应 Prompt (A/B 变体作 L1 system block，cache=true)
- 5.6 能力画像可视化 (Dashboard 雷达图)

### Wave 6 生产硬化 (7 项) ✅ 全部完成
- 6.1 HealthManager (GET /v1/health)
- 6.2 ChaosEngine (POST/DELETE /v1/chaos/experiments)
- 6.3 PerfBenchmark (GET /v1/metrics/perf)
- 6.4 ObservabilityHub (GET /v1/observability)
- 6.5 SecurityAuditor (POST /v1/projects 入参审计)
- 6.6 MultiProjectManager (项目级配额 + 429)
- 6.7 ProductionReadiness (GET /v1/readiness)

### 关键路由公式（0139b5e Wave 7 调整）
```
combined = capScore*0.4 + learnScore*0.25 + causalScore*0.35
```
其中 5% 概率走 active learning 探索高不确定 brain。

### MACCS 配置（~/.brain/config.json）
9 块 MACCSConfig，全部默认 enabled=true：
- health / perf / observability / security / multi_project / adaptive_prompt
- conflict (4.2/4.5) — dry_run=true 首周观察期
- pattern_extractor (5.4)
- deadlock (4.3/4.4 Wave 7) — dry_run=true 首周观察期
