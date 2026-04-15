# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[unreleased]: https://github.com/leef-l/brain/compare/v0.7.0...HEAD
[0.7.0]: https://github.com/leef-l/brain/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/leef-l/brain/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/leef-l/brain/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/leef-l/brain/releases/tag/v0.5.0
