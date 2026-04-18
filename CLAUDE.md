# Brain v3 项目指令

## 语言
始终用中文回复。

## 编译验证
每次修改后必须执行：
```bash
go build ./...
go vet ./...
go test ./... -count=1
```

## 编译目标
- `cmd/brain/` → brain（kernel 主进程）
- `cmd/brain-quant-sidecar/` → brain-quant-sidecar（quant 专精大脑）
- `cmd/brain-data-sidecar/` → brain-data-sidecar（data 专精大脑）

## 架构文档
- 核心架构：`sdk/docs/32-v3-Brain架构.md`
- 重构计划：`sdk/docs/35-v3重构路径与开发计划.md`
- 目录结构：`sdk/docs/36-目录结构层次化重构设计.md`

## v3 架构实施状态（2026-04-18 核查）

### Phase A（资源层）✅ 基本完成
- A-1 BrainPool：✅ ProcessBrainPool 全局注入，修复 per-run fork 缺陷
- A-2 Lease Model：✅ MemLeaseManager AcquireSet 原子租约
- A-3 TaskExecution：✅ 12 状态机 + Mode×Lifecycle×Restart
- A-4 Orchestrator 瘦身：✅ active map 下沉到 pool
- A-5 Dashboard：⚠️ SSE 实时推送可用，但非 WebSocket（文档要求 WS）
- A-6 EventBus：✅ Pub/Sub + SSE 端点
- A-7 HTTP API：✅ /v1/executions + /v1/runs 别名

### Phase B（调度层）⚠️ 骨架完成，5 个缺口
- B-1 BatchPlanner：✅ Welsh-Powell 着色 + AcquireSet 执行路径打通
- B-2 Scheduler：❌ 无任务级调度引擎（只有工具级 BatchPlanner）
- B-3 语义审批：✅ 5 级 ApprovalClass + DefaultSemanticApprover
- B-4 Context Engine：⚠️ Assemble/Compress 可用但无 LLM 摘要路径，Share 仅内存无持久化
- B-5 自适应工具策略：⚠️ 静态配置驱动，非运行时动态调整
- B-6 四层学习：⚠️ L0 仅 quant 实现（5 脑缺），L1-L3 接口完整但未接入执行路径
- B-7 MCP Runtime：✅ MCPBrainPool 完整

### Phase C（编排层）⚠️ 主体完成，2 个缺口
- C-1 Workflow：⚠️ materialized edge 完整，streaming edge 未打通到 WorkflowEngine
- C-2 Background Job：✅ daemon/watch/restart 已实现
- C-3 Manifest 驱动：✅ 替代硬编码注册
- C-4 Brain CLI：⚠️ upgrade/rollback 是 stub（"not implemented yet"）
- C-5 第三方接入：✅ 模板 + 150 项合规测试

### Phase D（分发与远程）⚠️ 骨架完成，均为简化实现
- D-1 Package：⚠️ 打包/安装可用，签名校验未做
- D-2 Marketplace：⚠️ 本地 JSON 查询可用，无远程 marketplace 服务
- D-3 Remote Runtime：⚠️ HTTP JSON-RPC 可用，无 gRPC/服务发现
- D-4 组织级授权：⚠️ 文件策略可用，无 enterprise license/edition 管理

### 已知缺口清单（按优先级）
1. **5 脑 L0 学习缺失**（code/browser/data/verifier/fault 无 BrainLearner）
2. **L1-L3 学习未接入执行路径**（RecordDelegateResult 无人调用）
3. **Context Engine 无 LLM 摘要**（纯截断，无 Provider.Complete 调用）
4. **Streaming Edge 未打通**（sdk/flow/stream.go 存在但 WorkflowEngine 未使用）
5. **B-2 任务级 Scheduler 缺失**（文档定义但无实现）
6. **Dashboard 非 WebSocket**（SSE 功能等价但不符文档描述）
7. **upgrade/rollback stub**
8. **Package 无签名校验**
9. **D-2/D-3/D-4 均为简化本地实现**

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
