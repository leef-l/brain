# Brain

Brain 是一个多大脑协作的 AI Agent 系统。内置中央大脑 + 8 个专精大脑（code/browser/verifier/fault/data/quant/desktop/easymvp），通过统一的 BrainKernel 运行时协调，支持 CLI 交互、HTTP API、sidecar 架构和第三方大脑扩展。

> **🎉 MACCS v2.0 全 48 项接入完成 (100%)**: Multi-Agent Cognitive Collaboration System 架构。
>
> - **架构总览**: [`docs/README.md`](docs/README.md) — 30 分钟读完整体理解
> - **架构总纲**: [`docs/MACCS-架构总纲-v2.md`](docs/MACCS-架构总纲-v2.md)
> - **实施进度**: [`docs/MACCS-实施进度追踪.md`](docs/MACCS-实施进度追踪.md) (48/48 = 100%)
> - **SDK 设计稿**: [`sdk/docs/README.md`](sdk/docs/README.md) — 28 份子系统设计

## 快速开始

### 1. 安装

**方式 A：下载预编译二进制（推荐，无需 Go 环境）**

从 [GitHub Releases](https://github.com/leef-l/brain/releases) 下载对应平台的压缩包：

| 平台 | 文件 |
|------|------|
| Linux x64 | `brain-linux-amd64.tar.gz` |
| Linux ARM64 | `brain-linux-arm64.tar.gz` |
| macOS x64 | `brain-darwin-amd64.tar.gz` |
| macOS ARM64 (Apple Silicon) | `brain-darwin-arm64.tar.gz` |
| Windows x64 | `brain-windows-amd64.zip` |

```bash
# Linux / macOS 示例
tar xzf brain-linux-amd64.tar.gz
sudo mv brain /usr/local/bin/
sudo mv brain-*-sidecar /usr/local/bin/

# 验证安装
brain version
```

**方式 B：npm 全局安装（自动下载对应平台二进制）**

```bash
npm install -g @leef-l/brain-cli
```

**方式 C：从源码编译（需要 Go 1.25+）**

```bash
git clone https://github.com/leef-l/brain.git
cd brain

# 编译主程序
go build -o $GOPATH/bin/brain ./cmd/brain/

# 编译专精大脑 sidecar（按需）
go build -o $GOPATH/bin/brain-data-sidecar  ./brains/data/cmd/brain-data-sidecar/
go build -o $GOPATH/bin/brain-quant-sidecar ./brains/quant/cmd/brain-quant-sidecar/
```

### 2. 配置 API Key

Brain 需要 LLM API Key 才能工作。支持 Anthropic (Claude)、DeepSeek 等提供商。

```bash
# 方式 1：环境变量（最简单）
export ANTHROPIC_API_KEY="sk-ant-xxx"

# 方式 2：写入配置文件
brain config set api_key "sk-ant-xxx"

# 方式 3：使用其他提供商
brain config set providers.deepseek.base_url "https://api.deepseek.com/v1"
brain config set providers.deepseek.api_key "sk-xxx"
brain config set providers.deepseek.model "deepseek-chat"
brain config set active_provider deepseek
```

配置文件位于 `~/.brain/config.json`，可用 `brain config path` 查看路径。

### 3. 开始使用

```bash
# 交互式对话（最常用）
brain

# 等价于
brain chat
```

进入 REPL 后直接输入问题即可，Brain 会自动调用代码读写、搜索、命令执行等工具完成任务。输入 `exit` 或按 `Ctrl-C` 退出。

### 4. 验证安装

```bash
# 环境检查（检查配置、凭证、sidecar 可用性等）
brain doctor

# 无需 API Key 的测试（Mock 模式）
brain run --provider mock --prompt "hello" --reply "hi"
```

---

## 使用示例

### 单次任务

```bash
# 执行一次对话（非交互式）
brain run --prompt "读取 go.mod 文件内容"

# 指定模型
brain run --prompt "解释这段代码" --model claude-sonnet-4-20250514

# 限制执行时间
brain run --prompt "修复 cmd 下的测试" --timeout 30m

# 流式输出
brain run --prompt "写一个排序算法" --stream
```

### 交互式对话

```bash
# 默认模式
brain chat

# 指定工作目录
brain chat --workdir /path/to/project

# 受限模式（默认拒绝，显式允许）
brain chat --mode restricted \
  --file-policy-json '{
    "allow_read": ["src/**/*.go", "README.md"],
    "allow_edit": ["src/**/*.go"],
    "deny": [".git/**", "**/.env"]
  }'
```

### Run 管理

```bash
brain list                    # 列出所有 Run
brain status <run_id>         # 查询状态
brain logs <run_id>           # 查看日志
brain cancel <run_id>         # 取消运行中的 Run
brain resume <run_id>         # 恢复中断的 Run（支持对话上下文恢复）
brain replay <run_id>         # 审计重放
```

### HTTP API 模式

```bash
# 启动 HTTP 服务
brain serve --listen 127.0.0.1:7701

# 健康检查
curl http://127.0.0.1:7701/health

# 创建 Run
curl -X POST http://127.0.0.1:7701/v1/runs \
  -H 'Content-Type: application/json' \
  -d '{
    "brain": "central",
    "prompt": "读取 README.md 并总结",
    "timeout": "5m"
  }'

# 查看工具列表
curl http://127.0.0.1:7701/v1/tools
```

#### Contract Execute（同步 + SSE 流式）

**同步模式**（默认）：一次性返回完整结果。

```bash
curl -X POST http://127.0.0.1:7701/v1/contracts/execute \
  -H 'Content-Type: application/json' \
  -d '{
    "brain_kind": "easymvp",
    "contract_kind": "architect_chat",
    "context_json": {"project_name": "my-app", "tech_stack": "Go"},
    "instruction": "设计一个用户登录系统"
  }'
# 返回：{"status":"ok","summary":"...","result":{...}}
```

**SSE 流式模式**（`?stream=true`）：实时推送执行过程事件。

```bash
curl -X POST 'http://127.0.0.1:7701/v1/contracts/execute?stream=true' \
  -H 'Content-Type: application/json' \
  -d '{
    "brain_kind": "easymvp",
    "contract_kind": "architect_chat",
    "context_json": {"project_name": "my-app", "tech_stack": "Go"},
    "instruction": "设计一个用户登录系统"
  }'
```

SSE 响应格式（`Content-Type: text/event-stream`）：

```
data: {"id":"evt-1","execution_id":"exec-...","type":"execution.started","timestamp":"...","data":{"execution_id":"exec-..."}}

data: {"id":"evt-2","execution_id":"exec-...","type":"llm.content_delta","timestamp":"...","data":{"text":"首先"}}

data: {"id":"evt-3","execution_id":"exec-...","type":"agent.tool_start","timestamp":"...","data":{"tool_name":"code.write_file"}}

data: {"id":"evt-4","execution_id":"exec-...","type":"agent.tool_end","timestamp":"...","data":{"tool_name":"code.write_file","ok":true}}

data: {"id":"evt-5","execution_id":"exec-...","type":"execution.done","timestamp":"...","data":{"status":"ok","summary":"..."}}
```

**事件类型说明**：

| 事件类型 | 来源 | 说明 |
|----------|------|------|
| `execution.started` | Brain | 执行开始 |
| `llm.message_start` | Brain | LLM 开始生成 |
| `llm.content_delta` | Brain/Sidecar | 文本/token 增量 |
| `llm.thinking_delta` | Brain | 推理过程增量（DeepSeek 等） |
| `llm.tool_call_delta` | Brain/Sidecar | 工具调用参数增量 |
| `llm.message_end` | Brain | LLM 完成，携带 usage |
| `agent.tool_start` | Sidecar | 工具开始执行 |
| `agent.tool_end` | Sidecar | 工具执行完成 |
| `execution.done` | Brain | 执行完成，携带最终结果 |
| `execution.error` | Brain | 执行出错 |
| `execution.cancelled` | Brain | 执行被取消（客户端断开 SSE） |

**客户端断开 = 取消执行**：关闭 SSE 连接会触发取消链路，Brain 会中断正在进行的 LLM 调用并清理状态。

### 工具管理

```bash
brain tool list                              # 列出所有可用工具
brain tool list --scope chat.central.default # 查看特定场景下的工具
brain tool describe code.read_file           # 查看工具详情
brain tool test verifier.check_output \
  --args-json '{"actual":"ok","expected":"ok"}'  # 直接测试工具
```

### 配置管理

```bash
brain config path             # 查看配置文件路径
brain config list             # 列出所有配置
brain config get mode          # 读取配置
brain config set mode solo     # 设置配置
brain config unset mode        # 删除配置

# 常用配置
brain config set permission_mode restricted
brain config set timeout 30m
brain config set default_budget.max_turns 50
```

---

## 配置说明

### 文件布局

```
~/.brain/
├── config.json          # 主配置文件
├── brain.db             # SQLite 持久化（Run 记录、checkpoint、审计日志）
├── keybindings.json     # 快捷键配置
├── artifacts/           # CAS 存储（对话历史、工具快照等）
└── *.example.yaml       # 各大脑配置示例（brain doctor 自动生成）
```

### 主配置文件字段

| 字段 | 说明 | 默认值 |
|------|------|--------|
| `mode` | 运行模式 | `solo` |
| `default_brain` | 默认大脑 | `central` |
| `api_key` | LLM API Key | 读取 `ANTHROPIC_API_KEY` 环境变量 |
| `base_url` | LLM API 地址 | Anthropic 默认 |
| `model` | 默认模型 | `claude-sonnet-4-20250514` |
| `timeout` | 每轮超时 | `0`（无超时） |
| `permission_mode` | 权限模式 | `default` |
| `log_level` | 日志级别 | `info` |
| `active_provider` | 激活的 LLM 提供商 | 空（使用顶层 api_key） |

### 权限模式

| 模式 | 说明 |
|------|------|
| `default` | 写操作需要交互确认 |
| `plan` | 先出计划，确认后执行 |
| `accept-edits` | 自动接受文件编辑，命令仍需确认 |
| `auto` | 自动执行所有操作 |
| `restricted` | 默认拒绝，需显式配置 `file_policy` 白名单 |
| `bypass-permissions` | 跳过交互确认（不绕过沙箱和文件策略） |

### 多 LLM 提供商

```bash
# 注册 DeepSeek
brain config set providers.deepseek.base_url "https://api.deepseek.com/v1"
brain config set providers.deepseek.api_key "sk-xxx"
brain config set providers.deepseek.model "deepseek-chat"

# 按大脑指定不同模型
brain config set providers.deepseek.models.code "deepseek-coder"
brain config set providers.deepseek.models.central "deepseek-chat"

# 切换激活提供商
brain config set active_provider deepseek
```

### 文件策略（restricted 模式）

```bash
brain config set file_policy '{
  "allow_read": ["src/**/*.go", "docs/**/*.md"],
  "allow_create": ["docs/*.md"],
  "allow_edit": ["src/**/*.go"],
  "allow_delete": [],
  "deny": [".git/**", "bin/**", "**/.env"],
  "allow_commands": true,
  "allow_delegate": true
}'
```

- 默认拒绝，未命中允许列表的操作会失败
- glob 支持 `**` 递归匹配
- `allow_commands=false` 禁用命令型工具
- `allow_delegate=false` 禁止 `central.delegate` 跨大脑调用

---

## 大脑架构

```
┌─────────────────────────────────────────────────────┐
│  brain CLI  (chat / run / serve)                     │
│  ┌──────────────────────────────────────────────────┐│
│  │  BrainKernel                                     ││
│  │  AgentLoop + LLM Provider + Tool Registry        ││
│  │  Security + Persistence + Observability          ││
│  └──────────────────────┬───────────────────────────┘│
│                         │ stdio JSON-RPC              │
├────────┬────────┬───────┼───────┬──────────┬─────────┤
│Central │ Code   │ Data  │ Quant │ Browser  │Verifier │
│协调+复审│读写代码│行情采集│策略+风控│CDP 自动化│只读验证  │
│        │        │特征计算│交易执行│          │         │
└────────┴────────┴───────┴───────┴──────────┴─────────┘
```

| 大脑 | 二进制 | 说明 |
|------|--------|------|
| Central | `brain-central`（内置） | 协调器，LLM 复审，七阶段闭环编排 |
| Code | `brain-code-sidecar` | 代码读写、shell 执行、测试 |
| Browser | `brain-browser-sidecar` | CDP 自动化、UI 模式学习、异常感知 |
| Data | `brain-data` + `brain-data-sidecar`（双模式） | 行情采集、特征工程、回放 |
| Quant | `brain-quant` + `brain-quant-sidecar`（双模式） | 策略聚合、风控、交易执行 |
| Verifier | `brain-verifier-sidecar` | 只读审核、测试、规则验证 |
| Fault | `brain-fault-sidecar` | 故障诊断与混沌注入 |
| Desktop | `brain-desktop-sidecar` | 桌面自动化 |
| EasyMVP | `brain-easymvp-sidecar` | 端到端 MVP 闭环 |

> **工具列表权威源**：每个大脑的 `brains/<kind>/brain.json`。`brain.json.capabilities` 是该大脑声明的全部能力标签，`brain tool list --scope <brain>` 可查看运行时实际暴露的工具集。

所有基础大脑（code/browser/verifier/fault）共享同一套工业级 Agent Loop 引擎（`sdk/loop.Runner`）：

- **死循环检测**（LoopDetector）：自动发现 tool+args 重复调用，中止无进展循环
- **5 维预算**（Budget）：MaxTurns / MaxCostUSD / MaxLLMCalls / MaxToolCalls / MaxDuration
- **Prompt Cache**：L1 system block 自动 cache_control，长任务 token 成本下降 50-90%
- **消息压缩**（MessageCompressor）：长会话超预算时自动压缩
- **工具结果卫生化**（MemSanitizer）：prompt injection / 二进制 / BIDI / PII 防护
- **跨脑上下文注入**：central ContextEngine 装配的上下文注入到专精大脑对话起始
- **TODO 规划**：每个大脑都有 `<kind>.note` 工具做多步任务 scratchpad

### 三脑量化系统

Data + Quant + Central 三个大脑协同进行加密货币量化交易：

```
Data Brain ──→ Quant Brain ←──→ Central Brain
  行情采集        策略+执行         LLM 复审
  特征计算        风控+交易         日终分析
```

详见各大脑独立文档：[`brains/data/docs/`](brains/data/docs/) / [`brains/quant/docs/`](brains/quant/docs/) / [`shared/docs/35-量化系统三脑架构总览.md`](shared/docs/35-量化系统三脑架构总览.md)。

---

## CLI 命令一览

| 命令 | 功能 | 关键 flag |
|------|------|-----------|
| `brain chat` | 交互式 REPL | `--brain`, `--mode`, `--workdir`, `--timeout` |
| `brain run` | 启动新 Run | `--prompt`, `--model`, `--stream`, `--timeout` |
| `brain status` | 查询 Run 状态 | `--json` |
| `brain resume` | 恢复中断 Run | `--follow`, `--json` |
| `brain cancel` | 取消 Run | `--force`, `--json` |
| `brain list` | 列出 Run | `--state`, `--limit`, `--json` |
| `brain logs` | 查看日志 | `--type`, `--follow`, `--json` |
| `brain replay` | 审计重放 | `--output-dir`, `--json` |
| `brain tool` | 工具管理 | 子命令: `list`, `describe`, `test` |
| `brain config` | 配置管理 | 子命令: `list`, `get`, `set`, `unset`, `path`, `init` |
| `brain serve` | HTTP API 服务 | `--listen`, `--max-concurrent-runs` |
| `brain brain` | 已安装大脑管理 | 子命令: `list`, `install`, `activate`, `deactivate`, `upgrade`, `rollback` |
| `brain pattern` | UI 模式库管理 | 子命令: `list`, `delete`, `import`, `export` |
| `brain demo` | 人类示范序列管理 | 子命令: `list`, `approve`, `delete`, `purge` |
| `brain doctor` | 环境诊断 | `--json` |
| `brain version` | 版本信息 | `--short`, `--json` |

> 共 **16 个顶级子命令**，详见 [`sdk/docs/27-CLI命令契约.md`](sdk/docs/27-CLI命令契约.md)。

### 退出码

| 码 | 含义 |
|----|------|
| 0 | 成功 |
| 1 | Run 失败 |
| 2 | 被取消 |
| 3 | 预算耗尽 |
| 4 | 未找到 |
| 5 | 状态不允许 |
| 64 | 参数错误 |
| 70 | 内部错误 |
| 77 | 凭证缺失 |
| 130 | SIGINT (Ctrl-C) |

---

## 第三方大脑开发

Brain 支持通过 Manifest + Sidecar 协议接入第三方专精大脑。

```bash
# 使用模板创建新大脑
cp -r sdk/template my-brain

# 实现 BrainHandler 接口
# 编写 brain.yaml manifest
# 打包发布

brain install ./my-brain.brainpkg
```

详见 [第三方专精大脑开发指南](sdk/docs/29-第三方专精大脑开发.md)。

---

## 作为 Go 库引用

```go
import (
    "github.com/leef-l/brain/sdk/llm"
    "github.com/leef-l/brain/sdk/tool"
    "github.com/leef-l/brain/sdk/kernel"
    "github.com/leef-l/brain/sdk/loop"
)
```

---

## 从源码构建

```bash
# 编译全部
go build ./...

# 运行全部测试
go test ./...

# 静态检查
go vet ./...

# 发布构建（多平台）
./scripts/release/build-assets.sh 0.8.0 ./dist
```

支持平台：`linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64`、`windows/arm64`、`freebsd/amd64`。

---

## 包结构

```
brain/
├── cmd/brain/              CLI 主程序（16 个子命令）
├── sdk/                    SDK 核心库（28 子包）
│   ├── kernel/             编排核心：Orchestrator + PlanOrchestrator + ClosedLoopController
│   ├── loop/               AgentLoop 执行引擎（Run/Turn/Budget/Compress）
│   ├── llm/                LLM Provider 抽象（Anthropic / OpenAI / Mock）
│   ├── tool/               工具注册与执行（含 Sandbox + UI Pattern）
│   ├── sidecar/            Sidecar 端 RPC + Streaming
│   ├── protocol/           JSON-RPC 2.0（stdio / HTTP / WebSocket）
│   ├── persistence/        持久化（SQLite WAL）
│   ├── security/           沙箱 + Vault + 审计
│   ├── events/             EventBus + SSE
│   ├── observability/      Span / Trace
│   ├── flow/               Workflow + Edge
│   ├── license/            Ed25519 签名 + Marketplace 授权
│   └── runtimeaudit/ executionpolicy/ toolguard/ toolpolicy/ ...
├── brains/                 8 个专精大脑
│   ├── browser/  code/  data/  desktop/
│   └── easymvp/  fault/  quant/  verifier/
├── central/                中央大脑（协调 + 7 阶段闭环）
├── docs/                   MACCS 顶级文档（权威入口 docs/README.md）
├── scripts/release/        发布打包脚本
└── sdk/docs/               SDK 规格文档（28 份）
```

---

## 规格文档

⭐ **从这里开始**：[`docs/README.md`](docs/README.md) — 单一权威入口（9 节，30 分钟读完整体理解 brain v3）。

| 主题 | 文档 |
|------|------|
| **架构总览** | [`sdk/docs/32-v3-Brain架构.md`](sdk/docs/32-v3-Brain架构.md) |
| **核心规范** | [`02-BrainKernel`](sdk/docs/02-BrainKernel设计.md) · [`20-协议`](sdk/docs/20-协议规格.md) · [`21-错误模型`](sdk/docs/21-错误模型.md) · [`23-安全`](sdk/docs/23-安全模型.md) · [`26-持久化`](sdk/docs/26-持久化与恢复.md) · [`27-CLI`](sdk/docs/27-CLI命令契约.md) |
| **第三方接入** | [`29-第三方专精大脑开发`](sdk/docs/29-第三方专精大脑开发.md) · [`33-Manifest`](sdk/docs/33-Brain-Manifest规格.md) · [`34-Package`](sdk/docs/34-Brain-Package与Marketplace规范.md) · [`37-远程调用`](sdk/docs/37-远程专精大脑调用说明.md) |
| **编排与并发** | [`35-BrainPool`](sdk/docs/35-BrainPool实现设计.md) · [`35-LeaseManager`](sdk/docs/35-LeaseManager实现设计.md) · [`35-Dispatch-Policy`](sdk/docs/35-Dispatch-Policy-冲突图与Batch分组算法.md) |
| **智能层** | [`35-Context-Engine`](sdk/docs/35-Context-Engine详细设计.md) · [`35-学习算法`](sdk/docs/35-自适应学习L1-L3算法设计.md) · [`35-跨脑通信`](sdk/docs/35-跨脑通信协议设计.md) · [`35-能力匹配`](sdk/docs/35-BrainCapability标签与匹配算法.md) |
| **MACCS 进度** | [`docs/MACCS-架构总纲-v2.md`](docs/MACCS-架构总纲-v2.md) · [`docs/MACCS-实施进度追踪.md`](docs/MACCS-实施进度追踪.md) (48/48 = 100%) · [`docs/MACCS-实施路线图.md`](docs/MACCS-实施路线图.md) |
| **理论基础** | [`钱学森工程控制论-设计原则`](sdk/docs/钱学森工程控制论-设计原则.md) · [`docs/工程控制论-简体/`](docs/工程控制论-简体/) |

完整索引：[`sdk/docs/README.md`](sdk/docs/README.md) (28 份子系统设计稿)。

---

## 常见问题

**Q: 运行 `brain chat` 提示 "no API key configured"**

设置 API Key：`export ANTHROPIC_API_KEY="sk-ant-xxx"` 或 `brain config set api_key "sk-ant-xxx"`。

**Q: 找不到 sidecar 二进制**

运行 `brain doctor` 检查 sidecar 可用性。sidecar 默认从 `brain` 同目录或 `PATH` 中查找。

**Q: 如何在不消耗 API 额度的情况下测试？**

使用 Mock 模式：`brain run --provider mock --prompt "test" --reply "ok"`。

**Q: 如何限制 Brain 的文件访问范围？**

使用 restricted 模式 + file_policy：
```bash
brain chat --mode restricted \
  --file-policy-json '{"allow_read":["src/**"],"allow_edit":["src/**"],"deny":["**/.env"]}'
```

**Q: 支持哪些 LLM 提供商？**

内置支持 Anthropic (Claude) 和任何 OpenAI 兼容 API（DeepSeek、HunYuan 等），通过 `providers` 配置切换。

**Q: 如何升级？**

```bash
# npm 安装方式
npm update -g @leef-l/brain-cli

# 二进制方式：从 Releases 下载新版本覆盖
# 源码方式
git pull && go build -o $GOPATH/bin/brain ./cmd/brain/
```

---

## License

Apache License 2.0 — 详见 [LICENSE](./LICENSE)。
