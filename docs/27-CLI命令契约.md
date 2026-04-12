# 27 · CLI 命令契约 v1

> **状态**：Frozen · v1.0 · 2026-04-11
> **上位规格**：[02-BrainKernel设计.md](./02-BrainKernel设计.md) §12 执行器架构
> **依赖**：
> - [20-协议规格.md](./20-协议规格.md)（stdio 线缆协议）
> - [21-错误模型.md](./21-错误模型.md)（退出码与错误输出）
> - [26-持久化与恢复.md](./26-持久化与恢复.md)（solo/cluster 双轨存储）

## 目录

- [1. 动机与范围](#1-动机与范围)
- [2. 术语](#2-术语)
- [3. 顶层命令 `brain`](#3-顶层命令-brain)
- [4. 全局选项](#4-全局选项)
- [5. 子命令总览](#5-子命令总览)
- [6. `brain run` · 启动一次 Run](#6-brain-run--启动一次-run)
- [7. `brain status` · 查询 Run 状态](#7-brain-status--查询-run-状态)
- [8. `brain resume` · 恢复中断的 Run](#8-brain-resume--恢复中断的-run)
- [9. `brain cancel` · 取消 Run](#9-brain-cancel--取消-run)
- [10. `brain list` · 列出 Run](#10-brain-list--列出-run)
- [11. `brain logs` · 查看 Run 日志](#11-brain-logs--查看-run-日志)
- [12. `brain replay` · 重放 Run（审计）](#12-brain-replay--重放-run审计)
- [13. `brain tool` · 工具管理](#13-brain-tool--工具管理)
- [14. `brain config` · 配置管理](#14-brain-config--配置管理)
- [15. `brain serve` · 启动 Kernel 服务（cluster 模式）](#15-brain-serve--启动-kernel-服务cluster-模式)
- [16. `brain doctor` · 环境诊断](#16-brain-doctor--环境诊断)
- [17. `brain version` · 版本信息](#17-brain-version--版本信息)
- [18. 退出码规范](#18-退出码规范)
- [19. 输出格式规范](#19-输出格式规范)
- [20. stdin 输入协议](#20-stdin-输入协议)
- [21. 环境变量](#21-环境变量)
- [22. 工作目录与文件布局](#22-工作目录与文件布局)
- [23. 信号处理](#23-信号处理)
- [24. 向后兼容策略](#24-向后兼容策略)
- [25. 合规测试矩阵 C-CLI-\*](#25-合规测试矩阵-c-cli-)
- [附录 A · 完整命令速查](#附录-a--完整命令速查)
- [附录 B · JSON 输出 schema 清单](#附录-b--json-输出-schema-清单)

---

## 1. 动机与范围

### 1.1 为什么要把 CLI 写成独立规格

02-BrainKernel设计.md 定义了 Kernel 的 Go 接口和 stdio 线缆协议，但对"人怎么用它"只字未提。**CLI 是 Kernel 对人的唯一稳定接口**——运维、集成方、调试人员、CI 流水线全都通过 CLI 操作 Kernel。

CLI 一旦发布就必须长期稳定。命令名改一个字符、退出码换一个数字、`--flag` 改成 `--option`，都会导致下游脚本/CI/管道大规模失败。所以 CLI 的稳定性要求比 Kernel 内部 API 更严格。

本文档把 CLI 的**每个命令、每个参数、每个退出码、每一行 JSON 输出**全部冻结为 v1 契约，第三方 SDK 实现 CLI 时必须与本规格一致，否则不能声称兼容 BrainKernel v1。

### 1.2 范围

**本规格定义**：

- `brain` 顶层命令的命令树
- 每个子命令的参数、选项、stdin、stdout、stderr、退出码
- 两种输出格式（human / json）的字段
- solo 和 cluster 两种运行模式的 CLI 行为差异
- 环境变量、配置文件、工作目录布局
- 信号处理与优雅关闭
- C-CLI-01 ~ C-CLI-20 共 20 条合规测试

**本规格不定义**：

- CLI 内部实现（Cobra / Clap / Click 等框架选型留给各语言 SDK）
- TUI 交互界面（v1 只冻结非交互命令行行为；TUI 是 UX 层，未来独立规格）
- 图形化管理界面（是 EasyMVP 前端的范畴，不属于 BrainKernel 规格）

### 1.3 两种运行模式

| 模式 | 场景 | Kernel 位置 | 存储 | 命令行为差异 |
|------|------|-------------|------|--------------|
| **solo** | 开发机 / CLI 单机 / CI | CLI 进程内嵌入式 | SQLite WAL（`~/.brain/brain.db`） | 无需 `brain serve`，所有命令直接起嵌入式 Kernel |
| **cluster** | 生产环境 / 多节点 | 独立进程（`brain serve`） | MySQL | CLI 通过 `--endpoint` 连到远端 Kernel，等价于 HTTP/gRPC 客户端 |

**模式切换**：
- 通过 `BRAIN_MODE` 环境变量或 `brain config set mode <solo|cluster>`
- 未设置时默认 `solo`
- `brain` 每次启动时根据 mode 决定是嵌入式跑还是连远端

---

## 2. 术语

| 术语 | 定义 |
|------|------|
| **Run** | 一次完整的执行实例，由 `brain run` 创建，生命周期见 22-Agent-Loop 规格 |
| **Turn** | Run 内的一次完整 LLM 交互（prompt + response），见 22-Agent-Loop 规格 |
| **Brain** | sidecar 进程实现的具体 agent 角色，见 02 §12.5 |
| **Tool** | Brain 可调用的能力单元，见 02 §6 |
| **Endpoint** | cluster 模式下 CLI 连接的远端 Kernel 服务地址 |
| **Workspace** | Run 的工作目录，默认 `~/.brain/runs/<run_id>/workspace/` |
| **solo** | CLI 内嵌 Kernel 的单机模式 |
| **cluster** | CLI 作为远端 Kernel 客户端的分布式模式 |

---

## 3. 顶层命令 `brain`

### 3.1 命令名

**MUST** 使用 `brain` 作为顶层命令名。不得使用 `bk` / `brainkernel` / `brainctl` 等别名作为 v1 官方入口。

第三方 SDK 如果要提供 CLI，**MUST** 命名为 `brain`（可放在独立二进制中），这样不同 SDK 实现的 CLI 对用户是无差别的。如果 SDK 希望提供增强功能，**SHOULD** 以 `brain-<subcommand>` 的独立二进制方式扩展（类似 `git-lfs`），而不是覆盖 `brain` 本身。

### 3.2 命令结构

```
brain [global-options] <command> [command-options] [args...]
```

- `global-options` MUST 可以出现在 `<command>` 之前或之后（由实现保证）
- `<command>` MUST 是第 5 节定义的子命令之一
- 未知子命令 MUST 返回退出码 `64`（EX_USAGE）并在 stderr 打印 usage 提示

### 3.3 `brain` 不带参数

直接运行 `brain` MUST 等价于 `brain help`，打印顶层 usage 并以退出码 `0` 返回。

### 3.4 `brain help [command]`

- `brain help` 打印顶层 usage（列出所有子命令）
- `brain help <command>` 打印指定子命令的详细用法
- 退出码 `0`

---

## 4. 全局选项

下列选项 MUST 被所有子命令识别（即使某些子命令不使用）：

| 选项 | 短形式 | 类型 | 默认 | 说明 |
|------|--------|------|------|------|
| `--mode` | — | `solo`\|`cluster` | `solo` | 运行模式 |
| `--endpoint` | `-e` | URL | `http://127.0.0.1:7701` | cluster 模式下 Kernel 地址 |
| `--config` | `-c` | path | `~/.brain/config.yaml` | 配置文件路径 |
| `--workspace` | `-w` | path | `~/.brain/` | 工作目录根 |
| `--output` | `-o` | `human`\|`json` | `human` | 输出格式 |
| `--verbose` | `-v` | count | 0 | 日志级别（`-v` info / `-vv` debug / `-vvv` trace） |
| `--quiet` | `-q` | bool | false | 只输出结果，不输出进度 |
| `--no-color` | — | bool | false | 禁用 ANSI 颜色（stderr 检测 `NO_COLOR` 也生效） |
| `--timeout` | — | duration | `30s` | CLI 到 Kernel 的单次 RPC 超时（不影响 Run 本身的 budget） |
| `--help` | `-h` | bool | false | 打印 help 并退出 `0` |
| `--version` | `-V` | bool | false | 打印版本号并退出 `0` |

**优先级**（高到低）：

1. 命令行选项
2. 环境变量（见 §21）
3. 配置文件（`--config`）
4. 编译内默认值

---

## 5. 子命令总览

| 命令 | 作用 | 读/写 | 网络 |
|------|------|-------|------|
| `run` | 启动一次新 Run | 写 | LLM + tool |
| `status` | 查询 Run 状态 | 读 | — |
| `resume` | 恢复暂停/崩溃的 Run | 写 | LLM + tool |
| `cancel` | 取消运行中的 Run | 写 | — |
| `list` | 列出 Run | 读 | — |
| `logs` | 查看 Run 的 trace 日志 | 读 | — |
| `replay` | 重放已结束的 Run（审计） | 读 | — |
| `tool` | 工具管理（注册/列出/删除） | 读写 | — |
| `config` | 配置管理（get/set/list） | 读写 | — |
| `serve` | 启动 Kernel 服务（cluster 模式） | 写 | 监听 |
| `doctor` | 环境诊断 | 读 | — |
| `version` | 版本信息 | 读 | — |
| `help` | 帮助 | 读 | — |

---

## 6. `brain run` · 启动一次 Run

### 6.1 签名

```
brain run [options] [prompt]
```

### 6.2 参数

- `prompt` *(位置参数，可选)*：字符串形式的初始任务描述。如果省略：
  - stdin 是 TTY → 从 `$EDITOR` 打开编辑器让用户输入
  - stdin 是 pipe → 从 stdin 读取直到 EOF（见 §20）
  - stdin 是 pipe 但为空 → 退出码 `64`

### 6.3 选项

| 选项 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `--brain` | string | `central` | 指定入口 brain（必须是已注册的 BrainKind） |
| `--workspace-dir` | path | 自动创建 | 指定 workspace，若不存在则创建 |
| `--model` | string | 跟随 brain 默认 | 指定 LLM 模型 |
| `--max-turns` | int | 跟随 brain 默认 | Run 最大 Turn 数（Budget） |
| `--max-cost-usd` | float | 跟随 brain 默认 | Run 最大成本预算（USD） |
| `--tool` | string（可多次） | — | 限制可用工具白名单 |
| `--param` | `key=value`（可多次） | — | 额外参数传给入口 brain |
| `--tag` | string（可多次） | — | 给 Run 打标签（用于 list 过滤） |
| `--wait` | bool | `true` | 是否等待 Run 完成后再退出 |
| `--follow` / `-f` | bool | `false` | 实时流式输出（隐含 `--wait`） |
| `--detach` / `-d` | bool | `false` | 立即返回 run_id，等价于 `--wait=false` |
| `--idempotency-key` | string | 自动生成 | 重复调用时复用相同 Run（防止重复提交） |

### 6.4 行为

1. 读取配置，连接 Kernel（solo 模式启动嵌入式，cluster 模式连 `--endpoint`）
2. 检查 `--idempotency-key` 是否已对应一个 Run；若存在且未完成，返回现有 Run
3. 创建 Run 记录（`mvp_run_checkpoint` + `mvp_brain_plan`）
4. 启动入口 brain sidecar，发送 `initialize` 帧
5. 根据 `--wait` / `--follow` / `--detach` 选择后续行为：
   - `--detach`：立刻打印 run_id，退出 `0`
   - `--wait`（默认）：阻塞直到 Run 结束，打印最终结果
   - `--follow`：阻塞 + 实时流式打印每个 Turn 和 content.delta

### 6.5 输出（human 模式）

```
✓ Created run: r_01HX9K8M2ZABCDEFG
  workspace: /home/user/.brain/runs/r_01HX9K8M2ZABCDEFG/workspace
  brain: central (model: claude-sonnet-4-6)
  budget: 50 turns, $5.00

[Turn 1] central > thinking...
[Turn 1] central > plan_store.create: "Build REST API..."
[Turn 2] code_brain > file.write: src/main.go
...
[Turn 8] central > completed

✓ Run finished: completed
  turns: 8
  cost: $1.23
  duration: 4m 12s
```

### 6.6 输出（json 模式，`--detach`）

```json
{
  "run_id": "r_01HX9K8M2ZABCDEFG",
  "state": "running",
  "brain": "central",
  "model": "claude-sonnet-4-6",
  "workspace": "/home/user/.brain/runs/r_01HX9K8M2ZABCDEFG/workspace",
  "budget": {
    "max_turns": 50,
    "max_cost_usd": 5.0
  },
  "created_at": "2026-04-11T13:00:00Z"
}
```

### 6.7 输出（json 模式，`--wait` 完成后）

```json
{
  "run_id": "r_01HX9K8M2ZABCDEFG",
  "state": "completed",
  "final_message": "...",
  "turns": 8,
  "cost_usd": 1.23,
  "input_tokens": 45000,
  "output_tokens": 12000,
  "duration_seconds": 252,
  "started_at": "2026-04-11T13:00:00Z",
  "ended_at": "2026-04-11T13:04:12Z"
}
```

### 6.8 输出（json 模式，`--follow` 流式）

每行一个 JSON 对象（NDJSON），事件类型与 22-Agent-Loop §5 一致：

```
{"type":"run.created","run_id":"r_01HX9K8M2ZABCDEFG","ts":"..."}
{"type":"turn.started","run_id":"...","turn_index":0,"brain":"central","ts":"..."}
{"type":"content.delta","run_id":"...","turn_index":0,"text":"I'll start by..."}
{"type":"tool_call.delta","run_id":"...","turn_index":0,"tool":"plan_store.create","args_partial":"{\"title\":..."}
{"type":"message.end","run_id":"...","turn_index":0,"ts":"..."}
{"type":"turn.completed","run_id":"...","turn_index":0,"ts":"..."}
...
{"type":"run.completed","run_id":"...","final":{...},"ts":"..."}
```

**注意**：CLI 的 `--follow` NDJSON 输出是 BrainKernel 线缆协议 Content-Length 帧的**用户可读封装**，不是同一种协议。第三方 SDK 实现 CLI 时 MUST 用 NDJSON（`\n` 分隔），不得直接把线缆协议暴露给 stdout。

### 6.9 退出码

- `0` · Run 成功完成（state=`completed`）
- `1` · Run 失败（state=`failed`）
- `2` · Run 被取消（state=`canceled`）
- `3` · Run 超出预算（Budget exhausted）
- `64` · 参数错误
- `65` · 配置错误
- `70` · Kernel 通信失败（solo 启动失败 / cluster 连不上）
- `130` · 收到 SIGINT 后中断（同时 Run 被标记为 `paused`，可 `brain resume`）

---

## 7. `brain status` · 查询 Run 状态

### 7.1 签名

```
brain status <run_id>
brain status --all [--state <state>] [--since <duration>]
```

### 7.2 行为

- 单 run：查询指定 run_id 的当前状态，若不存在退出码 `4`
- `--all`：列出所有 Run 的状态摘要（等价于 `brain list`，但默认只显示活跃 Run）

### 7.3 输出（json，单 run）

```json
{
  "run_id": "r_01HX9K8M2ZABCDEFG",
  "state": "running",
  "brain": "central",
  "current_turn": 5,
  "max_turns": 50,
  "cost_usd": 0.82,
  "max_cost_usd": 5.0,
  "started_at": "2026-04-11T13:00:00Z",
  "last_activity_at": "2026-04-11T13:02:15Z",
  "checkpoint": {
    "turn_uuid": "t_8834abcd...",
    "turn_index": 5,
    "state": "waiting_tool"
  }
}
```

### 7.4 退出码

- `0` · 查询成功
- `4` · run 不存在
- `64` · 参数错误
- `70` · Kernel 通信失败

---

## 8. `brain resume` · 恢复中断的 Run

### 8.1 签名

```
brain resume <run_id> [--follow]
```

### 8.2 行为

- 按 26-持久化与恢复 §7 的 Run Resume 协议恢复
- 只允许恢复处于 `paused` 或 `crashed` 状态的 Run
- 若 Run 已经 `completed` / `failed` / `canceled`，退出码 `5`
- 恢复后的行为与 `brain run` 一致（可用 `--follow` 流式观察）

### 8.3 退出码

- `0/1/2/3` · 同 `brain run`
- `4` · run 不存在
- `5` · run 状态不允许恢复（例如已 completed）

---

## 9. `brain cancel` · 取消 Run

### 9.1 签名

```
brain cancel <run_id> [--force]
```

### 9.2 行为

- 向 Kernel 发送取消信号
- 默认**优雅取消**：等待当前 Turn 结束，保存 checkpoint，状态置 `canceled`
- `--force`：立即 kill sidecar，不保存 checkpoint（危险，可能丢数据）
- 等待取消完成后返回（最多等待 `--timeout`）

### 9.3 退出码

- `0` · 取消成功
- `4` · run 不存在
- `5` · run 状态不允许取消（例如已 completed）
- `70` · Kernel 通信失败

---

## 10. `brain list` · 列出 Run

### 10.1 签名

```
brain list [--state <state>] [--tag <tag>] [--since <duration>] [--limit <n>]
```

### 10.2 选项

| 选项 | 说明 |
|------|------|
| `--state` | 过滤状态（`running`/`completed`/`failed`/`paused`/`canceled`/`all`，默认 `all`） |
| `--tag` | 按 `--tag` 过滤 |
| `--since` | 只显示 N 时间内的（如 `24h` / `7d`） |
| `--limit` | 最多返回 N 条，默认 50 |
| `--sort` | `created`（默认）/ `updated` / `cost` |
| `--reverse` | 倒序 |

### 10.3 输出（human）

```
RUN ID               STATE      BRAIN    TURNS  COST     STARTED           DURATION
r_01HX9K8M2ZABC...   completed  central    8   $1.23   2026-04-11 13:00  4m12s
r_01HX8Y7M1YABD...   running    central    3   $0.34   2026-04-11 12:45  2m01s
r_01HX7X6L0XABE...   failed     central   12   $2.01   2026-04-11 12:20  6m30s
```

### 10.4 输出（json）

```json
{
  "runs": [
    {"run_id": "...", "state": "completed", ...},
    {"run_id": "...", "state": "running", ...}
  ],
  "total": 47,
  "returned": 3
}
```

---

## 11. `brain logs` · 查看 Run 日志

### 11.1 签名

```
brain logs <run_id> [--follow] [--from <turn>] [--to <turn>] [--type <type>]
```

### 11.2 选项

| 选项 | 说明 |
|------|------|
| `--follow` / `-f` | 流式 tail（只对 running Run 有意义） |
| `--from` | 起始 Turn 索引 |
| `--to` | 结束 Turn 索引 |
| `--type` | 过滤事件类型（`llm`/`tool`/`trace`/`audit`/`all`） |

### 11.3 输出（human）

```
[Turn 0] 13:00:00 central.llm_call    model=claude-sonnet-4-6 in=1200 out=450 cost=$0.023
[Turn 0] 13:00:03 central.tool        plan_store.create ✓ 45ms
[Turn 1] 13:00:04 central.llm_call    model=claude-sonnet-4-6 in=2100 out=890 cost=$0.041
[Turn 1] 13:00:12 central.tool        file.write(src/main.go) ✓ 120ms
...
```

### 11.4 输出（json）

NDJSON，每行一个事件（字段见 11-trace 事件结构）。

---

## 12. `brain replay` · 重放 Run（审计）

### 12.1 签名

```
brain replay <run_id> [--output-dir <path>] [--mock-llm] [--mock-tools]
```

### 12.2 行为

- 从 `mvp_run_checkpoint` + `mvp_brain_plan` + `mvp_brain_plan_delta` 恢复 Run 的完整历史
- 按原始顺序打印每个 Turn 的 prompt / response / tool_call / tool_result
- `--mock-llm`：不再调 LLM，只从 trace log 读回历史响应（cassette replay）
- `--mock-tools`：不执行工具，只显示调用记录

Replay 是纯只读操作，不修改数据库，也不消耗 LLM 额度。

### 12.3 用途

- 审计追溯（监管检查、事故复盘）
- Bug 定位（在开发机复现线上 Run）
- 合规测试（cassette 录制见 25-测试策略 §7）

---

## 13. `brain tool` · 工具管理

### 13.1 子命令

```
brain tool list [--brain <brain>]
brain tool describe <tool_name>
brain tool test <tool_name> [--args-json <json>]
```

### 13.2 `brain tool list`

列出当前 Kernel 注册的所有工具：

```
NAME                    BRAIN     KIND     RISK   DESCRIPTION
file.read               built-in  readonly safe   Read a file from workspace
file.write              built-in  mutable  med    Write file (requires fs.write permission)
shell.exec              built-in  mutable  high   Execute shell command (sandboxed)
plan_store.create       central   mutable  safe   Create a new plan
...
```

### 13.3 `brain tool describe`

打印指定工具的 JSON schema（参数、返回值、权限要求、风险等级）。

### 13.4 `brain tool test`

在 CLI 里直接调用一个工具（用于调试），不创建 Run：

```bash
brain tool test file.read --args-json '{"path":"README.md"}'
```

---

## 14. `brain config` · 配置管理

### 14.1 子命令

```
brain config list
brain config get <key>
brain config set <key> <value>
brain config unset <key>
brain config path    # 打印配置文件路径
```

### 14.2 配置文件格式

YAML（`~/.brain/config.yaml`）：

```yaml
mode: solo
endpoint: http://127.0.0.1:7701
default_brain: central
default_model: claude-sonnet-4-6
default_budget:
  max_turns: 50
  max_cost_usd: 5.0
output: human
log_level: info

credentials:
  anthropic:
    api_key_env: ANTHROPIC_API_KEY    # 只存变量名，不存明文
  openai:
    api_key_env: OPENAI_API_KEY
```

### 14.3 凭证保护

配置文件 MUST NOT 存明文 API key，只存环境变量名（`api_key_env`）或 Vault 引用（`api_key_vault: vault://brain/anthropic`）。凭证规则见 23-安全模型 §4 Vault。

---

## 15. `brain serve` · 启动 Kernel 服务（cluster 模式）

### 15.1 签名

```
brain serve [--listen <addr>] [--db <dsn>] [--log-file <path>]
```

### 15.2 选项

| 选项 | 默认 | 说明 |
|------|------|------|
| `--listen` | `127.0.0.1:7701` | 监听地址（HTTP + gRPC 同一端口多路复用） |
| `--db` | `mysql://brain:brain@127.0.0.1:3306/brain` | MySQL DSN |
| `--workspace-root` | `/var/lib/brain/runs/` | Run workspace 根目录 |
| `--max-concurrent-runs` | `20` | 最大并发 Run 数 |
| `--log-file` | `stderr` | 结构化日志输出位置 |
| `--otel-endpoint` | — | OTel collector 地址（见 24-可观测性） |

### 15.3 行为

- 启动嵌入式 Kernel，监听 HTTP/gRPC 接受其他 `brain` CLI 的请求
- 初始化数据库连接 + 跑 `make db-up` 等价的 migration
- 加载 tool registry / brain registry
- 监听 SIGTERM 优雅关闭（当前运行的 Run 存 checkpoint 后退出）

### 15.4 退出码

- `0` · 收到 SIGTERM 优雅退出
- `65` · 配置错误（DSN 解析失败等）
- `66` · DB 连接失败
- `67` · 端口被占用

---

## 16. `brain doctor` · 环境诊断

### 16.1 签名

```
brain doctor [--fix]
```

### 16.2 检查项

| 检查项 | 说明 |
|--------|------|
| workspace 目录存在且可写 | `~/.brain/` 权限 |
| 配置文件格式正确 | YAML 语法 |
| SQLite / MySQL 可连接 | 根据 mode |
| 必要的凭证环境变量已设置 | `credentials.*.api_key_env` 指向的变量 |
| sidecar 二进制存在 | `~/.brain/brains/central`, `~/.brain/brains/code` 等 |
| LLM API 可达 | 发一个最小 ping 请求 |
| 磁盘空间 | workspace 所在盘剩余 >1GB |
| 时钟同步 | 与 NTP 服务器差异 <5s |

### 16.3 输出（human）

```
Checking brain environment...

✓ workspace: /home/user/.brain (writable)
✓ config: /home/user/.brain/config.yaml (valid)
✓ database: sqlite:///home/user/.brain/brain.db (connected)
✓ credentials: ANTHROPIC_API_KEY (set)
✗ sidecars: /home/user/.brain/brains/code_brain (missing)
  → install with: brain install code_brain
✓ llm reachable: anthropic (123ms)
✓ disk space: 45GB free
✓ clock drift: 0.2s

1 issue found. Run with --fix to attempt repair.
```

### 16.4 退出码

- `0` · 所有检查通过
- `1` · 至少一项失败

---

## 17. `brain version` · 版本信息

### 17.1 签名

```
brain version [--short] [--json]
```

### 17.2 输出（human）

```
brain version 1.0.0
  protocol: 1.0
  kernel:   1.0.0
  sdk:      go/1.0.0
  commit:   a1b2c3d
  built:    2026-04-11T10:00:00Z
  os/arch:  linux/amd64
```

### 17.3 输出（json）

```json
{
  "cli_version": "1.0.0",
  "protocol_version": "1.0",
  "kernel_version": "1.0.0",
  "sdk_language": "go",
  "sdk_version": "1.0.0",
  "commit": "a1b2c3d",
  "built_at": "2026-04-11T10:00:00Z",
  "os": "linux",
  "arch": "amd64"
}
```

### 17.4 `--short`

只打印 CLI 版本号，无其他信息（用于脚本）：

```
1.0.0
```

---

## 18. 退出码规范

v1 冻结以下退出码含义，MUST NOT 在 v1 内改变：

| 退出码 | 常量 | 含义 |
|-------:|------|------|
| `0` | `OK` | 成功 |
| `1` | `ERR_FAILED` | Run 失败 / 检查失败 |
| `2` | `ERR_CANCELED` | Run 被取消 |
| `3` | `ERR_BUDGET_EXHAUSTED` | 预算耗尽 |
| `4` | `ERR_NOT_FOUND` | run/tool/配置项不存在 |
| `5` | `ERR_INVALID_STATE` | 状态不允许操作（如 cancel 已完成的 run） |
| `64` | `EX_USAGE` | 命令行参数错误（BSD sysexits） |
| `65` | `EX_DATAERR` | 配置文件格式错误 |
| `66` | `EX_NOINPUT` | 输入文件/数据库不可读 |
| `67` | `EX_NOPERM` | 权限不足 / 端口被占用 |
| `70` | `EX_SOFTWARE` | Kernel 通信失败（RPC 错误） |
| `71` | `EX_OSERR` | 操作系统错误（fork/fs/net） |
| `77` | `EX_NOPERM` | 凭证缺失 / Vault 访问失败 |
| `130` | `SIGINT` | 收到 Ctrl-C 中断 |
| `143` | `SIGTERM` | 收到 SIGTERM 关闭 |

**扩展策略**：

- `8~63` 保留给未来子命令自定义
- `≥100` 的非标准退出码 MUST NOT 被使用（避免与信号号冲突）
- 任何新退出码的引入 MUST 走 minor version bump

---

## 19. 输出格式规范

### 19.1 两种格式

| 格式 | 何时用 | 特点 |
|------|--------|------|
| `human` | 默认，TTY 场景 | 彩色、对齐表格、进度条、中文友好 |
| `json` | `-o json` 或检测到非 TTY | 纯 JSON / NDJSON，UTF-8，无颜色 |

### 19.2 自动检测

- stdout 不是 TTY → 自动切换到 `json`
- `NO_COLOR` 环境变量存在 → human 模式禁用颜色
- `BRAIN_OUTPUT=json` → 强制 json

### 19.3 human 模式约束

- 所有列表 MUST 对齐（等宽字体渲染时整齐）
- 状态 badge MUST 使用固定 Unicode 符号：`✓`（成功）`✗`（失败）`→`（进行中）`⏸`（暂停）`⊘`（取消）
- 进度条 MUST 在 `--quiet` 模式下禁用
- 错误 MUST 在 stderr，正常输出 MUST 在 stdout

### 19.4 json 模式约束

- 字段命名 MUST 使用 `snake_case`
- 时间 MUST 使用 RFC 3339（带 Z 的 UTC）
- 金额 MUST 使用 `cost_usd` 字段名（float）
- Token 数 MUST 使用 `input_tokens` / `output_tokens` / `cache_read_tokens` / `cache_creation_tokens`
- 错误 MUST 符合 21-错误模型 的 BrainError 格式
- 所有非流式命令 MUST 输出单个 JSON 对象
- 流式命令（`--follow`）MUST 输出 NDJSON，**每行一个对象 + 尾部 `\n`**

### 19.5 NDJSON 与 stdio 线缆协议的边界

**CLI 的 stdout NDJSON ≠ BrainKernel stdio 线缆协议。**

- stdio 线缆协议（20）是 Kernel ↔ sidecar 的内部协议，使用 Content-Length 头 + JSON body
- CLI 的 NDJSON 输出是给**人和脚本**看的用户界面，每行一个自包含 JSON

第三方 SDK 实现 CLI 时 MUST 正确分离这两种协议，不得把线缆协议帧直接复制到 stdout。

---

## 20. stdin 输入协议

### 20.1 三种输入方式

`brain run` 的 prompt 可以通过三种方式提供：

1. **命令行参数**：`brain run "build a REST API"`
2. **管道 stdin**：`echo "build a REST API" | brain run`
3. **heredoc / 文件**：`brain run < prompt.txt`

### 20.2 stdin 解析规则

- 若 stdin 是 TTY 且无参数 prompt：打开 `$EDITOR` 编辑器
- 若 stdin 是 pipe：读取直到 EOF，作为 prompt
- 若同时提供参数 prompt 和非空 stdin：退出码 `64`（冲突）
- 空 stdin + 空参数：退出码 `64`

### 20.3 多轮交互协议（v2 保留）

v1 **不支持** CLI 的多轮交互输入。`brain run` 是 one-shot 模式，如需多轮对话：

- 通过 `brain resume <run_id>` 继续之前的 Run
- 或用 `brain run --param user_question="..."` 把多轮融合进一次

v2 可能引入 `brain chat` 交互命令，v1 不暴露。

---

## 21. 环境变量

| 变量 | 等价 CLI 选项 | 说明 |
|------|---------------|------|
| `BRAIN_MODE` | `--mode` | solo / cluster |
| `BRAIN_ENDPOINT` | `--endpoint` | cluster 模式下 Kernel 地址 |
| `BRAIN_CONFIG` | `--config` | 配置文件路径 |
| `BRAIN_WORKSPACE` | `--workspace` | 工作目录根 |
| `BRAIN_OUTPUT` | `--output` | human / json |
| `BRAIN_LOG_LEVEL` | `--verbose` | trace / debug / info / warn / error |
| `BRAIN_NO_COLOR` | `--no-color` | 禁用颜色 |
| `BRAIN_TIMEOUT` | `--timeout` | CLI 超时 |
| `NO_COLOR` | — | 标准约定：存在即禁用颜色 |
| `ANTHROPIC_API_KEY` | — | LLM 凭证（由 Vault 引用） |
| `OPENAI_API_KEY` | — | LLM 凭证 |
| `EDITOR` | — | 编辑器命令（`brain run` 无 prompt 时使用） |

**未定义**：`BRAIN_*` 前缀以外的环境变量 MUST NOT 影响 CLI 行为。实现者不得引入 `BRAINKERNEL_*` 之类的变体。

---

## 22. 工作目录与文件布局

### 22.1 solo 模式

```
~/.brain/
├── config.yaml              # 用户配置
├── brain.db                 # SQLite WAL（见 26-持久化与恢复 §4）
├── brain.db-wal             # WAL 文件
├── brain.db-shm             # SHM 文件
├── brains/                  # sidecar 二进制
│   ├── central
│   ├── code_brain
│   ├── browser_brain
│   └── ...
├── runs/
│   └── r_01HX9K8M2ZABCDEFG/
│       ├── workspace/       # Run workspace（sidecar 的 FS sandbox 根）
│       ├── trace.jsonl      # trace 事件日志
│       ├── audit.jsonl      # 审计事件日志（hash chain）
│       └── artifacts/       # CAS 存储（sha256 命名）
├── cas/                     # 跨 Run CAS 存储（全局去重）
│   └── ab/cd/abcd....bin
└── logs/
    └── brain.log            # CLI 自己的日志
```

### 22.2 cluster 模式（serve 侧）

```
/var/lib/brain/
├── runs/
│   └── r_.../               # 同 solo
├── cas/                     # 同 solo
└── logs/

/etc/brain/
└── config.yaml              # 服务端配置
```

### 22.3 权限要求

- `~/.brain/` MUST 是 `0700`（仅用户可访问）
- `config.yaml` MUST 是 `0600`
- `brain.db*` MUST 是 `0600`
- sidecar 二进制 MUST 至少 `0755`（可执行）
- CAS 文件 MUST 是 `0644`（只读）

### 22.4 磁盘管理

- `~/.brain/runs/` 下每个 Run 目录由 CLI 或 Kernel 管理
- `brain list --prune` （v1 保留为 v2 命令，v1 只显示不清理）
- 用户可手动删除 `runs/` 下的目录，但 MUST 同步 `brain.db` 中的记录一致性（Kernel 启动时应修复孤儿）

---

## 23. 信号处理

### 23.1 SIGINT（Ctrl-C）

- **第 1 次**：发送优雅取消信号到当前 Run（相当于 `brain cancel <run_id>`，保存 checkpoint）
- **第 2 次**：强制中断（相当于 `--force`，丢弃 checkpoint）
- **第 3 次**：立刻 `_exit(130)`

### 23.2 SIGTERM

等价于第 1 次 SIGINT：优雅取消，最多等 30 秒，超时后 `_exit(143)`。

### 23.3 SIGPIPE

stdout/stderr 被关闭时（例如 `brain logs | head`）：

- CLI MUST 捕获 SIGPIPE 并干净退出，**不**打印额外错误
- 退出码 `0`（因为用户主动关闭了管道，不是错误）

### 23.4 SIGHUP

- 非守护模式（`brain run` / `brain logs --follow`）：等价于 SIGTERM
- 守护模式（`brain serve`）：重新加载配置文件（不重启连接）

---

## 24. 向后兼容策略

### 24.1 v1 冻结范围

下列项 MUST NOT 在 v1 的整个生命周期内改变（除非是 bug 修复）：

- 所有子命令名称
- 所有长选项名称（`--mode` / `--brain` / ...）
- 所有短选项含义（`-o` / `-v` / ...）
- 所有环境变量名（`BRAIN_*`）
- 所有退出码含义
- JSON 输出的字段名与含义
- 配置文件 YAML 字段名

### 24.2 允许的 minor 扩展

下列变更允许在 v1.x 的 minor bump 引入：

- 新子命令
- 新选项（不影响现有选项行为）
- JSON 输出新增字段（已有字段 MUST 保持）
- 新环境变量（不影响现有）
- 新退出码（在 `8~63` 范围内）

### 24.3 弃用流程

- 要弃用一个命令/选项：
  1. v1.x 引入 `--deprecated` 警告（stderr 打印 WARN）
  2. v1.last（最后一个 v1 minor）继续警告
  3. v2.0 才能真正移除
  4. 弃用窗口至少 6 个月或 2 个 minor 版本（取大）

### 24.4 v2 breaking change 策略

- v2 可以引入 breaking change，但必须：
  - 提供迁移工具（`brain migrate-config v1-to-v2`）
  - v1 和 v2 二进制可以共存（不同可执行名 `brain1` / `brain2`？留给 v2 决定）
  - 提供兼容层选项（`--compat v1` 让部分命令保持 v1 行为）

---

## 25. 合规测试矩阵 C-CLI-*

| ID | 测试项 | 期望 |
|----|--------|------|
| C-CLI-01 | `brain` 无参数 | 打印 usage 退出 0 |
| C-CLI-02 | `brain --version` | 打印版本信息退出 0 |
| C-CLI-03 | `brain --version --short` | 只打印版本号退出 0 |
| C-CLI-04 | `brain unknown-cmd` | 退出 64 + stderr usage |
| C-CLI-05 | `brain run` 无 prompt 无 stdin 非 TTY | 退出 64 |
| C-CLI-06 | `brain run "test" --detach` | 立刻返回 run_id 退出 0 |
| C-CLI-07 | `brain run --follow` NDJSON 格式 | 每行一个自包含 JSON 对象 |
| C-CLI-08 | `brain status <不存在>` | 退出 4 |
| C-CLI-09 | `brain cancel <completed run>` | 退出 5 |
| C-CLI-10 | `brain resume <completed run>` | 退出 5 |
| C-CLI-11 | `brain list -o json` | 输出合法 JSON，顶层有 `runs` 数组 |
| C-CLI-12 | `brain config set output invalid` | 退出 65（值域校验） |
| C-CLI-13 | `brain -o json` 所有命令 | stdout 合法 JSON，stderr 可有日志 |
| C-CLI-14 | stdout 非 TTY | 自动切换 json |
| C-CLI-15 | `NO_COLOR=1 brain list` | 输出无 ANSI 码 |
| C-CLI-16 | SIGINT 单次 | 优雅取消，退出 130，checkpoint 已保存 |
| C-CLI-17 | SIGPIPE（`brain logs \| head -1`） | 退出 0 |
| C-CLI-18 | `brain doctor` 所有检查 pass | 退出 0 |
| C-CLI-19 | `brain replay --mock-llm` | 不发 LLM 调用，只读 trace |
| C-CLI-20 | `brain --help` 所有子命令 | 打印 help，退出 0 |

**实现要求**：第三方 SDK 必须通过全部 20 条测试才能声称 `brain` CLI 兼容 BrainKernel v1。测试驱动见 25-测试策略 §4 Cross-lang 层。

---

## 附录 A · 完整命令速查

```
brain run [options] [prompt]
  启动一次新 Run
  主要选项: --brain --model --wait --follow --detach --idempotency-key

brain status <run_id>
brain status --all [--state] [--since]
  查询 Run 状态

brain resume <run_id> [--follow]
  恢复中断的 Run

brain cancel <run_id> [--force]
  取消 Run

brain list [--state] [--tag] [--since] [--limit] [--sort] [--reverse]
  列出 Run

brain logs <run_id> [--follow] [--from] [--to] [--type]
  查看 Run 日志

brain replay <run_id> [--output-dir] [--mock-llm] [--mock-tools]
  重放 Run（审计）

brain tool list [--brain]
brain tool describe <tool>
brain tool test <tool> [--args-json]
  工具管理

brain config list|get|set|unset|path
  配置管理

brain serve [--listen] [--db] [--log-file]
  启动 Kernel 服务（cluster 模式）

brain doctor [--fix]
  环境诊断

brain version [--short] [--json]
  版本信息

brain help [command]
  帮助
```

---

## 附录 B · JSON 输出 schema 清单

v1 冻结以下 JSON 对象的 schema（字段名 + 类型 + 必选/可选）。第三方 SDK 的 json 输出 MUST 与这些 schema 完全一致。

### B.1 Run 对象

```jsonc
{
  "run_id":           "string",       // 必选
  "state":            "string",       // 必选 · pending/running/paused/waiting_tool/completed/failed/canceled/crashed
  "brain":            "string",       // 必选
  "model":            "string",       // 可选
  "workspace":        "string",       // 可选（绝对路径）
  "current_turn":     "int",          // 可选
  "turns":            "int",          // 必选（completed 后是总数）
  "cost_usd":         "float",        // 必选
  "max_cost_usd":     "float",        // 可选（budget）
  "max_turns":        "int",          // 可选
  "input_tokens":     "int",          // 可选
  "output_tokens":    "int",          // 可选
  "cache_read_tokens":"int",          // 可选
  "cache_creation_tokens":"int",      // 可选
  "started_at":       "rfc3339",      // 必选
  "ended_at":         "rfc3339",      // 可选（仅结束后）
  "duration_seconds": "int",          // 可选
  "last_activity_at": "rfc3339",      // 可选
  "tags":             "string[]",     // 可选
  "checkpoint":       "Checkpoint"    // 可选
}
```

### B.2 Checkpoint 对象

```jsonc
{
  "turn_uuid":    "string",
  "turn_index":   "int",
  "state":        "string",
  "trace_parent": "string"
}
```

### B.3 Tool 对象（`brain tool list` 输出）

```jsonc
{
  "name":        "string",
  "brain":       "string",
  "kind":        "string",   // readonly/mutable
  "risk":        "string",   // safe/low/med/high/critical
  "description": "string",
  "schema":      "object"    // JSON schema of args
}
```

### B.4 Version 对象（见 §17.3）

### B.5 错误对象

所有非零退出码的 stderr（或 json 模式下的 stdout）MUST 输出一个 BrainError：

```jsonc
{
  "class":       "string",
  "error_code":  "string",
  "retryable":   "bool",
  "fingerprint": "string",
  "message":     "string",
  "hint":        "string",
  "trace_id":    "string",
  "occurred_at": "rfc3339"
}
```

完整定义见 21-错误模型.md。

### B.6 流式事件（`brain run --follow` / `brain logs --follow`）

NDJSON，每行是以下类型之一：

- `run.created` / `run.started` / `run.completed` / `run.failed` / `run.canceled`
- `turn.started` / `turn.completed`
- `message.start` / `content.delta` / `tool_call.delta` / `message.delta` / `message.end`
- `tool.invoke` / `tool.result` / `tool.error`

每个事件对象 MUST 包含 `type` / `run_id` / `ts` 三个必选字段。详见 22-Agent-Loop §5。

---

## 版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.0 | 2026-04-11 | 首版：冻结 13 个子命令 + 20 条合规测试 + 两种模式 + 退出码规范 + 输出格式规范 + stdin 协议 + 环境变量 + 工作目录布局 + 信号处理 + 向后兼容策略 |
