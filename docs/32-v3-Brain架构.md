# 32. v3 Brain 架构

> 目标：为未来 3-5 年冻结一套稳定的 Specialist Brain 顶层模型，
> 让 `native brain`、`MCP-backed brain`、`hybrid brain`、`remote brain`
> 都能在同一世界观下共存，同时不把产品语义绑死在 MCP、tool 或 package 上。

---

## 1. 设计结论

v3 推荐正式采用这一套顶层架构：

- **Brain-first**
  顶层产品对象永远是 `Brain`
- **Manifest-driven**
  Brain 的稳定契约由 `Brain Manifest` 描述
- **Runtime-pluggable**
  Brain 的运行实现由 `Brain Runtime` 决定
- **Package-delivered**
  Brain 的发布、安装、授权、升级通过 `Brain Package`
- **Policy-governed**
  权限、工具暴露、授权、健康门禁统一收敛在 policy 层

一句话概括：

> **用户安装的是 package，Kernel 调度的是 brain，运行依赖 runtime，稳定契约由 manifest 定义。**

这比 `MCP-first`、`Tool-first`、`Package-first` 都更稳，更适合 3-5 年演化。

---

## 2. 为什么不用其他顶层模型

### 2.1 不是 Tool-first

“一切都是 tool” 的问题是：

- central 只能看到一堆能力，无法看到一个完整角色
- delegate、健康检查、授权、版本、审计都会碎在工具层
- 无法形成真正的专精大脑生态

tool 是 capability 的承载，不是产品对象。

### 2.2 不是 MCP-first

“一切都是 MCP” 的问题是：

- `MCP server` 本身不等于 brain
- MCP server 通常没有 `brain/execute`
- 它不负责角色提示词、计划、license、health、delegate 语义
- 把顶层概念绑死在 MCP，会让未来 `native` / `remote` / `hybrid` 模式失去对称性

MCP 很重要，但它应该是 runtime/binding 层，而不是整个系统的顶层世界观。

### 2.3 不是 Package-first

“一切都是 package” 的问题是：

- package 更像分发单位，不是运行时对象
- central 不应该 delegate 给一个“包”
- 同一个 brain 未来可能有多个 package/edition/runtime 变体

package 是交付层，不是调度层。

### 2.4 不是 Capability-first

完全 capability-first 的世界观更适合内核内部做智能路由，不适合作为第三方开发者的第一层认知。

原因：

- 对外会变得抽象，不利于理解“我要开发一个什么东西”
- 用户更容易理解 `browser brain`、`security brain`，而不是一组 capability graph
- capability 更适合做 routing 信号，而不是顶层产品命名

所以 v3 里 capability 仍然很重要，但它是**内部路由维度**，不是顶层产品对象。

---

## 3. v3 的五个核心概念

### 3.1 Brain

`Brain` 是 v3 的唯一顶层产品对象。

它表示一个可被 Kernel/central 识别、调度、健康检查、授权、计量、审计的专精执行单元。

对外约束：

- central **只 delegate 给 brain**
- orchestrator **只管理 brain**
- 用户安装、启用、禁用、升级的都是某个 brain

### 3.2 Brain Manifest

`Brain Manifest` 是 brain 的稳定契约文件。

它描述：

- brain 是什么
- 适合做什么
- 具备哪些 capability
- 采用什么 runtime
- 需要哪些 policy / scope / license
- 兼容哪些 protocol / kernel

manifest 的职责是“声明”，不是“执行”。

具体 schema 见：

- [33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md)

### 3.3 Brain Runtime

`Brain Runtime` 定义这个 brain 怎么跑。

v3 预计支持四类 runtime：

- `native`
- `mcp-backed`
- `hybrid`
- `remote`

runtime 是实现层，可替换，但不改变顶层 brain 身份。

### 3.4 Brain Package

`Brain Package` 是分发与安装单位。

它负责：

- 二进制或远程配置的交付
- manifest 打包
- 版本元数据
- 签名
- license
- 升级与兼容信息

package 可以承载商业化，但不参与 delegate 语义。

Package 与 Marketplace 的分发规则见：

- [34-Brain-Package与Marketplace规范.md](./34-Brain-Package与Marketplace规范.md)

### 3.5 Capability

`Capability` 是 brain 的能力标签与路由信号。

它用于：

- central 的候选脑筛选
- marketplace 搜索
- policy 限制
- 未来 capability-based routing

它不取代 brain，只辅助 brain 被发现和选择。

---

## 4. 运行时总模型

```text
                        ┌────────────────────────────┐
                        │         Central Brain       │
                        │  plan / route / delegate    │
                        └──────────────┬─────────────┘
                                       │
                          delegate only to Brain
                                       │
              ┌────────────────────────┼────────────────────────┐
              │                        │                        │
              ▼                        ▼                        ▼
      ┌───────────────┐        ┌───────────────┐        ┌───────────────┐
      │ Native Brain  │        │ MCP-backed    │        │ Hybrid Brain  │
      │               │        │ Brain         │        │               │
      │ local tools   │        │ host + MCP    │        │ local + MCP   │
      └───────┬───────┘        └───────┬───────┘        └───────┬───────┘
              │                        │                        │
              │                        │                        │
              ▼                        ▼                        ▼
      local registry           MCP bindings / servers   local registry + MCP
```

要点只有两条：

1. `central` 永远只 delegate 给 `brain`
2. `MCP server` 永远只是 runtime 背后的 capability backend，不是 delegate 目标

---

## 5. Brain Manifest 需要定义什么

建议 v3 的 manifest 至少包含这些字段：

```json
{
  "schema_version": 1,
  "kind": "browser",
  "name": "Browser Brain",
  "brain_version": "1.0.0",
  "description": "Web automation and browser reasoning brain",
  "capabilities": [
    "web.browse",
    "web.extract",
    "web.form_fill"
  ],
  "task_patterns": [
    "browser",
    "web page",
    "form",
    "screenshot"
  ],
  "runtime": {
    "type": "mcp-backed"
  },
  "policy": {
    "tool_scope": "delegate.browser",
    "approval_mode": "default"
  },
  "compatibility": {
    "protocol": "1.0",
    "tested_kernel": "1.0.x"
  },
  "license": {
    "required": false
  },
  "health": {
    "startup_timeout_ms": 10000
  }
}
```

### 5.1 Manifest 的最小职责

manifest 至少要回答这 8 个问题：

1. 这是什么 brain
2. 它适合什么任务
3. 它有哪些 capability
4. 它用什么 runtime 跑
5. 它需要什么 policy/scope
6. 它兼容哪些协议和 kernel
7. 它是否需要授权
8. 它如何做启动健康门禁

字段级规则与演进策略见：

- [33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md)

### 5.2 Manifest 不负责什么

manifest 不应该承载：

- 具体的工具实现代码
- 大段 system prompt 正文
- 大量运行时临时状态
- 调用统计

这些分别应该放在 runtime、package 或持久化系统里。

---

## 6. Brain Runtime 四种模式

### 6.1 Native Runtime

`native` 是今天已经存在的模式。

特征：

- 独立 sidecar 二进制
- 本地 registry
- 自己实现 `brain/execute`
- 可直接使用本地工具或 reverse RPC

适合：

- `brain-code`
- `brain-verifier`
- `brain-fault`
- 需要强控制、强审计、强授权的官方付费 brain

### 6.2 MCP-backed Runtime

`mcp-backed` 指一个真正的 brain host，在内部绑定一个或多个 MCP server。

特征：

- 对外仍然是标准 brain sidecar
- 对内通过 MCP adapter 消费 `tools/resources/prompts`
- 由 brain host 负责提示词、策略、授权、健康、delegate 语义

适合：

- 快速复用现成 MCP 生态
- 第三方低成本构建领域 brain
- 官方免费生态型脑子

**重要边界**：

> `MCP server` 不是 brain。  
> `MCP-backed brain` 才是 brain。

### 6.3 Hybrid Runtime

`hybrid` 是长期最常见的模式。

特征：

- 同时拥有本地工具和 MCP capability
- 某些关键能力本地实现
- 某些外围能力走 MCP 绑定

适合：

- `browser` 本地有核心控制能力，外围再接抓包/知识库/测试平台 MCP
- `security` 本地有高可信扫描能力，再接 Jira/Slack/GitHub MCP

这是最适合商业产品的模式。

### 6.4 Remote Runtime

`remote` 是 v3 后续扩展点。

特征：

- brain 逻辑运行在远端
- 本地只保留描述与接入信息
- 适合托管脑、企业控制平面、多租户 SaaS

v3.0 不必完整落地，但 manifest 与 package 设计必须提前为它留口。

---

## 7. MCP 在 v3 的正式定位

v3 里，MCP 的角色被正式定义为：

- 一种 **capability binding 协议**
- 一种 **runtime backend**
- 一种 **生态接入面**

但 MCP **不是**：

- 顶层产品对象
- brain 的唯一实现方式
- orchestrator 的直接 delegate 目标

这条边界必须冻结，否则以后：

- license 放哪里会混乱
- health 算谁的会混乱
- central 到底 delegate 给谁会混乱
- marketplace 到底卖 MCP server 还是 brain 会混乱

---

## 8. Brain Package 要解决什么问题

`Brain Package` 负责交付，不负责思考。

建议 package 至少包含：

```text
brain-browser/
  manifest.json
  bin/
    brain-browser
  bindings/
    mcp/
      puppeteer.json
      fetch.json
  config.example.json
  LICENSE
  CHANGELOG.md
```

如果是付费版，还可以包含：

```text
brain-browser-pro/
  manifest.json
  bin/
    brain-browser-pro
  license/
    public_key.pem
    license.example.json
  bindings/
    mcp/
      network.json
      trace.json
```

package 需要解决的核心问题：

- 如何安装
- 如何升级
- 如何签名
- 如何授权
- 如何声明兼容性
- 如何表达 edition / feature gate

安装目录、签名与 marketplace 索引见：

- [34-Brain-Package与Marketplace规范.md](./34-Brain-Package与Marketplace规范.md)

### 8.1 Package 不应该负责调度

package 是静态交付物，不应该承载：

- delegate 算法
- run-time health 状态
- 实时 routing 决策

这些都应该留在 kernel / orchestrator / runtime。

---

## 9. Policy 层应该放在哪

想把系统做稳，policy 不能散在每个角落。

v3 建议把 policy 统一理解为 brain 的一个标准插槽，至少覆盖：

- `tool_scope`
- `active_tools`
- `approval mode`
- `sandbox profile`
- `license gate`
- `health gate`

也就是：

- manifest 声明 policy 需求
- runtime 执行 policy
- kernel/orchestrator 校验 policy 是否满足

这样以后免费版、企业版、托管版才能走同一套脑子模型。

---

## 10. Capability 在 v3 的位置

capability 非常重要，但不应该成为第一层对外世界观。

### 10.1 v3.0

先把 capability 做成：

- manifest 中的标签
- 路由候选筛选条件
- marketplace 检索维度
- policy/授权的 feature 维度

### 10.2 v3.1+

再逐步升级成内部 capability graph：

- 一个任务可映射到多个 capability
- 一个 brain 可声明主能力、次能力、依赖能力
- orchestrator 可做更细粒度的 brain ranking

但这应该是内核增强，不应该先把第三方 API 复杂化。

---

## 11. Delegate 语义在 v3 的冻结规则

为了让未来 3-5 年不反复推翻模型，建议冻结这几条规则：

1. `central` 只 delegate 给 `brain`
2. delegate 的执行入口仍是 `brain/execute`
3. `MCP server` 不能直接成为 delegate target
4. license / health / policy 在 delegate 前必须先过门禁
5. `brain_version` 不需要和 central 强制对齐
6. runtime 可以替换，但 manifest 契约尽量稳定

这 6 条一旦定住，后面的 v3.1/v3.2 扩展都不会把世界观打散。

---

## 12. 与当前仓库的衔接关系

这份 v3 架构不是推翻现状，而是把现有成果上收成稳定模型。

### 12.1 已有能力如何映射

- 当前内置 sidecar
  → `native brain`
- 当前 `kernel/mcpadapter`
  → `mcp binding backend`
- 当前第三方 sidecar 开发指南
  → `native brain` 的最小接入文档
- 当前付费授权方案
  → `brain package` 的商业化扩展

### 12.2 当前还缺什么

要真正落成 v3，文档层已经补上，但实现层仍然缺：

- 按 [33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md) 落地 manifest 解析与校验
- 按 [34-Brain-Package与Marketplace规范.md](./34-Brain-Package与Marketplace规范.md) 落地 package 安装与索引
- `MCP-backed brain` host 约定
- brain 级 policy 装配模型
- capability-based routing 的最小实现

---

## 13. 推荐的 v3 实施顺序

### 13.1 v3.0 必做

1. 实现 `Brain Manifest` schema v1
2. 明确 `native / mcp-backed / hybrid` runtime
3. 实现 `Brain Package` 目录与发布规范
4. 让 orchestrator 能读取 manifest 做脑子发现
5. 做第一个 `MCP-backed brain` 参考实现

### 13.2 v3.1 建议做

1. capability-based routing
2. package index / marketplace 元数据
3. 远程 runtime 的统一描述字段
4. package 签名与安装器

### 13.3 v3.2 再做

1. hosted/remote brain
2. 企业 policy center
3. 组织级 license 与 edition 管理
4. multi-brain marketplace

---

## 14. 最终结论

如果目标是让架构稳定 3-5 年，v3 最不该做的事，是把顶层世界观建立在 `MCP`、`tool` 或 `package` 之上。

更稳的结论是：

- 顶层产品对象：`Brain`
- 稳定契约：`Brain Manifest`
- 运行实现：`Brain Runtime`
- 分发单位：`Brain Package`
- 路由维度：`Capability`

因此，v3 的正式口径应该是：

> **Brain-first, manifest-driven, runtime-pluggable, package-delivered, policy-governed.**

这套模型既能承接今天的 sidecar 架构，也能容纳未来的 MCP、商业化、第三方生态和远程运行时。
