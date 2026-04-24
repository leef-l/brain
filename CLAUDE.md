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
- `cmd/brain-quant-sidecar/` → brain-quant-sidecar（quant 专精大脑）
- `cmd/brain-data-sidecar/` → brain-data-sidecar（data 专精大脑）

## 架构文档
- 核心架构：`sdk/docs/32-v3-Brain架构.md`
- 重构计划：`sdk/docs/35-v3重构路径与开发计划.md`
- 目录结构：`sdk/docs/36-目录结构层次化重构设计.md`
- 第三方开发：`sdk/docs/29-第三方专精大脑开发.md`（含双模式、L0、签名）
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
- C-1 Workflow：✅ materialized + streaming edge 完整（E-8 竞态修复）
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
