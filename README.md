# BrainKernel

BrainKernel 是所有大脑（CentralBrain + N 个 SpecialistBrain）共享的**基础设施**，不是"又一个大脑"。它只做六件事：运行 Agent Loop、抽象 LLM Provider、持久化 BrainPlan、管理 Artifact、执行 Guardrail、记账与审计。它**不做**任何业务决策，不拆任务，不判断验收是否通过——这些是大脑自己的活。

> 2026-04-15 v0.7.0：
> **三脑架构**：Data Brain sidecar（9 tools）+ Quant Brain sidecar（14 tools）+
> Bridge Tool 模式（chat/run/serve 直接调用专精工具）+ 跨大脑授权策略 +
> 动态 orchestrator prompt 生成 + 打包脚本自动探测 sidecar 二进制。
> `go build/test/vet -race` 全绿，10 个二进制。

## 版本状态

| 维度 | 版本 | 状态 |
|------|------|------|
| Protocol | v1.0 | Content-Length framed stdio JSON-RPC 2.0 完整实现 |
| Kernel | v0.7.0 | 三脑 sidecar 架构、跨大脑授权、Bridge Tool、动态 prompt |
| CLI | v0.7.0 | 13/13 命令实现，chat/run/serve 全面接入专精大脑工具 |
| SDK | go/0.7.0 | 仅 1 个外部依赖（chroma 语法高亮），Go 1.24 |
| 代码规模 | 340+ 个 Go 文件 | ~65,000 行代码 |

### 测试覆盖

| 测试集 | 数量 | 状态 |
|--------|------|------|
| 骨架测试 | 133 | test/skeleton/ 全部通过 |
| 合规测试 | 151 | test/compliance/ 8 类别全覆盖 |
| Runner 测试 | 10 | loop/ Agent Loop 执行引擎 |
| 流式管道测试 | 6 | loop/ stream.start/chunk/end 全链路 |
| AnthropicProvider 测试 | 16 | llm/ cassette 录制回放 + httptest |
| 真实工具测试 | 26 | tool/ read_file/write_file/search/shell_exec |
| Verifier 工具测试 | 15 | tool/ read_file/run_tests/check_output |
| 传输层测试 | 7 | kernel/ transport |
| MCP 适配器测试 | 12 | kernel/mcpadapter/ 端到端 |
| Sidecar 框架测试 | 5 | sidecar/ BrainHandler 接口 |
| 持久化测试 | 11 | persistence/ FileStore 全接口 |
| 其他单元测试 | ~25 | errors/security/observability/protocol |
| **总计** | **730** | **`go test -race ./...` 全绿** |

## 包结构

```
brain/
├── sdk/                    SDK 核心库
│   ├── agent/              BrainAgent, BrainKind, BrainDescriptor       (02 §3)
│   ├── protocol/           stdio frame, bidir RPC, lifecycle, methods   (20)
│   ├── errors/             BrainError, Class, Decide, Fingerprint       (21)
│   ├── loop/               Run, Turn, Budget, Cache, Stream, Sanitizer  (22)
│   ├── llm/                LLMProvider, ChatRequest/Response            (02 §5 + 22)
│   ├── tool/               ToolRegistry, Tool, ToolSchema               (02 §6)
│   ├── security/           Vault, Sandbox, LLMAccess, AuditEvent, Zones (23)
│   ├── observability/      OTLP, MetricsRegistry, TraceExporter         (24)
│   ├── persistence/        PlanStore, ArtifactStore, Driver             (26)
│   ├── kernel/             Kernel, Orchestrator, Transport              (02 §12)
│   │   └── mcpadapter/     MCP 适配器 — 桥接 MCP 生态工具
│   ├── sidecar/            Sidecar 共享运行时框架 — BrainHandler 注入
│   └── license/            Sidecar License 验证 — CheckSidecar/IsPaidBrain
├── brains/                 专精大脑实现
│   ├── data/               数据大脑 — 行情采集、192 维特征向量、Ring Buffer
│   │   ├── cmd/            standalone 二进制 (brain-data)
│   │   ├── cmd/brain-data-sidecar/  sidecar 二进制 (brain-data-sidecar, 9 tools)
│   │   ├── sidecar/        sidecar handler + tools 实现
│   │   ├── feature/        特征引擎 (assembler + ML fallback)
│   │   ├── ringbuf/        Ring Buffer + MarketSnapshot
│   │   ├── provider/       OKX WebSocket 数据源
│   │   └── store/          PostgreSQL K 线持久化
│   ├── quant/              量化大脑 — 策略聚合、风控、交易执行
│   │   ├── cmd/            standalone 二进制 (quant-brain)
│   │   ├── cmd/brain-quant-sidecar/  sidecar 二进制 (brain-quant-sidecar, 14 tools)
│   │   ├── sidecar/        sidecar handler + tools 实现
│   │   ├── strategy/       4 策略 + RegimeAwareAggregator
│   │   ├── risk/           AdaptiveGuard + BayesianSizer + GlobalGuard
│   │   ├── exchange/       Paper/OKX Exchange 适配器
│   │   ├── tradestore/     交易记录持久化 (Memory/PG)
│   │   ├── tracer/         信号追踪链 (Memory/PG)
│   │   └── backtest/       回测引擎
│   ├── code/               代码大脑 — 读写文件、搜索代码、执行命令
│   ├── browser/            浏览器大脑 — CDP 自动化
│   ├── verifier/           验证大脑 — 只读独立验证
│   └── fault/              故障大脑 — 混沌工程
├── central/                中央大脑 — 协调器 + LLM 复审
│   ├── cmd/                brain-central 二进制
│   ├── llm/                LLM 客户端 (DeepSeek/Claude/HunYuan)
│   └── quant/              量化复审 handler
├── cmd/brain/              brain CLI 入口 — 13 个子命令
│   ├── specialist_bridge.go  Bridge Tool（chat/run 直调专精工具）
│   └── chat_prompt.go        动态 orchestrator prompt 生成
├── npm/brain-cli/          pnpm/npm wrapper（从 GitHub Releases 下载二进制）
├── scripts/release/        发布打包脚本（自动探测所有 sidecar）
└── docs/                   规格文档 + 使用指南
```

## 快速开始

### 安装

```bash
# 方式 1：pnpm/npm 全局安装（推荐，自动下载对应平台二进制）
pnpm add -g @leef-l/brain-cli
# 或
npm install -g @leef-l/brain-cli

# 方式 2：从源码编译
go build -o bin/brain ./cmd/brain/
sudo mv bin/brain /usr/local/bin/
```

### 正式发布包

正式版本通过 GitHub Releases + npm 分发。每个平台发布包包含 **10 个二进制**：

| 二进制 | 说明 |
|--------|------|
| `brain` | CLI 主程序（chat/run/serve/doctor 等 13 命令） |
| `brain-central` | 中央大脑 sidecar（协调 + LLM 复审） |
| `brain-code` | 代码大脑 sidecar |
| `brain-browser` | 浏览器大脑 sidecar（CDP 自动化） |
| `brain-verifier` | 验证大脑 sidecar（只读） |
| `brain-fault` | 故障大脑 sidecar |
| `brain-data` | 数据大脑独立运行版 |
| `brain-data-sidecar` | 数据大脑 sidecar（9 tools，Kernel stdio） |
| `brain-quant` | 量化大脑独立运行版 |
| `brain-quant-sidecar` | 量化大脑 sidecar（14 tools，Kernel stdio） |

打包脚本自动探测 `brains/*/cmd/main.go` 和 `brains/*/cmd/brain-*-sidecar/main.go`，
新增专精大脑时无需手动修改打包脚本。

维护者本地 dry-run：

```bash
./scripts/release/build-assets.sh 0.7.0 ./dist
./scripts/release/release-notes.sh 0.7.0
```

正式发布：

```bash
git tag v0.7.0
git push origin v0.7.0
# npm 发布
cd npm/brain-cli && npm publish --access public --registry https://registry.npmjs.org
```

### 基本用法

```bash
# 查看版本
brain version
brain version --json

# 交互式 REPL（`brain` 无参数等价于 `brain chat`）
brain
brain chat --workdir . --mode restricted

# 环境检查
brain doctor

# `brain doctor` 当前会实际检查：
# workspace、config.json 权限/语法、file-backed runtime、
# 凭证解析、sidecar 可发现性、provider 主机 TCP 可达性、
# artifact store round-trip

# 运行一次对话（需要 API Key）
ANTHROPIC_API_KEY=sk-xxx brain run --prompt "读取 go.mod 文件内容"
brain run --prompt "hello world" --model claude-sonnet-4-20250514
brain run --prompt "修复 cmd 下的测试" \
  --model-config-json '{"provider":"anthropic","model":"claude-sonnet-4-20250514"}'

# restricted 模式：默认拒绝，只允许读/写白名单文件
brain run --prompt "更新 README 并修复 Go 代码" \
  --workdir . \
  --mode restricted \
  --timeout 30m \
  --file-policy-json '{"allow_read":["README.md","cmd/**/*.go"],"allow_edit":["cmd/**/*.go"],"allow_create":["docs/*.md"],"allow_delete":[],"deny":[".git/**","bin/**","**/.env"]}'

# 也可以把 restricted 默认策略放进 config.json
brain config set permission_mode restricted
brain config set serve_workdir_policy confined
brain config set timeout 30m
brain config set file_policy '{"allow_read":["README.md","cmd/**/*.go"],"allow_edit":["cmd/**/*.go"],"allow_create":["docs/*.md"],"allow_delete":[],"allow_commands":true,"allow_delegate":true,"deny":[".git/**","bin/**","**/.env"]}'

# Mock 模式（无需 API Key，用于测试/CI）
brain run --provider mock --prompt "hello" --reply "hi"

# 工具管理
brain tool list                              # 列出运行时工具
brain tool list --scope chat.central.default # 查看某个 scope 下的 effective tools
brain tool describe code.read_file           # 查看工具 input/output schema
brain tool test verifier.check_output --args-json '{"actual":"ok","expected":"ok"}'  # 直接执行工具

# 配置管理
brain config path                            # 查看配置文件路径
brain config set mode solo                   # 设置配置
brain config set permission_mode restricted
brain config set serve_workdir_policy confined
brain config set timeout 30m
brain config set default_budget.max_turns 50
brain config set file_policy '{"allow_read":["README.md","cmd/**/*.go"],"allow_edit":["cmd/**/*.go"],"allow_create":["docs/*.md"],"allow_delete":[],"allow_commands":true,"allow_delegate":true,"deny":[".git/**","bin/**","**/.env"]}'
brain config set tool_profiles.safe.include code.*,central.delegate
brain config set tool_profiles.safe.exclude '*.shell_exec'
brain config set active_tools.chat.central.default safe
brain config set active_tools.delegate.code safe
brain config list                            # 列出所有配置
brain config get mode                        # 读取单个配置
brain config unset mode                      # 删除配置

# Run 管理
brain list                                   # 列出 Run
brain status <run_id>                        # 查询状态
brain cancel <run_id>                        # 取消运行
brain resume <run_id>                        # 恢复中断的 Run
brain logs <run_id>                          # 查看日志
brain replay <run_id>                        # 审计重放

# 集群模式
brain serve --listen 127.0.0.1:7701 --run-workdir-policy confined  # 启动 HTTP Kernel 服务
# 健康检查: curl http://127.0.0.1:7701/health
# 工具列表: curl http://127.0.0.1:7701/v1/tools
# 创建 Run（支持 model_config / file_policy / workdir / timeout）
curl -X POST http://127.0.0.1:7701/v1/runs \
  -H 'Content-Type: application/json' \
  -d '{
    "brain":"central",
    "prompt":"只修改 cmd 目录下的 Go 文件",
    "workdir":"./",
    "timeout":"30m",
    "model_config":{"provider":"mock"},
    "file_policy":{"allow_read":["cmd/**/*.go"],"allow_edit":["cmd/**/*.go"],"allow_create":["docs/*.md"],"allow_delete":[],"deny":["**/*.md"]}
  }'
```

`restricted` / `file_policy` 的当前语义：

- `allow_read` / `allow_create` / `allow_edit` / `allow_delete` / `deny` 分开生效
- 默认拒绝；未命中允许列表的读、建、改、删都会失败
- `allow_commands=false` 时会直接拒绝命令型工具；`allow_delegate=false` 时不会向模型暴露 `central.delegate`
- glob 支持 `**`
- `shell_exec` / `run_tests` 默认不会被一刀切禁用；它们会在受限 `workdir` 的临时隔离工作区里执行，只暴露 `allow_read` 可见文件面
- 已存在但未列入 `allow_read`、只列入 `allow_edit` / `allow_delete` 的文件不会把真实内容暴露给命令；命令只能做 blind overwrite / delete，不能借此读取原内容
- 命令结束后，只允许把命中的 create/edit/delete 变更同步回真实 `workdir`；越权改动会被丢弃并记审计事件
- `bypass-permissions` 只绕过交互确认，不绕过 `workdir` / sandbox / file policy
- `brain serve` 默认用 `confined` 请求级 workdir 策略；显式设置 `--run-workdir-policy open` 或 `serve_workdir_policy=open` 才允许请求跳出服务根目录
- sandbox 包装下的 `code.search` 若省略 `path` 或传空字符串，会默认搜索当前 `workdir`，而不是进程 `cwd`
- `brain serve` 只有在请求级校验通过并拿到并发槽位后才会持久化 run；被 4xx/429 拒绝的请求不会残留伪 `running` 记录

当前 CLI runtime 默认文件布局：

```text
~/.brain/
  config.json
  keybindings.json
  store.json
  runs.json
  artifacts/
```

说明：

- `store.json` 保存 plan/checkpoint/usage/artifact metadata
- `runs.json` 保存 run 元数据与持久化事件
- sidecar 默认优先从 `brain` 同目录查找，其次查 `PATH`
- `brain serve` 当前使用同一套 file-backed runtime；不是另一套独立数据库后端

### 作为库引用

```go
import "github.com/leef-l/brain/llm"
import "github.com/leef-l/brain/tool"
import "github.com/leef-l/brain/kernel"
import "github.com/leef-l/brain/loop"
```

## CLI 命令一览

| 命令 | 功能 | 关键 flag |
|------|------|-----------|
| `brain chat` | 交互式 REPL | `--brain`, `--provider`, `--model-config-json`, `--mode`, `--workdir`, `--file-policy-json`, `--timeout` |
| `brain run` | 启动新 Run | `--prompt`, `--provider`, `--api-key`, `--model`, `--base-url`, `--model-config-json`, `--mode`, `--workdir`, `--file-policy-json`, `--timeout`, `--stream` |
| `brain status` | 查询 Run 状态 | `--json` |
| `brain resume` | 恢复中断 Run | `--follow`, `--json` |
| `brain cancel` | 取消 Run | `--force`, `--json` |
| `brain list` | 列出 Run | `--state`, `--limit`, `--json` |
| `brain logs` | 查看 Run 日志 | `--type`, `--follow`, `--json` |
| `brain replay` | 审计重放 | `--output-dir`, `--mock-llm`, `--json` |
| `brain tool` | 工具管理 | 子命令: `list`, `describe`, `test` |
| `brain config` | 配置管理 | 子命令: `list`, `get`, `set`, `unset`, `path` |
| `brain serve` | Kernel HTTP 服务 | `--listen`, `--max-concurrent-runs`, `--mode`, `--workdir`, `--run-workdir-policy` |
| `brain doctor` | 环境诊断 | `--json` |
| `brain version` | 版本信息 | `--short`, `--json` |

### 退出码（15 个，按 27-CLI命令契约.md §18 冻结）

| 码 | 含义 |
|----|------|
| 0 | 成功 |
| 1 | Run 失败 |
| 2 | 被取消 |
| 3 | 预算耗尽 |
| 4 | 未找到 |
| 5 | 状态不允许 |
| 64 | 参数错误 (EX_USAGE) |
| 65 | 数据格式错误 (EX_DATAERR) |
| 66 | 输入不可读 (EX_NOINPUT) |
| 67 | 权限不足 (EX_NOPERM) |
| 70 | 内部错误 (EX_SOFTWARE) |
| 71 | 系统错误 (EX_OSERR) |
| 77 | 凭证缺失 |
| 130 | SIGINT (Ctrl-C) |
| 143 | SIGTERM |

## 实施进度

### 已完成

| 阶段 | 内容 | 状态 |
|------|------|------|
| 阶段 0 — 骨架 | 接口冻结，133 骨架测试通过 | ✅ |
| 阶段 1 — 能对话 | Agent Loop 执行引擎 (`loop/runner.go`, `loop/turn_executor.go`)，10 个 Runner 测试 | ✅ |
| 阶段 2 — 能调工具 | AnthropicProvider 完整实现 (463 行)，LLM → tool_use → tool_result 全链路 | ✅ |
| 阶段 3 — 能运行完整任务 | `brain run` 升级真实引擎、151 合规测试全通过、MCPAdapter (12 测试) | ✅ |
| 阶段 4 — CLI 命令树铺开 | `chat/run/status/list/cancel/resume/logs/replay/tool/config/serve/doctor/version` 均已有入口 | ✅ |
| 阶段 5 — v2 交付 | FileStore 持久化、5 个 Sidecar 二进制、Cassette 测试、流式管道 | ✅ |
| 阶段 6 — v2 生产级 | Chat REPL 对标 Claude Code、brain serve Run API、OS 级沙箱三平台 | ✅ |
| 阶段 7 — v2.1 增强 | Diff 预览 + chroma 语法高亮、交互式审批、ToolObserver 增强 | ✅ |
| 阶段 8 — v0.6.0 | Persistence Driver 抽象层、OTLP 导出器、日志脱敏、Vault Rotate/List、LLMAccess 双策略、SandboxEnforcer、License sidecar 集成 | ✅ |
| 阶段 9 — v0.7.0 | 三脑 sidecar 架构：Data Brain (9 tools) + Quant Brain (14 tools)、Bridge Tool、跨大脑授权、动态 prompt、sidecar 自动打包 | ✅ |

### 未完成

| 项目 | 说明 | 优先级 |
|------|------|--------|
| Brain Manifest | Manifest v1 解析与校验器，12 个顶层字段（docs/33） | v3 核心 |
| Brain Package | 标准目录布局、安装器、checksum 校验、签名（docs/34） | v3 核心 |
| Runtime 统一抽象 | native / mcp-backed / hybrid / remote 四种模式接口（docs/32） | v3 核心 |
| Marketplace 索引 | 大脑发现、兼容性筛选、publisher/edition 展示（docs/34） | v3 核心 |
| Capability 路由 | 能力标签、任务模式匹配、Orchestrator 按 manifest 发现大脑 | v3 核心 |
| Policy 声明层 | Manifest 声明策略需求 → Kernel 运行期校验装配 | v3 核心 |
| 跨语言 SDK | Python / TypeScript / Rust SDK（按 28-SDK交付规范） | v3 |
| RPCRunner | gRPC / 消息队列支持，大脑远程运行 | v3 |
| SQLite 持久化 | 可选高性能后端，Driver 抽象层已就绪，当前 FileStore 满足单节点需求 | v3 |
| 真实 API 集成测试 | 需真实 API Key 的端到端集成（CI 用 cassette 录制回放） | v3 |

## 构建

```bash
# 编译全部
go build ./...

# 编译 CLI 到 bin 目录
go build -o bin/brain ./cmd/

# 运行骨架测试
go test github.com/leef-l/brain/test/skeleton -v

# 运行合规测试 (150 项)
go test github.com/leef-l/brain/test/compliance -v

# 运行全部测试
go test ./...

# 静态检查
go vet ./...
```

## 规格文档

docs/ 目录下包含多篇 RFC 级规格、架构文档与实施计划，常用入口如下：

| 编号 | 文档 | 内容 |
|------|------|------|
| 02 | [BrainKernel 设计](sdk/docs/02-BrainKernel设计.md) | 内核宪法，顶层设计 |
| 20 | [协议规格](sdk/docs/20-协议规格.md) | stdio wire protocol, Content-Length framing, bidir JSON-RPC |
| 21 | [错误模型](sdk/docs/21-错误模型.md) | BrainError, 4 维 Class, Decide 决策矩阵, Fingerprint |
| 22 | [Agent Loop 规格](sdk/docs/22-Agent-Loop规格.md) | Run/Turn/ToolCall, 3 层 Prompt Cache, streaming |
| 23 | [安全模型](sdk/docs/23-安全模型.md) | 5 信任区域, 4 维沙箱, Vault, LLMAccess 三模式 |
| 24 | [可观测性](sdk/docs/24-可观测性.md) | OpenTelemetry metrics/traces/logs |
| 25 | [测试策略](sdk/docs/25-测试策略.md) | 7 层测试金字塔, cassettes, 150 合规测试 |
| 26 | [持久化与恢复](sdk/docs/26-持久化与恢复.md) | 目标架构：SQLite / MySQL 双轨、CAS、Run Resume |
| 27 | [CLI 命令契约](sdk/docs/27-CLI命令契约.md) | 当前 CLI 行为与 v1 契约，含 `chat/run/serve`、restricted、store/layout 快照 |
| 28 | [SDK 交付规范](sdk/docs/28-SDK交付规范.md) | 三级兼容性声明, 150 合规测试总览, 发布流程 |
| 29 | [第三方专精大脑开发指南](sdk/docs/29-第三方专精大脑开发.md) | Sidecar 接入、版本策略、发布与测试建议 |
| 30 | [付费专精大脑授权方案](sdk/docs/30-付费专精大脑授权方案.md) | License 文件、验签、付费 sidecar 商业化路径 |
| 31 | [Browser Brain 免费版与 Pro 版规划](sdk/docs/31-browser-brain-免费版与Pro版规划.md) | 浏览器大脑的免费/付费能力边界与工具规划 |
| 32 | [v3 Brain 架构](sdk/docs/32-v3-Brain架构.md) | Brain / Manifest / Runtime / Package / Capability 的长期架构冻结 |
| 33 | [Brain Manifest 规格](sdk/docs/33-Brain-Manifest规格.md) | v3 Brain 的稳定 schema、runtime/policy/license/health 声明面 |
| 34 | [Brain Package 与 Marketplace 规范](sdk/docs/34-Brain-Package与Marketplace规范.md) | package 布局、安装、签名、marketplace 索引与分发规则 |
| 35 | [量化系统三脑架构总览](shared/docs/35-量化系统三脑架构总览.md) | Data/Quant/Central 三脑协作、跨大脑授权、数据流 |
| 36 | [数据大脑设计](brains/data/docs/36-数据大脑设计.md) | 行情采集、192 维特征向量、Ring Buffer、sidecar 9 tools |
| 37 | [量化大脑设计](brains/quant/docs/37-量化大脑设计.md) | 策略聚合、风控、交易执行、sidecar 14 tools |
| 38 | [中央大脑量化职责](central/docs/38-中央大脑量化职责.md) | LLM 复审、日终分析、数据告警 |
| -- | [三脑系统使用指南](docs/三脑系统使用指南.md) | 部署、配置、运维指南（纸盘/生产模式） |

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
┌─────────────────────────────────────────────────────────────┐
│  brain CLI  (chat / run / serve)                            │
│  ┌────────────────┬────────────────┬──────────────────────┐ │
│  │ Bridge Tools   │ Orchestrator   │ LLM Provider         │ │
│  │ quant.* data.* │ CanDelegate    │ Anthropic/Mock       │ │
│  │ (23 tools)     │ CallTool       │                      │ │
│  └────────┬───────┴───────┬────────┴──────────────────────┘ │
│           │               │                                  │
│  BrainKernel ─────────────┤                                  │
│  ┌──────────┬─────────┬───┴──────────────────────┐          │
│  │ AgentLoop│ LLM Abst│ Tool Registry            │          │
│  │ Run/Turn │ Provider│ Schema + Guardrail        │          │
│  ├──────────┼─────────┼──────────────────────────┤          │
│  │ Security │ Persist │ Observability             │          │
│  │ Sandbox  │ Driver  │ OTel (Trace/Metrics/Log) │          │
│  └──────────┴─────────┴──────────────────────────┘          │
│           ↕ stdio JSON-RPC (sidecar 协议)                    │
├──────────┬──────────┬──────────┬──────────┬─────────────────┤
│ Central  │  Code    │ Data     │  Quant   │ Browser/Verify/ │
│ Brain    │  Brain   │ Brain    │  Brain   │ Fault Brain     │
│ 协调+复审│ 读写代码 │ 9 tools  │ 14 tools │ ...             │
│          │          │ 行情采集 │ 策略+风控│                  │
│          │          │ 特征向量 │ 交易执行 │                  │
└──────────┴──────────┴──────────┴──────────┴─────────────────┘
                        ↕ Ring Buffer (192-dim feature vector)
                   Data Brain ──→ Quant Brain
```

## License

This repository is distributed under a custom source-available license in
[`LICENSE`](./LICENSE):

- Individuals may use it free of charge for personal, non-commercial use.
- Any enterprise, company, school, government body, or other organization use
  requires a separate paid commercial license from the copyright holder.

Commercial licensing contact:

- <https://github.com/leef-l>

This is not an OSI-approved open source license.
