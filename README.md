# BrainKernel

BrainKernel 是所有大脑（CentralBrain + N 个 SpecialistBrain）共享的**基础设施**，不是"又一个大脑"。它只做六件事：运行 Agent Loop、抽象 LLM Provider、持久化 BrainPlan、管理 Artifact、执行 Guardrail、记账与审计。它**不做**任何业务决策，不拆任务，不判断验收是否通过——这些是大脑自己的活。

## 版本状态

| 维度 | 版本 | 状态 |
|------|------|------|
| Protocol | v1.0 | 接口定义完成，尚无 wire 实现 |
| Kernel | v0.1.0-skeleton | 接口冻结，方法体 panic("unimplemented") |
| CLI | v0.1.0-skeleton | 2/13 命令可运行（version, doctor） |
| SDK | go/0.1.0-skeleton | 零第三方依赖，纯标准库 |
| 合规测试 | 0/150 | 接口验证阶段，尚未执行合规测试 |

## 包结构

```
brain/
├── agent/         BrainAgent, BrainKind, BrainDescriptor       (02 §3)
├── protocol/      stdio frame, bidir RPC, lifecycle, methods   (20)
├── errors/        BrainError, Class, Decide, Fingerprint       (21)
├── loop/          Run, Turn, Budget, Cache, Stream, Sanitizer  (22)
├── llm/           LLMProvider, ChatRequest/Response            (02 §5 + 22)
├── tool/          ToolRegistry, Tool, ToolSchema               (02 §6)
├── security/      Vault, Sandbox, LLMAccess, AuditEvent, Zones (23)
├── observability/ MetricsRegistry, TraceExporter, LogExporter  (24)
├── persistence/   PlanStore, ArtifactStore, RunCheckpointStore (26)
├── testing/       ComplianceRunner, Cassettes, FakeSidecar     (25)
├── cli/           CLI commands, exit codes, output formats     (27)
├── kernel/        Kernel (top-level assembly), Runner, Transport(02 §12)
├── cmd/           brain CLI 入口 (main.go)
└── docs/          规格文档 (10 篇 RFC 级规格)
```

## 快速开始

### 安装

```bash
go install github.com/leef-l/brain/cmd@latest
```

### 运行

```bash
# 查看版本
brain version

# JSON 格式输出
brain version --json

# 环境检查
brain doctor
```

### 作为库引用

```go
import "github.com/leef-l/brain/llm"
import "github.com/leef-l/brain/tool"
import "github.com/leef-l/brain/kernel"
```

## 构建

```bash
# 编译全部
go build ./...

# 编译 CLI
go build -o brain ./cmd/

# 运行测试
go test ./...

# 静态检查
go vet ./...
```

## 规格文档

docs/ 目录下包含 10 篇 RFC 级规格文档，是 BrainKernel 的设计宪法：

| 编号 | 文档 | 内容 |
|------|------|------|
| 02 | [BrainKernel 设计](docs/02-BrainKernel设计.md) | 内核宪法，顶层设计 |
| 20 | [协议规格](docs/20-协议规格.md) | stdio wire protocol, Content-Length framing, bidir JSON-RPC |
| 21 | [错误模型](docs/21-错误模型.md) | BrainError, 4 维 Class, Decide 决策矩阵, Fingerprint |
| 22 | [Agent Loop 规格](docs/22-Agent-Loop规格.md) | Run/Turn/ToolCall, 3 层 Prompt Cache, streaming |
| 23 | [安全模型](docs/23-安全模型.md) | 5 信任区域, 4 维沙箱, Vault, LLMAccess 三模式 |
| 24 | [可观测性](docs/24-可观测性.md) | OpenTelemetry metrics/traces/logs |
| 25 | [测试策略](docs/25-测试策略.md) | 7 层测试金字塔, cassettes, 150 合规测试 |
| 26 | [持久化与恢复](docs/26-持久化与恢复.md) | SQLite WAL / MySQL 双轨, CAS, Run Resume |
| 27 | [CLI 命令契约](docs/27-CLI命令契约.md) | 13 子命令, 退出码, 输出格式 (human/json/NDJSON) |
| 28 | [SDK 交付规范](docs/28-SDK交付规范.md) | 三级兼容性声明, 150 合规测试总览, 发布流程 |

## 8 项关键设计决策

| # | 决策 | 要点 |
|---|------|------|
| 1 | 子任务分级验收 | RiskLevel × Confidence 分 low/medium/high 三档 |
| 2 | verifier_brain 独立 | 只读、无写工具、不参与实现 |
| 3 | 三种验证路径共存 | 证据直通 + 询问专精大脑 + 故障大脑接管 |
| 4 | fault_policy 中央可配 | 两阶段权限：先出方案，处理不好再接管 |
| 5 | 所有大脑强制 sidecar | 零例外，消灭内外代码分叉 |
| 6 | Runner 四方法 | Run / Cancel / Health / Shutdown |
| 7 | LLMAccess 三模式 | 默认 proxied，可切 direct / hybrid |
| 8 | 协议自研 + MCP Adapter | 自研 stdio JSON-RPC + MCPAdapterRunner 兼容 MCP 生态 |

## 架构概览

```
┌─────────────────────────────────────────────┐
│  EasyMVP Orchestrator (调度层)               │
│  ↕ stdio JSON-RPC                           │
├─────────────────────────────────────────────┤
│  BrainKernel (本仓库)                        │
│  ┌──────────┬──────────┬──────────────────┐ │
│  │ AgentLoop│ LLM Abst │ Tool Registry    │ │
│  │ Run/Turn │ Provider │ Schema+Guardrail │ │
│  ├──────────┼──────────┼──────────────────┤ │
│  │ Security │ Persist  │ Observability    │ │
│  │ Sandbox  │ Plan+CAS │ OTel 三信号      │ │
│  │ Vault    │ Artifact │ Metrics/Trace/Log│ │
│  └──────────┴──────────┴──────────────────┘ │
│  ↕ stdio JSON-RPC (sidecar 协议)             │
├─────────────────────────────────────────────┤
│  BrainAgent (各种大脑进程)                    │
│  CentralBrain / CodeBrain / BrowserBrain    │
│  VerifierBrain / FaultBrain / ...           │
└─────────────────────────────────────────────┘
```

## License

Private repository.
