# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### 基础大脑深度改造（Agent Loop 架构级）

- **sidecar Agent Loop 接入 sdk/loop.Runner**：4 个基础大脑（code/browser/verifier/fault）的 `RunAgentLoop` 从手搓 250 行 for-loop 替换为 `sdk/loop.Runner` 适配层。一次性激活此前闲置的 4000 行工业级能力：
  - **LoopDetector**：tool+args hash 检测重复，防止 LLM 在同一错误 edit 上死循环
  - **Budget 5 维**：MaxTurns / MaxCostUSD / MaxLLMCalls / MaxToolCalls / MaxDuration 全面贯通
  - **MessageCompressor**：超预算时自动压缩长会话
  - **CacheBuilder**：Prompt Cache L1 system 自动打 cache_control，长任务 token 成本下降 50-90%
  - **MemSanitizer**：prompt injection / 二进制 / 长度 / BIDI / PII 6 阶段工具结果卫生化
  - **ToolObserver**：工具执行生命周期观测
- **新增 `sdk/sidecar/kernel_provider.go`**：`kernelLLMProvider` 实现 `llm.Provider`，把 sidecar 的 LLM 调用通过反向 RPC 转发到 Kernel，sidecar 能透明复用主进程所有 Provider（Anthropic / DeepSeek / Mock）。
- **新增 `RunAgentLoopWithContext`**：接受 `ExecuteRequest.Context`，把 central 装配的 ContextEngine 上下文作为前置 user message 注入对话起始。跨脑委派不再"每次新鲜开始"。
- **Usage 回传链路**：`llm_proxy.go` 的 `llmCompleteResponse` 新增 `Usage` 字段（InputTokens/OutputTokens/CacheReadTokens/CacheCreationTokens/CostUSD）；sidecar 侧 `wireToChatResponse` 映射到 `llm.ChatResponse.Usage`，`Budget.CheckCost()` 从此真实生效。
- **`brain.note` Scratchpad 工具**（`sdk/tool/builtin_note.go`）：支持 add/update/done/list/clear，按 brainKind 隔离进程内存储。4 个基础大脑全部注册，Code Brain system prompt 加"复杂任务先 plan 再执行"引导。参考 Claude Code 的 TodoWrite 设计。Schema description 明确声明"in-memory only, lost on sidecar restart"。

#### 工具错误结构化（失败反馈友好化）

- **`code.edit_file` 找不到 old_string 时**：返回 `hints`（CRLF / UTF-8 BOM / 大小写 / leading-trailing whitespace 诊断）+ `similar_lines` 最相近 3 行（带行号）。LLM 能立即定位差异而不是盲试。
- **`code.search` 返回 `backend` + `notes`**：标注使用 ripgrep 还是 walk，声明两后端差异（1MB 上限、glob basename vs path 语义）。LLM 能感知"我搜到的是否完整"。
- **`*.shell_exec` 截断标注**：`limitWriter` 新增 `dropped` 计数，stdout/stderr 末尾追加 `[... truncated: N bytes dropped ...]` 可见提示 + response 带 `stdout_dropped_bytes` / `stderr_dropped_bytes` 结构化字段。LLM 不再误以为看到完整输出。

#### 工具扩展

- **Code Brain 工具**（5 → 8 个）：新增 `code.edit_file`、`code.list_files`、`code.note`；`code.search` 增强。
- **Browser / Verifier / Fault Brain** 各自注册 `<kind>.note` 工具。
- Code Brain sidecar、主进程 chat / run 三处 registry 全部注册新工具，LLM 在所有模式下可见。
- approval 启发式规则新增：`code.edit_*` → `ApprovalWorkspaceWrite`、`*.note` → `ApprovalReadonly`（优先于 `browser.*` 网络规则）。

### Fixed

- **Fault Brain `kill_process` 硬编码 deny-list**：拒绝 init/systemd/sshd/kthreadd 等 30+ 系统进程名 + PID<100 + kernel/systemd 前缀。此前仅依赖 system prompt 提示，LLM 被 prompt-inject 即可杀关键进程。
- **Fault Brain `inject_latency` network 模式去欺骗性**：此前只返回 tc 命令字符串却 status=completed 误导 LLM。改为真实执行 `tc netem`（检测 Linux+tc+root），带 deferred cleanup 自动移除规则；能力不足时返回明确 `status=unsupported`。
- **Browser Brain session 泄漏**：`buildRegistry` 每次 RPC 调用都 `NewBrowserTools()` 导致 Chromium 进程泄漏并丢失会话。改为复用 handler 的 `h.browserTools`。
- **`ExecuteRequest.Context` 被静默丢弃**：4 个基础大脑此前不读 `req.Context`，central 花 token 装配的 ContextEngine 上下文到 sidecar 入口即归零。
- **approval `browser.note` 误归类**：`browser.*` → external-network 规则抢占了 `*.note`，本应是纯本地 scratchpad 的 `browser.note` 被标成需要网络审批。调整规则表顺序，`*.note` 置于 L3 之后、L4 前。

## [1.0.0] - 2026-04-19

### Added

- **v3 架构四阶段全部完成** — Phase A（资源层）/ B（调度层）/ C（编排层）/ D（分发与远程）共 21 项交付。
- **Phase A 资源层**: BrainPool 全局注入 + per-run fork、MemLeaseManager 原子租约、TaskExecution 12 状态机、Orchestrator 瘦身、Dashboard SSE + WebSocket、EventBus Pub/Sub、HTTP API /v1/executions + /v1/runs。
- **Phase B 调度层**: BatchPlanner Welsh-Powell 着色、TaskScheduler 拓扑排序 + 优先级批次、语义审批 5 级 ApprovalClass、Context Engine LLM 摘要 + SharedMessages 持久化、AdaptiveToolPolicy 运行时动态工具筛选、四层学习（L0-L3）、MCP Runtime 完整。
- **Phase C 编排层**: Workflow materialized + streaming edge、daemon/watch/restart 后台任务、Manifest 驱动替代硬编码注册、Brain CLI upgrade/rollback、第三方接入模板 + 150 项合规测试。
- **Phase D 分发与远程**: Package 打包/安装 + Ed25519 签名校验、RemoteMarketplace HTTP 客户端 + 本地缓存 + Sync、HTTP JSON-RPC + ServiceDiscovery (DNS SRV / Static) + CircuitBreaker、EnterpriseEnforcer（OrgPolicy + PermissionMatrix + RevocationList）。
- **Phase E 统一持久化**: SQLite WAL 替代 JSON 全量重写 + 纯内存，含 RunStore/LearningStore/AuditLogger/SharedMessageStore 迁移。
- **Sprint E-1**: SQLite 驱动实现、Streaming Edge 竞态修复、upgrade/rollback 命令、Package Ed25519 签名校验。
- **Sprint E-2**: RunStore 迁移到 SQLite、LearningEngine L1/L2/L3 持久化、AuditLogger 接口 + SQLite + Purge、Dashboard WebSocket Hub 广播。
- **Sprint E-3**: 5 脑 L0 BrainLearner、L1-L3 接入执行路径、Context Engine LLM 摘要、SharedMessages 持久化。
- **Resume 对话上下文恢复**: Checkpoint 存入 messages/system/tools 到 CAS，resume 从 CAS 恢复完整对话历史、system prompt 和 tool schemas。
- **README 全面重写**: 面向用户的安装→配置→使用完整指南，预编译二进制为推荐安装方式，新增 FAQ。

### Fixed

- `CanResume` 状态字符串大小写错误（`"Completed"/"Failed"/"Cancelled"` → `"completed"/"failed"/"canceled"`），修复终态 Run 被错误允许 resume。
- `resume` 命令接入 `ResumeCoordinator`，启用 `MarkResumeAttempt` 3 次上限保护（此前完全失效）。
- `resume` 完成后将 FinalMessages 存入 CAS（此前仅更新 state 字段，下次 resume 仍用旧快照）。
- `chat/executor.go` 错误路径保存 crash checkpoint（此前 crash 后无 checkpoint anchor 可 resume）。
- `chat/executor.go` 正确传递 system prompt 到 checkpoint（此前传 nil 导致 SystemRef 永远为空）。
- `chat/executor.go` 错误路径状态拼写 `"cancelled"` → `"canceled"` 与 `loop.State` 一致。
- `LoadCheckpointSystem` / `LoadCheckpointTools` 补全（此前 SystemRef/ToolsRef 有存无取）。
- `SaveRunCheckpointWithMessages` 支持 ToolsRef 存入 CAS（此前 tools_ref 列永远为空）。

## [0.7.0] - 2026-04-15

### Added

- **Specialist brain sidecar architecture** (三脑架构): Data Brain, Quant Brain, and Central Brain communicate via stdio JSON-RPC sidecar protocol with automatic process lifecycle management.
- **Quant Brain sidecar** with 14 tools: `global_portfolio`, `account_status`, `daily_pnl`, `trade_history`, `trace_query`, `strategy_weights`, `global_risk_status`, `pause_trading`, `resume_trading`, `account_pause`, `account_resume`, `account_close_all`, `force_close`, `backtest_start`.
- **Data Brain sidecar** with 9 tools: `get_snapshot`, `get_candles`, `get_feature_vector`, `provider_health`, `validation_stats`, `backfill_status`, `active_instruments`, `replay_start`, `replay_stop`.
- **Bridge tool pattern**: specialist sidecar tools are registered directly in chat/run/serve tool registries, enabling the LLM to call `quant.*` and `data.*` tools without going through `central.delegate`.
- **Cross-brain authorization policy** (`SpecialistToolCallAuthorizer`): static allowlist governs sidecar-to-sidecar tool calls (quant→data market queries, quant→central trade review, data→central alerts).
- **Dynamic orchestrator prompt generation**: `buildOrchestratorPrompt` auto-discovers available specialist brains and generates delegation instructions with direct tool listings.
- **`brains` config field**: `config.json` now supports a `brains` map for declaring specialist brain sidecar binary paths and environment variables.
- **Release packaging auto-discovery**: `package.sh` and `build-assets.bat` now auto-detect all specialist brain sidecar binaries under `brains/<name>/cmd/brain-<name>-sidecar/` in addition to standalone brain binaries.

### Fixed

- `brain serve` now validates request-level `file_policy` / restricted-mode requirements and reserves concurrency before persisting a run, so rejected `POST /v1/runs` requests no longer leave orphan `"running"` records in `list` / `status`.
- Sandboxed `code.search` now treats omitted or empty `path` as the sandbox primary workdir, instead of falling back to the process `cwd`.
- Bridge tool schemas now match sidecar tool schemas exactly (`trace_query`, `trade_history`, `account_status`).
- Removed nonexistent `data.get_similar_patterns` from cross-brain authorization policy.
- Fixed nil pointer dereference in `TradingUnit` when `cfg.Account` is nil.
- Fixed semantic naming error: `dailyLoss` → `dailyPnL` in `globalRiskStatusTool`.
- Fixed ignored `QueryPositions` error in quant sidecar `execGlobalPortfolio`.

## [0.6.0] - 2026-04-13

### Added

- Persistence Driver abstraction layer: `Register/Open/Drivers` pattern (like `database/sql`), built-in `"mem"` and `"file"` drivers, `kernel.WithPersistence()` one-shot wiring.
- OTLP exporters: `OTLPTraceExporter`, `OTLPLogExporter`, `OTLPMetricsExporter` with batched flush and pluggable `Sender` callbacks for wire-protocol-agnostic OTel interop.
- Log sanitization: `PatternSanitizer` with built-in redaction of API keys (Anthropic/OpenAI/Bearer tokens), configurable sensitive key list, regex value patterns, extensible via `WithExtraSensitiveKeys`/`WithExtraValuePatterns`.
- Vault `Rotate` and `List` methods: atomic credential rotation preserving TTL, prefix-based key listing with expired-entry filtering, full audit coverage.
- `DirectLLMAccess` strategy: Zone 1 brains fetch short-lived credentials from Vault, with audit trail.
- `HybridLLMAccess` strategy: proxied-by-default with on-demand ephemeral credentials, provider whitelist support.
- `SandboxEnforcer` with `SandboxLevel` model (L0-none, L1-seccomp, L2-container, L3-vm) and level validation.
- License integration in all 5 sidecar `main()` functions via `license.CheckSidecar()`: paid brains require license, free brains pass through, `BRAIN_LICENSE_REQUIRED=1` forces verification for enterprise deployments.

### Fixed

- CDP WebSocket data race: `wsConn.closed` field converted from `bool` to `atomic.Bool` to eliminate race between `ReadMessage` and `Close` goroutines.

## [0.5.1] - 2026-04-13

### Fixed

- Fixed 5 compilation errors in `cmd/` package: `bgCtx()` call sites not destructuring `(context.Context, context.CancelFunc)` return value.
- Corrected version numbers from premature `1.0.0` back to `0.5.1` across `VERSION.json`, `version.go`, and `doc.go`.

## [0.5.0] - 2026-04-13

### Added

- Complete `brain serve` Run API: `POST/GET/DELETE /v1/runs`, cancellation, status query, and HTTP smoke coverage.
- Built-in specialist sidecars for `central`, `code`, `verifier`, `fault`, and `browser`, including orchestrator retry and health-check coverage.
- `tool_profiles` / `active_tools` with scope-aware filtering for `chat`, `run`, delegated sidecars, and `brain tool list/describe/test --scope`.
- Diff preview, interactive sandbox approval, and ToolObserver output propagation in the chat workflow.
- GitHub Releases packaging workflow with multi-platform archives, checksums, release notes extraction, and artifact attestations.
- Release helper scripts under `scripts/release/` for local dry-runs and reproducible packaging.
- Custom source-available licensing: free for personal use, separate paid license required for organizational and commercial use.

### Changed

- `code.search` now prefers `rg`/ripgrep for fast text and regex search, while keeping the pure Go walker as fallback.
- Windows terminal input now uses a real console raw backend instead of line-mode fallback.
- Official release packages place `brain` and all built-in sidecars in the same directory so delegated execution works out of the box.

### Fixed

- `brain serve` cancellation now remains `cancelled` instead of reverting to `failed` after background execution exits.
- `loop.Runner` now maps provider-call `context canceled` to `StateCanceled`.
- Cross-platform build issues on Darwin, FreeBSD, and Windows caused by raw terminal and signal handling differences.
- Windows sidecar discovery now checks same-directory `.exe` binaries, matching the packaged release layout.

[unreleased]: https://github.com/leef-l/brain/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/leef-l/brain/compare/v0.7.0...v1.0.0
[0.7.0]: https://github.com/leef-l/brain/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/leef-l/brain/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/leef-l/brain/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/leef-l/brain/releases/tag/v0.5.0
