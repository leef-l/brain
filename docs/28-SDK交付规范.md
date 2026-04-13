# 28 · SDK 交付规范 v1

> **状态**：Frozen · v1.0 · 2026-04-11
> **上位规格**：[02-BrainKernel设计.md](./02-BrainKernel设计.md)
> **依赖**：全部下级规格（[20](./20-协议规格.md) / [21](./21-错误模型.md) / [22](./22-Agent-Loop规格.md) / [23](./23-安全模型.md) / [24](./24-可观测性.md) / [25](./25-测试策略.md) / [26](./26-持久化与恢复.md) / [27](./27-CLI命令契约.md)）
> **v3 扩展阅读**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) / [33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md) / [34-Brain-Package与Marketplace规范.md](./34-Brain-Package与Marketplace规范.md)

## 目录

- [1. 动机与范围](#1-动机与范围)
- [2. 术语](#2-术语)
- [3. 兼容性声明的三个层级](#3-兼容性声明的三个层级)
- [4. 三段式版本号](#4-三段式版本号)
- [5. 参考实现与 tiebreaker](#5-参考实现与-tiebreaker)
- [6. SDK 包结构要求](#6-sdk-包结构要求)
- [7. 必须实现的接口清单](#7-必须实现的接口清单)
- [8. 合规测试执行要求](#8-合规测试执行要求)
- [9. 文档交付物](#9-文档交付物)
- [10. CHANGELOG 规则](#10-changelog-规则)
- [11. 发布流程](#11-发布流程)
- [12. 安全披露流程](#12-安全披露流程)
- [13. 商标与命名保护](#13-商标与命名保护)
- [14. 合规测试矩阵 C-SDK-\*](#14-合规测试矩阵-c-sdk-)
- [附录 A · 每种语言的起步模板](#附录-a--每种语言的起步模板)
- [附录 B · 合规测试总览（110 → 145）](#附录-b--合规测试总览110--145)

---

## 1. 动机与范围

### 1.1 为什么要把 SDK 交付写成规格

前面 8 篇规格（02 + 20~27）定义了**行为契约**——Kernel 应该怎么工作、线缆协议长什么样、错误怎么分类、Agent Loop 怎么跑、CLI 命令有哪些。这些规格保证了"**不同 SDK 实现的 Kernel 对外表现是一致的**"。

但规格写得再详细，也有两个问题会导致生态碎片化：

1. **实现不完整**：某个 SDK 只实现了 80% 的接口，却声称"兼容 BrainKernel v1"，用户用起来就会踩到未实现的坑
2. **合规无标准**：没有机制判断一个 SDK 到底算不算"合规实现"，第三方各说各话
3. **版本号混乱**：Protocol 1.0 + Kernel 1.2 + SDK 0.3 这三个版本号到底啥关系？用户不知道该看哪个

本文档解决这三个问题——**把 SDK 怎么算"一等公民"的标准固化下来**，让第三方用同一把尺子自检和互检。

### 1.2 范围

**本规格定义**：

- 兼容性声明的三个层级（Protocol / Kernel / CLI）
- 三段式版本号（Protocol / Kernel / SDK）的语义
- 参考实现与 tiebreaker 机制
- SDK 包的目录结构要求
- 必须实现的接口清单（从 02 + 20~27 提取）
- 合规测试执行要求（145 条）
- 文档交付物（README / API Docs / CHANGELOG / MIGRATION）
- 发布流程与版本管理
- 安全披露流程
- 商标与命名保护
- 当 SDK 同时分发 Specialist Brain 时，与 `Brain Manifest` / `Brain Package` 的衔接建议
- C-SDK-01 ~ C-SDK-20 共 20 条合规测试（测"SDK 包本身"而不是 Kernel 行为）

**本规格不定义**：

- 具体语言的 API 风格（惯用法因语言而异，只要行为对就算合规）
- 构建系统选型（Go 用 Go modules / Python 用 Poetry or PEP 621 / Rust 用 Cargo 等留给各语言自己决定）
- 依赖政策（允许用哪些第三方库留给 SDK 自己，但见 §12 安全披露）
- 性能基准（是 v2 的范畴）

---

## 2. 术语

| 术语 | 定义 |
|------|------|
| **SDK** | 对 BrainKernel v1 的一种语言实现，包含 Kernel 库 + CLI 二进制 |
| **Reference SDK** | 官方 Go 实现（`github.com/leef-l/brain`），作为 tiebreaker |
| **Compliant SDK** | 通过 110 条行为合规测试 + 20 条 CLI 合规测试 + 20 条 SDK 合规测试 = 150 条测试的第三方实现 |
| **Protocol** | stdio 线缆协议（20-协议规格），独立版本号 |
| **Kernel** | Kernel 库实现（02 + 21~26），独立版本号 |
| **CLI** | `brain` 命令行工具（27-CLI命令契约），跟随 SDK 版本号 |
| **Brain Manifest** | v3 Brain 的稳定契约文件，定义 kind / capabilities / runtime / policy / compatibility（见 33） |
| **Brain Package** | v3 Brain 的分发单位，承载 manifest / runtime / bindings / license / metadata（见 34） |
| **Tiebreaker** | 规格出现歧义时，以参考实现的实际行为为准的原则 |

---

## 3. 兼容性声明的三个层级

SDK 声称"兼容 BrainKernel v1" 时，**MUST** 明确声明以下三个层级各自的兼容性：

### 3.1 Protocol 级兼容

**含义**：实现了 20-协议规格.md 定义的 stdio 线缆协议，可以与其他 SDK 实现的 sidecar 互操作。

**验证条件**：

- 通过 C-*（协议层，20 条）
- 通过 C-E-*（错误模型，20 条）

**能做什么**：用该 SDK 实现的 sidecar 可以被其他 SDK 实现的 Kernel 宿主调用，反之亦然。

**适用场景**：只想写一个第三方 brain（例如 Rust 写的 high-performance code_brain），接入到官方 Go Kernel 里。

### 3.2 Kernel 级兼容

**含义**：实现了完整的 Kernel 运行时（Agent Loop / Tool Registry / PlanStore / ArtifactStore / Trace Logger / Sandbox / Vault / LLMAccess 三模式），可以作为 Kernel 宿主运行任何第三方 sidecar。

**验证条件**：包含 Protocol 级的 40 条，加上：

- 通过 C-L-*（Agent Loop，20 条）
- 通过 C-S-*（安全模型，20 条）
- 通过 C-O-*（可观测性，15 条）
- 通过 C-P-*（持久化与恢复，15 条）

**能做什么**：该 SDK 可以作为独立的 Kernel 进程，替代官方 Go Kernel。

**适用场景**：想要一个 Python SDK 跑在 AWS Lambda 里（Go 二进制冷启动太慢），完全替代官方 Kernel。

### 3.3 CLI 级兼容

**含义**：在 Kernel 级兼容基础上，提供 27-CLI命令契约.md 定义的完整 `brain` 命令行工具。

**验证条件**：包含 Kernel 级的 110 条，加上：

- 通过 C-CLI-*（CLI 命令契约，20 条）
- 通过 C-SDK-*（SDK 包规范，20 条）

**能做什么**：用户可以把官方 `brain` 二进制替换成该 SDK 的 `brain` 二进制，所有脚本、CI、文档保持不变。

**适用场景**：想要提供完整的可替换 BrainKernel 发行版（例如嵌入特定 OS 的包管理器、企业定制发行版）。

### 3.4 兼容性声明表达

SDK 的 README.md / package metadata 里 **MUST** 包含类似以下语句：

```markdown
## Compatibility

- **Protocol**: v1.0 ✓
- **Kernel**:   v1.0 ✓ (passes C-L-* / C-S-* / C-O-* / C-P-*)
- **CLI**:      v1.0 ✓
- **Reference**: github.com/leef-l/brain@v1.0.0 (tiebreaker)
```

如果只实现部分，必须诚实标注（例如 "Protocol ✓, Kernel ✗, CLI ✗"），**MUST NOT** 模糊其词。

---

## 4. 三段式版本号

### 4.1 三个独立版本号

BrainKernel 生态有三个独立演化的版本号：

| 版本号 | 定义来源 | 含义 |
|--------|----------|------|
| **Protocol Version** | 20-协议规格.md §2 | stdio 线缆协议的版本（字节级契约） |
| **Kernel Version** | 02 宪法 + 21~26 | Kernel 行为契约的版本 |
| **SDK Version** | 各 SDK 自定义 | SDK 包本身的 semver |

### 4.2 版本号递进规则

#### 4.2.1 Protocol Version

格式：`major.minor`（**不带 patch**，protocol 不打补丁）。

- **major bump**：breaking change，不向下兼容（例如换序列化格式、删除字段语义）
- **minor bump**：向下兼容的扩展（例如新增帧类型、新增可选字段）
- Protocol v1.0 的生命周期内 MUST NOT 有 major bump

Protocol version 通过 `initialize` 帧协商（见 20 §4）。双方选 `min(host.protocol_version, sidecar.protocol_version)`。

#### 4.2.2 Kernel Version

格式：`major.minor.patch`（标准 semver）。

- **major bump**：行为契约 breaking change（例如改变 Budget 计算规则、改变 Turn 边界定义）
- **minor bump**：向下兼容的行为扩展（例如新增合规测试、新增可选接口）
- **patch bump**：bug 修复、文档更正、不影响合规测试的内部优化

每个 Kernel 版本都 MUST 声明 "compatible with Protocol v1.0" 之类的 compatibility 矩阵。

#### 4.2.3 SDK Version

格式：`major.minor.patch`（标准 semver）。

- 各 SDK 的 version 独立演化，不和 Kernel version 绑定
- SDK 1.2.3 可能实现的是 Kernel 1.1.0 的行为
- SDK 必须声明：`SDK vX.Y.Z implements Kernel vA.B.C, Protocol va.b`

### 4.3 兼容性矩阵示例

```
SDK 1.0.0 (go)    → Protocol 1.0 ✓ / Kernel 1.0.0 ✓ / CLI 1.0 ✓
SDK 1.1.0 (go)    → Protocol 1.0 ✓ / Kernel 1.0.0 ✓ / CLI 1.0 ✓  (SDK bug fixes only)
SDK 1.2.0 (go)    → Protocol 1.0 ✓ / Kernel 1.1.0 ✓ / CLI 1.0 ✓  (adopts new Kernel minor)
SDK 2.0.0 (go)    → Protocol 2.0 ✓ / Kernel 2.0.0 ✓ / CLI 2.0 ✓  (breaking change)

SDK 0.9.0 (python) → Protocol 1.0 ✓ / Kernel 1.0.0 ✓ / CLI 1.0 ✗  (early preview, CLI not ready)
SDK 1.0.0 (python) → Protocol 1.0 ✓ / Kernel 1.0.0 ✓ / CLI 1.0 ✓  (first stable)
```

### 4.4 cassette replay 一致性

Patch bump（例如 SDK 1.2.3 → 1.2.4）**MUST** 通过 cassette replay 一致性测试：

- 用 1.2.3 录制的 cassette 放进 1.2.4 replay 必须产生完全相同的输出
- 见 25-测试策略.md §7 cassette 机制

如果 patch bump 打破了 cassette 一致性，说明不是 bug 修复而是行为改变——MUST 升级为 minor bump。

### 4.5 版本号发布声明文件

每个 SDK 发行包 **MUST** 在根目录提供 `VERSION.json`：

```json
{
  "sdk_version": "1.0.0",
  "sdk_language": "go",
  "kernel_version": "1.0.0",
  "protocol_version": "1.0",
  "cli_version": "1.0.0",
  "compatibility": {
    "protocol": true,
    "kernel": true,
    "cli": true
  },
  "reference_sdk": "github.com/leef-l/brain@v1.0.0",
  "compliance_tests": {
    "total": 150,
    "passed": 150,
    "skipped": 0,
    "failed": 0,
    "last_run_at": "2026-04-10T12:00:00Z",
    "last_run_commit": "a1b2c3d"
  }
}
```

`brain version --json` 读取这个文件作为数据源（见 27 §17.3）。

---

## 5. 参考实现与 tiebreaker

### 5.1 参考实现的定位

**官方 Go SDK**（`github.com/leef-l/brain`）是 **Reference SDK**，作用是：

- **Tiebreaker**：当规格出现歧义时，以 Reference SDK 的实际行为为准
- **Cassette master**：Reference SDK 录制的 cassette 是跨语言 replay 测试的 golden master
- **合规测试仓库维护方**：150 条合规测试的规范实现由 Reference SDK 维护

### 5.2 什么不是 tiebreaker

**Reference SDK 不是 "一等公民"**。具体来说：

- 其他 SDK 通过 150 条合规测试后，与 Reference SDK **地位平等**，可以声称 "CLI 级兼容"
- Reference SDK 的代码风格、API 命名、构建工具不对其他 SDK 有约束力
- Reference SDK 自己的 bug 如果和规格冲突，以规格为准（同时提 issue 修 Reference SDK）

### 5.3 tiebreaker 的触发条件

只有当以下两个条件**同时**满足时，才能援引 tiebreaker：

1. 规格文档（02 + 20~27）对某个场景**有明显歧义或沉默**
2. 实际实现时需要做出选择，但任一选择都和规格不冲突

不满足这两个条件的情况 MUST NOT 援引 tiebreaker：

- 规格明确写了规则 → 按规格走，Reference SDK 的 bug 不算数
- 规格没明确但 common sense 明显 → 按 common sense 走
- 规格有多解但只有一种和其他条款一致 → 按一致解走

### 5.4 歧义报告流程

发现规格歧义时，SDK 实现者 **SHOULD**：

1. 在规格文档仓库提 issue，标签 `spec/ambiguity`
2. 描述场景 + 援引 Reference SDK 的处理方式
3. 等待规格维护者在下一个规格 patch 中明确

在规格明确前，实现者可以临时采用 Reference SDK 的行为并在自己的 SDK 文档中标注 `⚠ tiebreaker: <issue-url>`。

---

## 6. SDK 包结构要求

### 6.1 必选目录

任何 SDK 包 **MUST** 在根目录提供以下文件/目录：

```
<sdk-root>/
├── README.md                # 必选：兼容性声明 + 快速开始
├── LICENSE                  # 必选：OSI 批准的开源许可证
├── VERSION.json             # 必选：§4.5 的版本声明
├── CHANGELOG.md             # 必选：§10 的变更日志
├── SECURITY.md              # 必选：§12 的安全披露流程
├── docs/
│   ├── compatibility.md     # 必选：§3 的兼容性声明细节
│   ├── migration/           # 可选：版本迁移指南
│   └── api/                 # 必选：API 参考文档（自动生成或手写）
├── tests/
│   ├── compliance/          # 必选：150 条合规测试的本地副本
│   │   ├── protocol/        # C-*
│   │   ├── error/           # C-E-*
│   │   ├── loop/            # C-L-*
│   │   ├── security/        # C-S-*
│   │   ├── observability/   # C-O-*
│   │   ├── persistence/     # C-P-*
│   │   ├── cli/             # C-CLI-*
│   │   └── sdk/             # C-SDK-*
│   └── cassettes/           # 必选：cassette replay 测试的本地副本
└── <language-specific>/     # 各语言自己的源码目录（src/ / lib/ / pkg/ 等）
```

如果一个 SDK 还同时分发官方或第三方 Specialist Brain，**SHOULD** 额外提供：

```text
<sdk-root>/
└── brains/
    ├── manifests/          # 推荐：示例或内置 brain 的 manifest
    └── packages/           # 可选：brain package 构建或安装脚本
```

这些目录不是 CLI/Kernel 合规的硬要求，但与 v3 的 Brain 生态对齐时非常有用。

### 6.2 README.md 必选章节

```markdown
# <SDK 名称>

## Compatibility
<§3.4 的兼容性声明块>

## Installation
<各语言的安装命令>

## Quick Start
<一个最小可运行示例，展示 brain run "hello world" 流程>

## Compliance
- 150 compliance tests passing (see `tests/compliance/`)
- Last cassette replay: <date> against Reference SDK <version>

## License
<SPDX 标识符>

## Security
See [SECURITY.md](./SECURITY.md) for vulnerability reporting.
```

### 6.3 LICENSE 要求

- **MUST** 使用 OSI 批准的开源许可证（Apache-2.0 / MIT / BSD-2 / BSD-3 / MPL-2.0 等）
- **MUST NOT** 使用非 OSI 许可证声称合规（例如 SSPL / BSL / custom non-free）
- 推荐 Apache-2.0（与 Reference SDK 一致，便于代码互通）

### 6.4 目录禁忌

下列内容 **MUST NOT** 出现在 SDK 根目录或 tests/ 下：

- 硬编码的 API key / 凭证
- 真实用户的 PII 数据
- Reference SDK 的代码拷贝（license 允许的除外，但必须 `vendor/` 隔离）
- 大于 50MB 的二进制（cassette 除外，cassette 有 §6.5 压缩要求）

### 6.5 cassette 格式

- cassette 文件 **MUST** 使用 `.cassette.json.gz` 后缀
- **MUST** 是有效的 gzip 压缩的 NDJSON（每行一个事件）
- **MUST** 包含元信息头：`{"type":"cassette.meta","version":"1.0","recorded_with":"go/1.0.0","recorded_at":"..."}`
- 单个 cassette 文件 **SHOULD** 不超过 10MB（解压后）

---

## 7. 必须实现的接口清单

下列接口是 Kernel 级兼容的最低要求（CLI 级兼容在此基础上加 27 的全部命令）。**MUST** 全部实现，MAY 提供额外的语言友好 API。

### 7.1 来自 02 宪法

| 接口 | 来自 | 用途 |
|------|------|------|
| `BrainAgent` | §3 | agent 抽象基类 |
| `BrainTransport` | §12 | stdio transport |
| `BrainRunner` | §12.5 | sidecar 运行器 |
| `LLMProvider` | §5 | LLM 调用抽象 |
| `ToolRegistry` | §6 | 工具注册表 |
| `ArtifactStore` | §7 | CAS artifact 存储 |
| `PlanStore` | §8 | 计划持久化 |
| `TraceLogger` / `AuditLogger` | §11 | trace/audit |

### 7.2 来自 21 错误模型

| 接口 | 用途 |
|------|------|
| `BrainError` 结构 | 4 维 Class 错误分类 |
| `Decide(err)` | 重试决策函数 |
| `Fingerprint(err)` | 错误去重指纹 |

### 7.3 来自 22 Agent Loop

| 接口 | 用途 |
|------|------|
| `RunState` | Run 生命周期状态机 |
| `TurnExecutor` | Turn 执行器 |
| `Budget` | 4 层 Budget 追踪 |
| `PromptCacheBuilder` | 三层 cache_control |
| `StreamConsumer` | 流式事件消费者接口 |
| `ToolResultSanitizer` | tool_result 清洗管道 |
| `LoopDetector` | 循环检测器 |

### 7.4 来自 23 安全模型

| 接口 | 用途 |
|------|------|
| `Vault` | 凭证管理 |
| `Sandbox` | 4 维沙箱 |
| `LLMAccessStrategy` | proxied/direct/hybrid |
| `AuditEvent` | 审计事件 |

### 7.5 来自 24 可观测性

| 接口 | 用途 |
|------|------|
| `MetricsRegistry` | 指标注册 |
| `TraceExporter` | OTel trace 导出器 |
| `LogExporter` | 结构化日志导出器 |

### 7.6 来自 25 测试策略

| 接口 | 用途 |
|------|------|
| `ComplianceRunner` | 跑 150 条合规测试的运行器 |
| `CassetteRecorder` | cassette 录制器 |
| `CassettePlayer` | cassette 回放器 |
| `FakeSidecar` | 测试用 fake sidecar |

### 7.7 来自 26 持久化与恢复

| 接口 | 用途 |
|------|------|
| `RunCheckpointStore` | Run checkpoint 存储 |
| `BrainPlanStore` | plan 快照 + delta |
| `UsageLedger` | LLM 调用计费记录 |
| `ArtifactMetaStore` | CAS 元数据 |
| `ResumeCoordinator` | Run 恢复协议 |

### 7.8 来自 27 CLI 命令契约

CLI 级兼容必须实现全部 13 个子命令（见 27 §5）+ 全局选项 + 退出码 + 输出格式。

### 7.9 接口命名的语言适配

各语言 SDK **MAY** 根据惯用法调整接口命名：

- Go：`PlanStore` interface
- Python：`PlanStore` abstract base class 或 `Protocol`
- Rust：`trait PlanStore`
- TypeScript：`interface PlanStore`

只要行为一致即可，**不要求**字面完全相同。

### 7.10 可选扩展的规则

SDK **MAY** 提供规格之外的增强接口（例如 ORM 风格的 Query Builder、GraphQL 风格的 API），但：

- 扩展接口 **MUST** 放在独立模块（`<sdk>/extras/` 或 `<sdk>/experimental/`）
- 扩展接口 **MUST NOT** 影响 150 条合规测试的通过
- 扩展接口 **SHOULD** 在文档中标注 "non-standard, not portable across SDKs"

---

## 8. 合规测试执行要求

### 8.1 总数

| 类别 | 来源 | 条数 |
|------|------|-----:|
| C-* | 20-协议规格 | 20 |
| C-E-* | 21-错误模型 | 20 |
| C-L-* | 22-Agent-Loop | 20 |
| C-S-* | 23-安全模型 | 20 |
| C-O-* | 24-可观测性 | 15 |
| C-P-* | 26-持久化与恢复 | 15 |
| C-CLI-* | 27-CLI命令契约 | 20 |
| C-SDK-* | 28（本规格） | 20 |
| **合计** | | **150** |

### 8.2 通过率要求

| 兼容性声明 | 最低通过率 |
|-----------|-----------|
| Protocol 级 | C-* + C-E-* 全部通过（40/40） |
| Kernel 级 | C-* + C-E-* + C-L-* + C-S-* + C-O-* + C-P- 全部通过（110/110） |
| CLI 级 | 150 条全部通过（150/150） |

**MUST NOT** 有 `skipped` 测试。如果某条测试在当前语言不适用（例如某个并发原语在 Python GIL 下没有意义），**MUST** 在 `VERSION.json` 中显式标注 `skipped_reason`，而不是沉默跳过。

### 8.3 测试运行环境

合规测试 **MUST** 能够在以下环境下运行：

- **Linux x86_64**（必选）
- **macOS ARM64**（SHOULD）
- **Linux ARM64**（SHOULD）
- **Windows**（MAY，非必选）

### 8.4 CI 集成要求

每个 SDK 的主分支 **MUST** 在每次 commit 跑合规测试，并把结果写入 `VERSION.json`。

推荐使用 GitHub Actions / GitLab CI 等公开 CI，使结果可验证。

### 8.5 cross-lang 测试

对于 protocol 级兼容的 SDK，**MUST** 额外跑 cross-lang 测试：

- 用本 SDK 实现的 sidecar + Reference SDK Kernel 宿主
- 用本 SDK 实现的 Kernel 宿主 + Reference SDK sidecar
- 两个方向都要通过协议级合规测试

cross-lang 测试的自动化工具由 Reference SDK 提供（`brain-go/cmd/crosslang-tester/`）。

### 8.6 合规报告格式

合规测试运行完成后，**MUST** 产出一份 JSON 格式的合规报告 `compliance-report.json`：

```json
{
  "sdk_language": "python",
  "sdk_version": "1.0.0",
  "kernel_version": "1.0.0",
  "protocol_version": "1.0",
  "run_at": "2026-04-10T12:00:00Z",
  "run_commit": "a1b2c3d",
  "environment": {
    "os": "linux",
    "arch": "amd64",
    "python_version": "3.12.2"
  },
  "results": {
    "C-01":   { "status": "pass", "duration_ms": 12 },
    "C-02":   { "status": "pass", "duration_ms": 8 },
    ...
    "C-SDK-20": { "status": "pass", "duration_ms": 45 }
  },
  "summary": {
    "total": 150,
    "passed": 150,
    "failed": 0,
    "skipped": 0
  }
}
```

---

## 9. 文档交付物

### 9.1 必选文档

| 文档 | 来源 | 内容 |
|------|------|------|
| `README.md` | §6.2 | 入口说明 + 兼容性声明 + quick start |
| `CHANGELOG.md` | §10 | 每个版本的变更 |
| `SECURITY.md` | §12 | 安全披露流程 |
| `docs/compatibility.md` | §3 | 兼容性声明细节 |
| `docs/api/` | — | API 参考（可自动生成） |

### 9.2 推荐文档

| 文档 | 内容 |
|------|------|
| `docs/architecture.md` | SDK 内部架构概览（帮助贡献者） |
| `docs/migration/v1.0-to-v1.1.md` | 跨版本迁移指南（minor bump 时写） |
| `docs/examples/` | 常见用法示例 |
| `docs/performance.md` | 基准测试结果（非合规要求） |
| `CONTRIBUTING.md` | 贡献指南 |
| `docs/brains/manifest.md` | 如果 SDK 分发 Specialist Brain，建议记录 Manifest schema 与示例 |
| `docs/brains/package.md` | 如果 SDK 分发 Specialist Brain，建议记录 Package 布局、安装和升级规则 |

### 9.3 API 文档自动化

各语言 SDK **SHOULD** 使用语言原生工具生成 API 文档：

- Go：`godoc` / `pkgsite`
- Python：`sphinx` / `mkdocs`
- Rust：`cargo doc`
- TypeScript：`typedoc`

**MUST** 为每个公开的 接口 / 方法 / 类 写 docstring（语言惯用格式）。

### 9.4 示例代码要求

`docs/examples/` **SHOULD** 至少包含以下 5 个可运行示例：

1. `hello_world/`：最小的 `brain run "hello"` 调用
2. `custom_brain/`：实现一个自定义 sidecar brain
3. `custom_tool/`：注册一个自定义工具
4. `resume_run/`：演示 Run 恢复
5. `cassette_record/`：录制 + 回放 cassette

每个示例 **MUST** 可以用不超过 3 行命令跑起来。

---

## 10. CHANGELOG 规则

### 10.1 格式

**MUST** 遵循 [Keep a Changelog](https://keepachangelog.com/) 规范，并额外加入 BrainKernel 特有字段。

### 10.2 每个版本必选字段

```markdown
## [1.2.0] - 2026-06-15

**Compatibility**:
- Protocol: 1.0 (unchanged)
- Kernel:   1.1.0 (was 1.0.0, see Kernel CHANGELOG)
- CLI:      1.0 (unchanged)

**Compliance**: 150/150 passing (report: sha256:abc...)

### Added
- Support for xyz
### Changed
- ...
### Fixed
- ...
### Security
- ...
### Deprecated
- ...
### Removed
- ...
```

### 10.3 breaking change 标注

任何 breaking change **MUST** 在 CHANGELOG 顶部用 `⚠ BREAKING` 标签高亮：

```markdown
## [2.0.0] - 2027-01-10

⚠ BREAKING: Protocol upgraded from v1.0 to v2.0
⚠ BREAKING: Removed deprecated `brain exec` command (use `brain run`)
```

---

## 11. 发布流程

### 11.1 发布前检查清单

每次发布 **MUST** 验证：

- [ ] 150 条合规测试全部通过（`compliance-report.json` 已更新）
- [ ] `VERSION.json` 已同步更新
- [ ] `CHANGELOG.md` 已写新版本条目
- [ ] 跨平台构建成功（Linux x86_64 + macOS ARM64 最低）
- [ ] cross-lang 测试通过（protocol 级及以上）
- [ ] SECURITY.md 无未处理的安全披露
- [ ] API 文档已重新生成
- [ ] `brain version` 输出匹配 `VERSION.json`

### 11.2 版本号 tag

Git tag **MUST** 符合 `v<sdk_version>` 格式：

- `v1.0.0` ✓
- `v1.0.0-rc.1` ✓（pre-release）
- `v1.0.0+build.5` ✗（SemVer build metadata 不推荐）
- `release-1.0` ✗

### 11.3 artifact 签名

发行二进制 **MUST** 提供以下签名证据之一：

- **GPG 签名**：`.sig` 文件 + 公钥在官网
- **Sigstore / cosign**：keyless 签名
- **Release attestation**（GitHub Actions artifact attestation）

**MUST NOT** 发布无签名的二进制。

### 11.4 发布渠道

每种语言推荐的官方渠道：

| 语言 | 渠道 |
|------|------|
| Go | GitHub Releases + Go module proxy |
| Python | PyPI |
| Rust | crates.io |
| TypeScript | npm + GitHub Packages |

**MUST NOT** 在 PyPI / npm 等渠道占用 `brain` / `brainkernel` / `brain-kernel` 等名字给非 Reference SDK 的实现（除非官方授权，见 §13）。

如果发布的是 `Brain Package` 而不是整个 SDK，分发建议转到：

- [34-Brain-Package与Marketplace规范.md](./34-Brain-Package与Marketplace规范.md)

也就是：

- SDK 继续走语言生态渠道
- Specialist Brain 建议走 GitHub Releases / Marketplace / 安装器渠道

---

## 12. 安全披露流程

### 12.1 必选文件

SDK **MUST** 在根目录提供 `SECURITY.md`，包含：

- 支持的版本（哪些版本还在接收安全补丁）
- 漏洞报告邮箱或 URL（**MUST NOT** 是 public issue tracker）
- 响应 SLA（initial response 时间 + fix SLA）
- PGP 公钥（推荐）

### 12.2 漏洞响应 SLA

Reference SDK 的 SLA 标准（其他 SDK **SHOULD** 不低于此）：

| 严重程度 | Initial Response | Fix SLA |
|---------|-----------------|---------|
| Critical（远程代码执行、凭证泄露） | 24h | 7 天 |
| High（沙箱逃逸、权限提升） | 48h | 14 天 |
| Medium（信息泄露、DoS） | 72h | 30 天 |
| Low（微弱影响） | 7 天 | 90 天 |

### 12.3 协调披露

严重漏洞 **MUST** 走协调披露流程：

1. 报告者提交漏洞
2. SDK 维护者确认 + 打补丁
3. 同步通知其他 SDK 维护者（如果漏洞跨 SDK 影响）
4. 统一发布补丁版本 + CVE
5. 披露原始报告

**MUST NOT** 在补丁发布前公开漏洞细节。

### 12.4 CVE 编号

凡是影响 **Kernel 级兼容** 的漏洞，**MUST** 申请 CVE 编号（通过 GitHub Security Advisory 或 MITRE）。

---

## 13. 商标与命名保护

### 13.1 保留名称

以下名称在公开包注册表中 **MUST** 只由 EasyMVP 官方或其授权方使用：

- `brain`
- `brainkernel`
- `brain-kernel`
- `brain-sdk`
- `easymvp-brain`

其他 SDK **MUST NOT** 在 PyPI / npm / crates.io 等注册表上占用这些名称。

### 13.2 允许的命名格式

第三方 SDK **MUST** 使用区分性命名：

- `brain-<organization>`（例如 `brain-acme`）
- `<organization>-brain-sdk`（例如 `acme-brain-sdk`）
- `brain-kernel-<language>-<vendor>`（例如 `brain-kernel-rust-acme`）

### 13.3 CLI 二进制名

**例外**：CLI 二进制 **MUST** 命名为 `brain`（见 27 §3.1），这是为了保证用户无缝替换。包注册表名和 CLI 二进制名是两回事。

具体做法：

- 包名：`acme-brain-sdk`（注册表名字）
- 安装后的二进制：`brain`（标准命令名）
- 用户：`pip install acme-brain-sdk && brain run "..."`

这种设计与 `coreutils` / `busybox` 等包名 ≠ 命令名的先例一致。

### 13.4 文档声明要求

第三方 SDK 的 README **MUST** 包含：

```markdown
> This is an independent implementation of the BrainKernel v1 specification.
> "BrainKernel" is a trademark of EasyMVP. This project is not affiliated with
> or endorsed by EasyMVP beyond compliance with the public specification.
```

---

## 14. 合规测试矩阵 C-SDK-*

| ID | 测试项 | 期望 |
|----|--------|------|
| C-SDK-01 | 根目录有 `README.md` / `LICENSE` / `VERSION.json` / `CHANGELOG.md` / `SECURITY.md` | 5 个文件都存在 |
| C-SDK-02 | `LICENSE` 是 OSI 批准的许可证 | SPDX 标识符在 OSI 列表中 |
| C-SDK-03 | `VERSION.json` 格式合法 | 所有必选字段存在 + schema 正确 |
| C-SDK-04 | `VERSION.json` 中 `compliance_tests.passed == total` | 真实通过 |
| C-SDK-05 | `README.md` 包含 Compatibility 声明块 | §3.4 的格式 |
| C-SDK-06 | `CHANGELOG.md` 遵循 Keep a Changelog 格式 | 顶部有 `## [x.y.z]` 条目 |
| C-SDK-07 | `tests/compliance/` 目录下有 8 个子类别 | protocol/error/loop/security/observability/persistence/cli/sdk |
| C-SDK-08 | cassette 文件是 `.cassette.json.gz` 格式 | 所有 cassette 符合 |
| C-SDK-09 | 根目录无硬编码 API key | grep 扫描通过 |
| C-SDK-10 | 根目录无真实用户 PII | 正则扫描通过 |
| C-SDK-11 | 扩展接口在 `extras/` 或 `experimental/` | 扩展接口不污染核心 |
| C-SDK-12 | `brain version --json` 输出匹配 `VERSION.json` | 字段一致 |
| C-SDK-13 | 150 条合规测试实际可运行（不只是列表） | 跑一次全部 pass |
| C-SDK-14 | cross-lang 测试通过（vs Reference SDK） | protocol 级或以上必选 |
| C-SDK-15 | 包名不占用 §13.1 保留名称 | 包注册表名称合规 |
| C-SDK-16 | README 包含 §13.4 的商标声明 | 声明存在 |
| C-SDK-17 | `SECURITY.md` 包含邮箱或 URL + SLA | 必选字段存在 |
| C-SDK-18 | 二进制有签名 | GPG/cosign/attestation 三选一 |
| C-SDK-19 | 无规格未覆盖的自定义 breaking behavior | 与规格一致或明确标注 non-standard |
| C-SDK-20 | `compliance-report.json` 格式合法且 fresh（<30 天） | 真实存在 + 时间新 |

### 14.1 C-SDK-* 的测试驱动

这 20 条测试是"SDK 包本身的静态检查"，不需要启动 Kernel。实现方式：

- 一个通用 Python 脚本 `check_sdk_compliance.py`（由 Reference SDK 提供）
- 读取 SDK 根目录，按 C-SDK-01 ~ 20 逐项验证
- 输出 `sdk-compliance-report.json`

各语言 SDK 可以选择跑 Python 脚本，或用本语言重写（只要产出相同格式的 report）。

---

## 附录 A · 每种语言的起步模板

以下是 Reference SDK 推荐的起步结构（非强制，仅示例）：

### A.1 Go

```
brain-go/
├── README.md
├── LICENSE
├── VERSION.json
├── CHANGELOG.md
├── SECURITY.md
├── go.mod
├── go.sum
├── docs/
├── cmd/
│   └── brain/              # CLI 二进制
│       └── main.go
├── pkg/
│   ├── kernel/             # Kernel 核心
│   ├── protocol/           # 20 协议
│   ├── errors/             # 21 错误模型
│   ├── loop/               # 22 Agent Loop
│   ├── security/           # 23 安全
│   ├── observability/      # 24 可观测性
│   ├── persistence/        # 26 持久化
│   └── cli/                # 27 CLI
├── internal/               # 不对外的实现细节
├── tests/
│   ├── compliance/
│   └── cassettes/
└── extras/                 # §7.10 扩展接口
```

### A.2 Python

```
brain-py/
├── README.md
├── LICENSE
├── VERSION.json
├── CHANGELOG.md
├── SECURITY.md
├── pyproject.toml
├── docs/
├── src/
│   └── brain/
│       ├── __init__.py
│       ├── __main__.py     # python -m brain 入口
│       ├── cli/            # 27 CLI
│       ├── kernel/
│       ├── protocol/
│       ├── errors/
│       ├── loop/
│       ├── security/
│       ├── observability/
│       ├── persistence/
│       └── extras/
├── tests/
│   ├── compliance/
│   └── cassettes/
└── scripts/
    └── brain               # shim，entry_points 入口
```

### A.3 Rust

```
brain-rs/
├── README.md
├── LICENSE
├── VERSION.json
├── CHANGELOG.md
├── SECURITY.md
├── Cargo.toml
├── Cargo.lock
├── docs/
├── crates/
│   ├── brain-kernel/       # 库
│   ├── brain-protocol/
│   ├── brain-errors/
│   ├── brain-loop/
│   ├── brain-security/
│   ├── brain-observability/
│   ├── brain-persistence/
│   ├── brain-cli/          # bin target "brain"
│   └── brain-extras/
├── tests/
│   ├── compliance/
│   └── cassettes/
```

### A.4 TypeScript

```
brain-ts/
├── README.md
├── LICENSE
├── VERSION.json
├── CHANGELOG.md
├── SECURITY.md
├── package.json
├── tsconfig.json
├── docs/
├── src/
│   ├── index.ts
│   ├── bin/
│   │   └── brain.ts        # CLI 入口
│   ├── kernel/
│   ├── protocol/
│   ├── errors/
│   ├── loop/
│   ├── security/
│   ├── observability/
│   ├── persistence/
│   ├── cli/
│   └── extras/
├── tests/
│   ├── compliance/
│   └── cassettes/
```

---

## 附录 B · 合规测试总览（110 → 145 → 150）

| 阶段 | 总数 | 覆盖规格 | 说明 |
|-----|-----:|----------|------|
| Round 1~3 | 110 | 20/21/22/23/24/26 | 行为契约合规 |
| Round 4（本规格 + 27） | 150 | + 27/28 | CLI + SDK 包规范 |

注：25-测试策略.md 自身不提供合规测试条目，它是"如何跑合规测试"的元规格。

### B.1 完整编号范围

| 前缀 | 来源 | 条数 | 编号范围 |
|------|------|-----:|---------|
| C-* | 20-协议规格 | 20 | C-01 ~ C-20 |
| C-E-* | 21-错误模型 | 20 | C-E-01 ~ C-E-20 |
| C-L-* | 22-Agent-Loop | 20 | C-L-01 ~ C-L-20 |
| C-S-* | 23-安全模型 | 20 | C-S-01 ~ C-S-20 |
| C-O-* | 24-可观测性 | 15 | C-O-01 ~ C-O-15 |
| C-P-* | 26-持久化与恢复 | 15 | C-P-01 ~ C-P-15 |
| C-CLI-* | 27-CLI命令契约 | 20 | C-CLI-01 ~ C-CLI-20 |
| C-SDK-* | 28-SDK交付规范 | 20 | C-SDK-01 ~ C-SDK-20 |
| **合计** | | **150** | |

### B.2 最小合规条件速查

| 想要声称的兼容性 | 必须通过的测试 | 数量 |
|-----------------|--------------|-----:|
| "实现了 BrainKernel Protocol v1" | C-* + C-E-* | 40 |
| "实现了 BrainKernel v1" | + C-L-* + C-S-* + C-O-* + C-P-* | 110 |
| "实现了完整 BrainKernel v1 发行版" | + C-CLI-* + C-SDK-* | 150 |

---

## 版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| v1.0 | 2026-04-11 | 首版：冻结三级兼容性声明 + 三段式版本号 + 参考实现 tiebreaker 机制 + SDK 包结构 + 必须实现的接口清单 + 150 条合规测试总览 + 发布流程 + 安全披露 SLA + 商标保护 |
