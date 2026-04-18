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

## v3 架构实施状态（2026-04-18）

### Phase A（资源层）✅ 全部完成
- Step 1-3: BrainPool + 全局注入 + Orchestrator 瘦身
- Step 5-7: EventBus SSE + TaskExecution + HTTP API 统一
- Step 8-9: LeaseManager + Dashboard API + SPA
- Step 14: Brain Manifest 落地（7 脑 brain.json）
- Step 15, 17: ToolConcurrencySpec + Dashboard embed

### Phase B（调度层）✅ 全部完成
- Step 10: Dispatch Policy BatchPlanner（冲突图 + AcquireSet 打通）
- Step 11: Context Engine（Assemble/Compress/Share）
- Step 12: MCP-backed Runtime（MCPBrainPool）
- Step 13: 语义审批分级（5 级 ApprovalClass）
- Step 16: BrainLearner L0（QuantBrainLearner + metrics 上报）
- 接线：LearningEngine 接入 delegate 链，Dashboard leases/providers 端点

### Phase C（编排层）✅ 全部完成
- C-1: Workflow DAG 引擎（拓扑排序 + 并行执行 + 环路检测）
- C-2: Background Job（daemon/watch 循环，自动重启）
- C-3: Manifest 驱动脑发现（替代硬编码注册）
- C-4: Brain 管理 CLI（list/install/activate/deactivate/init/pack/uninstall）
- C-5: 第三方 Brain 接入（sidecar 模板 + 150 项合规测试全通过）

### Phase D（分发与远程）✅ 全部完成
- D-1: Package 规范（.brainpkg 打包/验证/安装/卸载）
- D-2: Marketplace 索引（LocalMarketplace 搜索/筛选 + CLI search/info）
- D-3: Remote Runtime（RemoteBrainPool HTTP JSON-RPC + 7 项测试）
- D-4: 组织级授权（OrgPolicy 黑白名单/并发限制 + 8 项测试）

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
