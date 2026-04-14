# 27 · CLI 命令契约 v1

> **状态**：v1 契约 + 当前实现对齐说明 · 2026-04-13
> **上位规格**：[02-BrainKernel设计.md](./02-BrainKernel设计.md) §12 执行器架构
> **依赖**：
> - [20-协议规格.md](./20-协议规格.md)（stdio 线缆协议）
> - [21-错误模型.md](./21-错误模型.md)（退出码与错误输出）
> - [26-持久化与恢复.md](./26-持久化与恢复.md)（solo/cluster 双轨存储）
>
> **实现快照（2026-04-13）**：
> 当前仓库主干已经切到共享 file-backed CLI runtime：`brain run/chat/serve/status/list/logs/cancel/resume/replay`
> 共用持久化与权限边界；`run_id` 对外统一为字符串；`brain serve` 已支持 `/health`、`/v1/version`、`/v1/tools`、`/v1/runs*`。
> 本文档仍保留部分面向未来的 cluster/marketplace 约束，但 `run`/`chat`/`serve` 的模型配置覆写与文件策略边界已在实现中生效。

## 目录

- [1. 动机与范围](#1-动机与范围)
- [2. 术语](#2-术语)
- [3. 顶层命令 `brain`](#3-顶层命令-brain)
- [4. 顶层与共享选项](#4-顶层与共享选项)
- [5. 子命令总览](#5-子命令总览)
- [6. `brain run` · 启动一次 Run](#6-brain-run--启动一次-run)
- [6A. `brain chat` · 交互式 REPL](#6a-brain-chat--交互式-repl)
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

### 1.3 当前运行形态

当前主干已稳定提供两种**本地可用运行形态**：

| 形态 | 场景 | Kernel 位置 | 持久化 | 对外接口 |
|------|------|-------------|--------|----------|
| **嵌入式 CLI runtime** | 开发机 / CI / 单机执行 | CLI 进程内 | `~/.brain/store.json` + `~/.brain/runs.json` + `~/.brain/artifacts/` | `brain chat/run/status/list/...` |
| **HTTP service runtime** | 常驻服务 / 本机控制面 / 受控集成 | `brain serve` 进程 | 同一套 file-backed runtime | `/health`、`/v1/version`、`/v1/tools`、`/v1/runs*` |

`cluster / remote endpoint` 作为更强的未来形态仍保留在规格层讨论中，但当前正式实现并没有一个“CLI 作为远端 client 通吃所有命令”的独立完成态。

### 1.4 当前实现快照（2026-04-13）

为避免把“目标契约”和“当前代码行为”混为一谈，这里固定记录当前主干已经对齐到的部分：

1. `brain run/chat/serve/status/list/logs/cancel/resume/replay` 已切到共享 file-backed CLI runtime，不再是纯内存态
2. `run_id` 对外统一为字符串，`status / logs / list / cancel / resume` 均可直接追踪同一条持久化记录
3. `brain serve` 已提供 `/health`、`/v1/version`、`/v1/tools`、`/v1/runs*` 的可用 Run API，并支持请求级 `model_config` / `workdir` / `timeout` / `file_policy`
4. `restricted` 已是 `run/chat/serve` 共用的正式权限模式，文件级边界由 `workdir + file_policy` 共同决定
5. `brain` 无参数已直接进入 `chat` REPL；`chat` 不再是未来保留项

后续继续推进 CLI 时，应继续补强更细的审计、持久化后端和 marketplace / package 层，而不是回退到过时的内存态或 prompt-only 边界。

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

当前实现里，直接运行 `brain` 等价于 `brain chat`，进入交互式 REPL。

如需顶层帮助，使用：

- `brain --help`
- `brain help`

### 3.4 `brain help [command]`

- `brain help` 打印顶层 usage（列出所有子命令）
- `brain help <command>` 打印指定子命令的详细用法
- 退出码 `0`

---

## 4. 顶层与共享选项

当前实现**没有**统一的“全局 flag 解析器”。除 `brain --help` / `brain help` 外，大多数 flag 都是**子命令本地解析**。

当前稳定可依赖的共享行为：

| 行为 | 当前实现 |
|------|----------|
| 顶层帮助 | `brain --help` / `brain help` |
| 版本查看 | `brain version` / `brain version --short` / `brain version --json` |
| 配置文件路径覆盖 | `BRAIN_CONFIG` 环境变量 |
| 默认 API key | `ANTHROPIC_API_KEY` 环境变量 |

`mode/workdir/timeout/json/provider/model` 等均应通过**具体子命令的 flag** 传入，而不是假定存在顶层全局 flag。

---

## 5. 子命令总览

| 命令 | 作用 | 读/写 | 网络 |
|------|------|-------|------|
| `chat` | 交互式 REPL | 写 | LLM + tool |
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
brain run [options]
```

### 6.2 当前实现参数与选项

当前实现中，初始任务通过 `--prompt` 传入；位置参数 / `$EDITOR` / stdin prompt 协议尚未作为正式 CLI 行为开放。

| 选项 | 类型 | 说明 |
|------|------|------|
| `--prompt` | string | 初始任务文本；当前默认值是 `hello from brain run` |
| `--provider` | string | provider/profile 名称；`mock` 表示使用内置 MockProvider |
| `--api-key` | string | 显式 API key 覆盖 |
| `--base-url` | string | 显式 provider base URL 覆盖 |
| `--model` | string | 显式模型名覆盖 |
| `--brain` | string | 入口 brain，当前默认 `central` |
| `--max-turns` | int | 当前 Run 最大 turn 数 |
| `--model-config-json` | JSON string | 结构化模型配置，字段：`provider` / `base_url` / `api_key` / `model` / `brain_models` |
| `--mode` | string | 权限模式：`plan` / `default` / `accept-edits` / `auto` / `restricted` / `bypass-permissions` |
| `--workdir` | path | 工作目录根；所有路径级工具都受此边界约束 |
| `--file-policy-json` | JSON string | 细粒度文件策略，字段：`allow_read` / `allow_create` / `allow_edit` / `allow_delete` / `deny` |
| `--timeout` | duration | 总运行超时；`0` 表示关闭本次 Run 的 CLI 挂钟超时 |
| `--stream` | bool | 打开 provider streaming 路径；当前最终输出仍是最终 summary，不是 NDJSON follow |
| `--json` | bool | 输出 Run summary JSON；当前默认 `true` |
| `--reply` | string | 仅 `--provider mock` 时使用的固定回复 |

`--model-config-json` 的优先级为：

1. `brain_models.<brain>`
2. `model_config.model`
3. 显式 `--model`
4. 本地 `config.json` provider/profile
5. 环境变量

`--file-policy-json` 生效时：

- `read_file` / `search` MUST 受 `allow_read` 约束
- `write_file` 对新文件按 `allow_create` 判定，对已存在文件按 `allow_edit` 判定
- `delete_file` MUST 单独受 `allow_delete` 约束；删除默认最严
- `deny` 优先级最高；命中即拒绝
- 命令型工具（如 `shell_exec`、`run_tests`）不会被一刀切禁用；它们 MUST 在受限 `workdir` 的临时隔离工作区内执行，只暴露 `allow_read` 命中的可读文件面
- 仅命中 `allow_edit` / `allow_delete`、未命中 `allow_read` 的已存在文件，MUST 以不暴露原内容的占位形式进入命令工作区；命令 MAY blind overwrite / delete，但 MUST NOT 读取真实内容
- 命令结束后，只有命中 `allow_create` / `allow_edit` / `allow_delete` 的真实 diff 才能同步回原始 `workdir`；越权改动 MUST 被丢弃并记审计事件
- delegated sidecar MUST 继承同一份 `workdir + file_policy` 执行边界，不能成为绕过手段
- 上述边界独立于 permission mode；`bypass-permissions` 也不能绕过

### 6.3 行为

1. 读取 `config.json`，解析 provider/model、permission mode、timeout、file policy
2. 构建共享 file-backed CLI runtime，并为本次 Run 创建持久化 run record
3. 根据 `workdir + file_policy + mode` 构建受控执行环境
4. 组装工具注册表；当 `brain=central` 且 sidecar 可用时，会额外挂上 `central.delegate`
5. 执行一次托管 Run，并把 `run.*` / `tool.*` / `policy.*` / `approval.*` 事件落到 `runs.json`
6. 输出最终 summary；当前 `brain run` 不提供 `detach/follow` 用户接口

### 6.4 输出（json 模式，当前默认）

```json
{
  "run_id": "run-1-20260413T120000Z",
  "store_run_id": 1,
  "brain_id": "central",
  "state": "completed",
  "turns": 1,
  "llm_calls": 1,
  "tool_calls": 0,
  "elapsed_ms": 42,
  "reply": "hello from mock provider",
  "provider": "mock",
  "plan_id": 1
}
```

### 6.5 输出（human 模式，`--json=false`）

```
run run-1-20260413T120000Z completed reply="hello from mock provider"
```

### 6.6 退出码

- `0` · Run 成功完成（state=`completed`）
- `1` · Run 失败（state=`failed`）
- `2` · Run 被取消（state=`canceled`）
- `3` · Run 超出预算（Budget exhausted）
- `64` · 参数错误
- `65` · 配置错误
- `70` · 运行时初始化失败

## 6A. `brain chat` · 交互式 REPL

### 6A.1 签名

```
brain chat [options]
brain
```

### 6A.2 当前实现参数

`brain chat` 与 `brain run` 共享大部分 provider / mode / workdir / file policy 参数，并额外提供：

| 选项 | 类型 | 说明 |
|------|------|------|
| `--brain` | string | 当前会话 brain，默认 `central` |
| `--max-turns` | int | 每条用户消息的最大 turn 数 |
| `--provider` / `--api-key` / `--base-url` / `--model` / `--model-config-json` | — | 与 `brain run` 相同 |
| `--mode` / `--workdir` / `--file-policy-json` / `--timeout` | — | 与 `brain run` 相同 |

### 6A.3 行为

- `brain` 无参数直接进入 REPL
- `chat` 会话与 `run` 共用同一套执行环境、受限策略、provider 解析与持久化骨架
- 每轮消息会建立持久化 run 记录，因此 `restricted` / `file_policy` / 审批 / policy 拒绝同样会留下可追踪事件
- 当前支持交互式审批、diff 预览、slash command、delegate specialist brain

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
brain tool list [--brain <brain>] [--scope <scope>]
brain tool describe [--scope <scope>] <tool_name>
brain tool test <tool_name> [--scope <scope>] [--args-json <json>]
```

### 13.2 `brain tool list`

列出当前运行时注册的工具。若提供 `--scope`，则 MUST 先应用
`active_tools.<scope>` 对应的 tool profile，再输出 effective tools。

```
NAME                    BRAIN     KIND     RISK   DESCRIPTION
file.read               built-in  readonly safe   Read a file from workspace
file.write              built-in  mutable  med    Write file (requires fs.write permission)
shell.exec              built-in  mutable  high   Execute shell command (sandboxed)
plan_store.create       central   mutable  safe   Create a new plan
...
```

### 13.3 `brain tool describe`

打印指定工具的 JSON schema（参数、返回值、所属 brain、风险等级、描述）。
当前实现会输出：

- `input_schema`
- `output_schema`（如果工具声明了结构化成功返回）
- `risk`
- `brain`
- `description`

若提供 `--scope`，则工具 MUST 先通过该 scope 的 effective 过滤。

### 13.4 `brain tool test`

在 CLI 里直接调用一个工具（用于调试），不创建 Run：

```bash
brain tool test verifier.check_output --args-json '{"actual":"ok","expected":"ok"}'
brain tool test code.read_file --scope run.code --args-json '{"path":"README.md"}'
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

JSON（`~/.brain/config.json`）：

```json
{
  "mode": "solo",
  "default_brain": "central",
  "permission_mode": "restricted",
  "serve_workdir_policy": "confined",
  "timeout": "30m",
  "active_provider": "anthropic",
  "providers": {
    "anthropic": {
      "base_url": "https://api.anthropic.com",
      "api_key": "sk-ant-xxxxx",
      "model": "claude-sonnet-4-20250514"
    }
  },
  "default_budget": {
    "max_turns": 20,
    "max_cost_usd": 5.0
  },
  "tool_profiles": {
    "coding_no_shell": {
      "include": ["code.*", "central.delegate"],
      "exclude": ["*.shell_exec"]
    }
  },
  "active_tools": {
    "chat.central.default": "coding_no_shell",
    "delegate.code": "coding_no_shell"
  },
  "file_policy": {
    "allow_read": ["README.md", "cmd/**/*.go"],
    "allow_create": ["docs/*.md"],
    "allow_edit": ["cmd/**/*.go"],
    "allow_delete": [],
    "allow_commands": true,
    "allow_delegate": true,
    "deny": [".git/**", "bin/**", "**/.env"]
  }
}
```

`tool_profiles` / `active_tools` 规则：

- `tool_profiles.<name>.include` 是 allow-list；空 include 表示从全部已注册工具开始。
- `tool_profiles.<name>.exclude` 在 include 之后应用。
- pattern MUST 使用 glob 语义（如 `code.*`、`*.shell_exec`）。
- `active_tools.<scope>` MUST 引用已存在的 profile 名称，多个 profile 用逗号分隔。
- 推荐 scope：`chat`、`chat.<brain>`、`chat.<brain>.<mode>`、`run`、`run.<brain>`、`delegate`、`delegate.<brain>`。

### 14.3 凭证保护

当前实现允许在 `config.json` 中存 `api_key`，但生产环境 SHOULD 优先使用环境变量或 Vault 引用。凭证规则见 23-安全模型 §4 Vault。

---

## 15. `brain serve` · 启动 Kernel 服务（cluster 模式）

### 15.1 签名

```
brain serve [--listen <addr>] [--max-concurrent-runs <n>] [--mode <mode>] [--workdir <path>] [--run-workdir-policy <confined|open>] [--log-file <path>]
```

### 15.2 选项

| 选项 | 默认 | 说明 |
|------|------|------|
| `--listen` | `127.0.0.1:7701` | HTTP 监听地址 |
| `--max-concurrent-runs` | `20` | 最大并发 Run 数 |
| `--mode` | 配置或 `default` | 默认权限模式 |
| `--workdir` | 当前目录 | 服务级默认工作目录；请求可再覆盖 |
| `--run-workdir-policy` | `confined` | 请求级 `workdir` 约束：`confined` 仅允许服务根目录内，`open` 显式放开 |
| `--log-file` | `stderr` | 结构化日志输出位置 |

### 15.2.A 当前实现补充（2026-04-13）

当前实现的 `brain serve` 公开以下 HTTP 端点：

- `GET /health`
- `GET /v1/version`
- `GET /v1/tools`
- `GET /v1/runs`
- `POST /v1/runs`
- `GET /v1/runs/:id`
- `DELETE /v1/runs/:id`

`POST /v1/runs` 当前支持请求体字段：

```json
{
  "prompt": "修复 cmd 下的测试",
  "brain": "central",
  "max_turns": 8,
  "stream": false,
  "workdir": "./",
  "timeout": "30m",
  "model_config": {
    "provider": "anthropic",
    "base_url": "https://api.anthropic.com",
    "api_key": "sk-ant-...",
    "model": "claude-sonnet-4-20250514",
    "brain_models": {
      "code": "claude-sonnet-4-20250514"
    }
  },
  "file_policy": {
    "allow_read": ["cmd/**/*.go"],
    "allow_edit": ["cmd/**/*.go"],
    "allow_create": ["docs/*.md"],
    "allow_delete": [],
    "deny": [".git/**", "bin/**"]
  }
}
```

其中：

- `model_config` 用于覆盖本地 provider/profile 配置
- `workdir` 是请求级工作目录覆盖；不传时沿用 `brain serve --workdir`
- `brain serve` 默认使用 `confined` 请求级 workdir 策略；若要允许请求跳出服务根目录，MUST 显式传 `--run-workdir-policy open` 或配置 `serve_workdir_policy=open`
- `timeout` 是请求级总超时覆盖；不传时沿用 config / 默认值
- `file_policy` 用于约束本次 Run 的读/写/删边界；`allow_read / allow_create / allow_edit / allow_delete / deny` 独立生效
- `allow_commands=false` 时命令型工具 MUST 直接拒绝；默认仍允许在受限 workdir 的临时隔离工作区里执行，并只同步命中策略的 create/edit/delete 结果
- `allow_delegate=false` 时 `central.delegate` MUST 不暴露给模型；默认仍允许 delegate，但 delegated sidecar MUST 继承同一份执行边界
- 在 sandbox 包装下，`code.search` 若省略 `path` 或传空字符串，MUST 默认解析到当前请求的 `workdir`，不能退回进程 `cwd`

### 15.3 行为

- 启动嵌入式 file-backed CLI runtime，并监听 HTTP 请求
- 加载当前运行时工具注册表；若 sidecar 可用，则 `central` 路径同时暴露 delegate 能力
- `POST /v1/runs` 为每次请求建立独立的 provider/session、`workdir`、`timeout`、`file_policy` 执行边界
- `POST /v1/runs` MUST 在请求级校验和并发槽位预留成功后才创建持久化 run；被校验拒绝或命中并发上限的请求不能留下伪 `running` 记录
- 收到 SIGINT / SIGTERM 时，先停止接收新请求，再取消 in-flight Run，并等待 drain 结束后退出

### 15.4 退出码

- `0` · 正常退出（含 SIGINT 优雅关闭）
- `64` · 参数错误
- `67` · 监听地址不可用 / 权限不足
- `70` · 运行时初始化失败或 HTTP server 异常退出
- `143` · 收到 SIGTERM 并优雅关闭

---

## 16. `brain doctor` · 环境诊断

### 16.1 签名

```
brain doctor [--fix]
```

### 16.2 检查项

| 检查项 | 当前实现 |
|--------|----------|
| workspace | `~/.brain/` 可访问、目录存在或可首次创建 |
| config | `config.json` 语法正确；非 Windows 下权限需为 `0600` |
| database | 共享 file-backed runtime 可初始化，且 PlanStore round-trip 正常 |
| credentials | 当前 provider 的 API key 可从 config / env 解析 |
| sidecars | 内置 sidecar 可在 `brain` 同目录或 `PATH` 中找到 |
| llm reachable | 对 provider `base_url` 主机做最小 TCP dial |
| disk space | 通过 ArtifactStore round-trip 验证本地 artifact backend 可写 |
| clock drift | 当前仍为 `skip`，因为还未配置 NTP probe |

### 16.3 输出（human）

```
Checking brain environment...

✓ workspace: /home/user/.brain (writable)
✓ config: /home/user/.brain/config.json (valid)
✓ database: file-backed runtime OK (plan=1)
✓ credentials: anthropic credentials resolved
✓ sidecars: all built-in sidecars found
✓ llm reachable: anthropic (123ms)
✓ disk space: artifact store round-trip OK (ref=sha256:...)
- clock drift: skipped (NTP probe not configured)

All active checks passed (1 skipped in current build).
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
| `human` | 默认 | 面向人类的文本输出 |
| `json` | 子命令显式传 `--json` 时 | 结构化 JSON 输出 |

### 19.2 自动检测

当前实现没有统一的顶层 `-o json` / `--output` 选择器，也没有全局 TTY 自动切换规则。

- 需要结构化输出时，应使用子命令自己的 `--json`
- `brain chat` 是交互界面，不适用本节的统一 JSON 约束

### 19.3 human 模式约束

- 列表命令 SHOULD 保持稳定列宽，便于扫描
- 错误 MUST 在 stderr，正常输出 MUST 在 stdout
- `chat` 的 ANSI 渲染、spinner、diff preview 属于 REPL UX；其他命令不应假定有统一颜色/进度条协议

### 19.4 json 模式约束

- 字段命名 MUST 使用 `snake_case`
- 时间 MUST 使用 RFC 3339（带 Z 的 UTC）
- 金额 MUST 使用 `cost_usd` 字段名（float）
- Token 数 MUST 使用 `input_tokens` / `output_tokens` / `cache_read_tokens` / `cache_creation_tokens`
- 错误 MUST 符合 21-错误模型 的 BrainError 格式
- 所有非流式命令 MUST 输出单个 JSON 对象
- 当前实现里，`brain run` 默认输出单个 summary JSON；持久化事件应通过 `brain logs --json` 查看

### 19.5 NDJSON 与 stdio 线缆协议的边界

**CLI 的 stdout NDJSON ≠ BrainKernel stdio 线缆协议。**

- stdio 线缆协议（20）是 Kernel ↔ sidecar 的内部协议，使用 Content-Length 头 + JSON body
- CLI 的 NDJSON 输出是给**人和脚本**看的用户界面，每行一个自包含 JSON

第三方 SDK 实现 CLI 时 MUST 正确分离这两种协议，不得把线缆协议帧直接复制到 stdout。

---

## 20. stdin 输入协议

### 20.1 当前输入方式

当前实现里：

1. `brain run` 通过 `--prompt` 提供任务文本
2. `brain chat` 通过交互式 REPL 持续接收用户输入
3. `brain serve` 通过 HTTP `POST /v1/runs` 请求体接收 `prompt`

### 20.2 当前限制

- `brain run` 还没有把位置参数 / stdin / `$EDITOR` 作为正式输入协议开放
- 若要做自动化集成，应优先使用 `--prompt` 或 `/v1/runs`

### 20.3 多轮交互输入

当前实现已经提供 `brain chat` 交互式 REPL。

- `brain` 无参数等价于 `brain chat`
- `brain run` 仍是一条 prompt 对应一次托管 Run
- 多轮交互应通过 `brain chat` 完成，而不是靠 `brain run` 的 stdin 协议

---

## 21. 环境变量

| 变量 | 当前实现 | 说明 |
|------|----------|------|
| `BRAIN_CONFIG` | ✓ | 覆盖配置文件路径 |
| `ANTHROPIC_API_KEY` | ✓ | provider 默认 API key 来源 |
| 其他 `BRAIN_*` | 保留 | 目前不应假定已经接线为稳定接口 |

实现建议：对自动化脚本，优先使用显式子命令 flag + `config.json`；不要依赖文档中尚未实现的全局环境变量矩阵。

---

## 22. 工作目录与文件布局

### 22.1 当前默认布局

```
~/.brain/
├── config.json              # 用户配置（0600）
├── keybindings.json         # chat REPL 键位配置（可选）
├── store.json               # plan/checkpoint/usage/artifact metadata
├── runs.json                # run 元数据与持久化事件
└── artifacts/               # CAS artifact 内容
```

### 22.2 sidecar 与 workdir 说明

- sidecar 二进制**不默认放在** `~/.brain/brains/`
- 当前查找顺序是：
  1. 与 `brain` 主二进制同目录
  2. `PATH`
- `workdir` 是执行边界，不是运行时数据库目录；`run`/`chat` 默认使用当前目录，`serve` 则先用服务级 `--workdir`，再允许请求级覆盖
- 对带 `path` 的路径型工具，省略 `path` 的默认值也 MUST 绑定到当前 `workdir`；不能因为工具内部走 `filepath.Abs(".")` 而漂移到宿主进程的 `cwd`

### 22.3 权限要求

- `~/.brain/` MUST 是 `0700`（仅用户可访问）
- `config.json` MUST 是 `0600`
- `store.json` / `runs.json` SHOULD 是 `0600`
- sidecar 二进制 MUST 至少 `0755`（可执行）
- artifact 文件由 runtime 管理；默认目录权限应不放宽到其他用户可写

### 22.4 磁盘管理

- `store.json` / `runs.json` / `artifacts/` 由 runtime 管理
- 当前没有 `brain list --prune`
- 如需清理数据，应整体备份并谨慎处理 `~/.brain/`，而不是只删单个孤立文件

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
| C-CLI-01 | `brain` 无参数 | 进入 chat REPL |
| C-CLI-02 | `brain version` | 打印版本信息退出 0 |
| C-CLI-03 | `brain version --short` | 只打印版本号退出 0 |
| C-CLI-04 | `brain unknown-cmd` | 退出 64 + stderr usage |
| C-CLI-05 | `brain run --provider mock --prompt "test"` | 返回合法 summary，退出 0 |
| C-CLI-06 | `brain run --provider mock --mode restricted` 且无 `file_policy` | 退出 64 |
| C-CLI-07 | `brain run --stream --provider mock` | 走流式执行路径，但最终 stdout 仍是 summary |
| C-CLI-08 | `brain status <不存在>` | 退出 4 |
| C-CLI-09 | `brain cancel <completed run>` | 退出 5 |
| C-CLI-10 | `brain resume <completed run>` | 退出 5 |
| C-CLI-11 | `brain list --json` | 输出合法 JSON，顶层有 `runs` 数组 |
| C-CLI-12 | `brain config set output invalid` | 退出 65（值域校验） |
| C-CLI-13 | `brain status <run_id> --json` | stdout 合法 JSON |
| C-CLI-14 | `BRAIN_CONFIG=/tmp/config.json brain version` | 使用指定配置路径不报错 |
| C-CLI-15 | `brain serve --run-workdir-policy confined` | 启动参数可解析 |
| C-CLI-16 | SIGINT 单次 | 优雅取消，退出 130，checkpoint 已保存 |
| C-CLI-17 | SIGPIPE（`brain logs \| head -1`） | 退出 0 |
| C-CLI-18 | `brain doctor` 所有检查 pass | 退出 0 |
| C-CLI-19 | `brain replay --mock-llm` | 不发 LLM 调用，只读 trace |
| C-CLI-20 | `brain --help` 所有子命令 | 打印 help，退出 0 |

**实现要求**：第三方 SDK 必须通过全部 20 条测试才能声称 `brain` CLI 兼容 BrainKernel v1。测试驱动见 25-测试策略 §4 Cross-lang 层。

---

## 附录 A · 完整命令速查

```
brain chat [--brain] [--provider] [--model-config-json] [--mode] [--workdir] [--file-policy-json] [--timeout]
  交互式 REPL

brain run [--prompt] [--brain] [--provider] [--model-config-json] [--mode] [--workdir] [--file-policy-json] [--timeout] [--stream]
  启动一次 Run

brain status <run_id> [--json]
  查询 Run 状态

brain resume <run_id> [--follow] [--json]
  恢复中断的 Run

brain cancel <run_id> [--force] [--json]
  取消 Run

brain list [--state] [--limit] [--json]
  列出 Run

brain logs <run_id> [--type] [--follow] [--json]
  查看 Run 日志

brain replay <run_id> [--output-dir] [--mock-llm] [--mock-tools]
  重放 Run（审计）

brain tool list [--brain] [--scope <scope>]
brain tool describe [--scope <scope>] <tool>
brain tool test <tool> [--scope <scope>] [--args-json]
  工具管理

brain config list|get|set|unset|path
  配置管理

brain serve [--listen] [--max-concurrent-runs] [--mode] [--workdir] [--run-workdir-policy] [--log-file]
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

当前实现补充说明：

- 具体命令的返回体以各命令节中的示例为准
- 当前 CLI 并没有把所有命令都收敛成一个统一的“Run 对象”输出；例如 `run` summary、`status`、`list`、`replay` 的 JSON 字段并不完全相同

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

### B.6 当前持久化事件（`brain logs`）

当前 `brain logs` 读取的是 `runs.json` 中持久化的运行事件。常见事件类型包括：

- `run.created` / `run.started` / `run.completed` / `run.failed` / `run.cancelled`
- `tool.start` / `tool.end`
- `policy.denied` / `policy.command.denied` / `policy.command.rollback`
- `approval.requested` / `approval.allowed` / `approval.denied`

这些事件至少包含：

- `at`
- `type`
- `message`
- 可选 `data`

---

## 版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.0 | 2026-04-11 | 首版：冻结 13 个子命令 + 20 条合规测试 + 两种模式 + 退出码规范 + 输出格式规范 + stdin 协议 + 环境变量 + 工作目录布局 + 信号处理 + 向后兼容策略 |
