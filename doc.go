// Package brain is the reference implementation of BrainKernel v1.
//
// BrainKernel is a multi-brain agent infrastructure defined by a set of RFC-style
// specifications in docs/next-gen-executor/. This package is the Go reference SDK
// targeted by 28-SDK交付规范.md.
//
// # Specification index
//
//   - 02-BrainKernel设计.md        constitution / top-level design
//   - 20-协议规格.md               stdio wire protocol (Content-Length framing, bidir JSON-RPC)
//   - 21-错误模型.md               BrainError, 4-dim Class, Decide, Fingerprint
//   - 22-Agent-Loop规格.md         Run/Turn/ToolCall, 3-layer Prompt Cache, streaming
//   - 23-安全模型.md               5 trust zones, 4-dim sandbox, Vault, LLMAccess modes
//   - 24-可观测性.md               OTel metrics/traces/logs
//   - 25-测试策略.md               7-layer pyramid, cassettes, 150 compliance tests
//   - 26-持久化与恢复.md           SQLite WAL / MySQL dual-track, CAS, Run Resume
//   - 27-CLI命令契约.md            `brain` CLI contract (13 subcommands, exit codes)
//   - 28-SDK交付规范.md            SDK delivery spec (150 compliance tests)
//
// # Package layout
//
// Each subpackage corresponds to a down-level spec:
//
//   agent/         BrainAgent, BrainKind, BrainDescriptor       (02 §3)
//   protocol/      stdio frame, bidir RPC, lifecycle, methods   (20)
//   errors/        BrainError, Class, Decide, Fingerprint       (21)
//   loop/          Run, Turn, Budget, Cache, Stream, Sanitizer  (22)
//   llm/           LLMProvider, ChatRequest/Response            (02 §5 + 22)
//   tool/          ToolRegistry, Tool, ToolSchema               (02 §6)
//   security/      Vault, Sandbox, LLMAccess, AuditEvent, Zones (23)
//   observability/ MetricsRegistry, TraceExporter, LogExporter  (24)
//   persistence/   PlanStore, ArtifactStore, RunCheckpointStore (26)
//   testing/       ComplianceRunner, Cassettes, FakeSidecar     (25)
//   cli/           CLI commands, exit codes, output formats     (27)
//   kernel/        Kernel (top-level assembly), Runner, Transport (02 §12)
//   sidecar/       Sidecar shared runtime framework             (02 §12.5)
//
// # Implementation status
//
// v0.6.0: Persistence Driver abstraction layer (Register/Open pattern),
// OTLP trace/metrics/log exporters with batched flush, log sanitization
// (PatternSanitizer with API key/token/credential redaction), Vault
// Rotate/List methods, DirectLLMAccess and HybridLLMAccess strategies,
// SandboxEnforcer with L0-L3 level model, license integration in all 5
// sidecar binaries, CDP WebSocket race condition fix.
//
// # Compatibility
//
//   Protocol: v1.0
//   Kernel:   v0.6.0
//   CLI:      v0.6.0 (13/13 commands implemented)
//   SDK:      go/0.6.0
//
// A compliant SDK must pass 150 compliance tests per 28-SDK交付规范.md §8.
package brain
