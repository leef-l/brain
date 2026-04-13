# 33. Brain Manifest 规格 v1

> **状态**：Draft · v1 · 2026-04-13
> **上位规格**：[32-v3-Brain架构.md](./32-v3-Brain架构.md)
> **相关文档**：[29-第三方专精大脑开发.md](./29-第三方专精大脑开发.md) / [30-付费专精大脑授权方案.md](./30-付费专精大脑授权方案.md) / [34-Brain-Package与Marketplace规范.md](./34-Brain-Package与Marketplace规范.md)

---

## 1. 设计结论

`Brain Manifest` 是 v3 Specialist Brain 的**稳定契约文件**。

它的职责只有一个：

> 用机器可读的方式声明“这个 brain 是谁、会什么、怎么跑、需要什么门禁”。

Manifest 是：

- `Brain` 的身份说明书
- `Runtime` 的装配入口
- `Policy` / `License` / `Health` 的声明面
- `Package` 和 `Marketplace` 的共同元数据基底

Manifest 不是：

- 工具实现代码
- prompt 正文仓库
- 运行时状态快照
- 计费账单

---

## 2. 为什么需要单独的 Manifest

如果没有 Manifest，v3 会很快陷入 4 个问题：

1. orchestrator 只能看二进制名，无法稳定知道脑子的能力与适用范围
2. package 只能发文件，无法声明兼容性、policy、license gate
3. marketplace 只能做文件索引，无法做 capability 检索
4. `native` / `mcp-backed` / `hybrid` / `remote` runtime 无法统一接线

所以 Manifest 必须独立存在，并且尽量在未来 3-5 年保持稳定。

---

## 3. Manifest 的根结构

推荐根结构如下：

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
    "type": "native"
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

### 3.1 顶层字段清单

| 字段 | 必选 | 说明 |
|------|------|------|
| `schema_version` | 是 | Manifest schema 版本，v1 固定为 `1` |
| `kind` | 是 | Brain kind，例如 `browser` / `security` |
| `name` | 是 | 展示名 |
| `brain_version` | 是 | 这个 brain 自己的 semver |
| `description` | 是 | 一句话说明 |
| `capabilities` | 是 | capability 标签数组 |
| `task_patterns` | 否 | 给 central / marketplace 的任务提示词或关键词 |
| `runtime` | 是 | 运行时定义 |
| `policy` | 否 | 工具范围、审批模式、沙箱等声明 |
| `compatibility` | 是 | protocol / kernel 兼容信息 |
| `license` | 否 | 授权要求与 edition 信息 |
| `health` | 否 | 启动和存活检查阈值 |
| `metadata` | 否 | 发布者自定义元数据，供展示或索引使用 |

---

## 4. `kind` / `name` / `brain_version`

### 4.1 `kind`

`kind` 是 brain 的稳定标识。

规则：

- 建议只用小写字母、数字、连字符
- SHOULD 与 sidecar 二进制命名保持语义一致
- MUST 在一个安装环境内唯一

示例：

- `browser`
- `browser-pro`
- `security`
- `data`
- `image`

### 4.2 `name`

`name` 是展示名，不参与协议匹配。

它允许更适合 UI/Marketplace 的写法，例如：

- `Browser Brain`
- `Browser Brain Pro`
- `Security Brain`

### 4.3 `brain_version`

`brain_version` 是这个 brain 自己的版本号。

它：

- 不需要和 central 强制对齐
- 不需要和 package version 必然相同
- SHOULD 使用标准 semver

---

## 5. `capabilities` 与 `task_patterns`

### 5.1 `capabilities`

`capabilities` 是一组稳定标签，用于：

- delegate 候选筛选
- marketplace 检索
- policy / license feature 映射
- 未来 capability-based routing

推荐规则：

- 使用 `<domain>.<verb>` 风格
- 语义尽量稳定，避免带实现细节

示例：

- `web.browse`
- `web.extract`
- `db.query`
- `security.scan`
- `repo.review`

### 5.2 `task_patterns`

`task_patterns` 是给 central 和 UI 的辅助提示，不是严格匹配规则。

它适合放：

- 用户可能说出的关键词
- 业务场景短语
- 搜索友好的领域标签

示例：

```json
[
  "browser",
  "web page",
  "login form",
  "take screenshot"
]
```

---

## 6. `runtime` 结构

`runtime` 是 Manifest 的核心字段，决定 brain 怎么跑。

v1 先冻结 4 种类型：

- `native`
- `mcp-backed`
- `hybrid`
- `remote`

### 6.1 Native

```json
{
  "type": "native",
  "entrypoint": "bin/brain-browser",
  "args": [],
  "env": {}
}
```

含义：

- 由本地 sidecar 二进制承载
- 自己实现 `brain/execute`
- 工具主要来自本地 registry

### 6.2 MCP-backed

```json
{
  "type": "mcp-backed",
  "entrypoint": "bin/brain-browser-mcp",
  "mcp_bindings": [
    "bindings/mcp/puppeteer.json",
    "bindings/mcp/fetch.json"
  ]
}
```

含义：

- 对外仍然是 brain sidecar
- 对内通过 MCP binding 使用外部能力

**重要约束**：

> `MCP server` 不是 brain。  
> `MCP-backed runtime` 是 brain 的一种实现方式。

### 6.3 Hybrid

```json
{
  "type": "hybrid",
  "entrypoint": "bin/brain-browser-pro",
  "mcp_bindings": [
    "bindings/mcp/network.json"
  ]
}
```

含义：

- 同时拥有本地工具和 MCP 能力
- 是最适合官方收费 brain 的模式

### 6.4 Remote

```json
{
  "type": "remote",
  "endpoint": "https://brain.example.com/browser",
  "auth_ref": "vault://brain/browser-prod"
}
```

含义：

- brain 运行在远端
- 本地 Manifest 只保留连接描述

v1 允许定义该结构，但不要求内核立即完整实现。

---

## 7. `policy` 结构

`policy` 负责声明 brain 需要什么执行门禁。

推荐字段：

```json
{
  "tool_scope": "delegate.browser",
  "approval_mode": "default",
  "sandbox_profile": "workspace-write",
  "active_tools_profile": "safe"
}
```

### 7.1 字段说明

| 字段 | 必选 | 说明 |
|------|------|------|
| `tool_scope` | 否 | 对接 `active_tools.<scope>` 的 scope 名 |
| `approval_mode` | 否 | 例如 `plan` / `default` / `accept-edits` |
| `sandbox_profile` | 否 | 沙箱档位名 |
| `active_tools_profile` | 否 | 推荐工具 profile |

### 7.2 Manifest 与 Policy 的关系

Manifest 只做声明，不做强制执行。

也就是说：

- Manifest 说“这个 brain 期望什么 policy”
- Kernel / orchestrator / runtime 决定是否满足它

---

## 8. `compatibility` 结构

`compatibility` 用于安装期和运行前兼容检查。

推荐结构：

```json
{
  "protocol": "1.0",
  "tested_kernel": "1.0.x",
  "min_kernel": "1.0.0",
  "max_kernel": "1.x"
}
```

### 8.1 最小要求

v1 至少要求：

- `protocol`
- `tested_kernel`

### 8.2 规则

- `protocol` SHOULD 为明确值，例如 `1.0`
- `tested_kernel` 可以是区间或通配描述，例如 `1.0.x`
- 如果 Manifest 提供 `min_kernel` / `max_kernel`，安装器 SHOULD 执行硬门禁

---

## 9. `license` 结构

`license` 不是开源许可证文本，而是运行授权声明。

推荐结构：

```json
{
  "required": true,
  "edition": "pro",
  "features": [
    "browser-pro.evidence",
    "browser-pro.assertions"
  ]
}
```

### 9.1 字段说明

| 字段 | 必选 | 说明 |
|------|------|------|
| `required` | 是 | 是否需要运行授权 |
| `edition` | 否 | `free` / `pro` / `enterprise` 等 |
| `features` | 否 | 该 brain 可能用到的 feature gate 列表 |

### 9.2 与授权文件的关系

Manifest 里的 `license` 是需求声明；
真正的授权校验仍然由 runtime 读取 `license.json` 执行。

---

## 10. `health` 结构

`health` 用于定义 brain 的基本存活门槛。

推荐结构：

```json
{
  "startup_timeout_ms": 10000,
  "heartbeat_timeout_ms": 30000,
  "expected_methods": [
    "initialize",
    "brain/execute",
    "tools/list"
  ]
}
```

### 10.1 规则

- `startup_timeout_ms` SHOULD 用于首次拉起超时
- `heartbeat_timeout_ms` SHOULD 用于长驻场景
- `expected_methods` 可帮助安装器或 `doctor` 做静态检查

---

## 11. `metadata` 结构

`metadata` 是可选展示字段，适合给 Marketplace/UI 用。

例如：

```json
{
  "publisher": "leef-l",
  "homepage": "https://github.com/leef-l/brain",
  "tags": ["browser", "automation", "web"],
  "icon": "assets/icon.png"
}
```

规则：

- MUST NOT 承载运行时关键字段
- SHOULD 只放展示或索引用元数据

---

## 12. 完整示例

### 12.1 Native Brain

```json
{
  "schema_version": 1,
  "kind": "verifier",
  "name": "Verifier Brain",
  "brain_version": "1.0.0",
  "description": "Read-only verification brain",
  "capabilities": ["repo.review", "test.verify"],
  "runtime": {
    "type": "native",
    "entrypoint": "bin/brain-verifier"
  },
  "policy": {
    "tool_scope": "delegate.verifier",
    "active_tools_profile": "readonly"
  },
  "compatibility": {
    "protocol": "1.0",
    "tested_kernel": "1.0.x"
  },
  "license": {
    "required": false
  }
}
```

### 12.2 MCP-backed Brain

```json
{
  "schema_version": 1,
  "kind": "data",
  "name": "Data Brain",
  "brain_version": "1.1.0",
  "description": "Data access brain backed by MCP servers",
  "capabilities": ["db.query", "db.inspect"],
  "runtime": {
    "type": "mcp-backed",
    "entrypoint": "bin/brain-data",
    "mcp_bindings": [
      "bindings/mcp/postgres.json",
      "bindings/mcp/fetch.json"
    ]
  },
  "compatibility": {
    "protocol": "1.0",
    "tested_kernel": "1.0.x"
  }
}
```

### 12.3 Hybrid Paid Brain

```json
{
  "schema_version": 1,
  "kind": "browser-pro",
  "name": "Browser Brain Pro",
  "brain_version": "2.0.0",
  "description": "Evidence-heavy browser automation brain",
  "capabilities": [
    "web.browse",
    "web.assert",
    "web.trace"
  ],
  "runtime": {
    "type": "hybrid",
    "entrypoint": "bin/brain-browser-pro",
    "mcp_bindings": [
      "bindings/mcp/network.json"
    ]
  },
  "policy": {
    "tool_scope": "delegate.browser",
    "active_tools_profile": "browser-pro"
  },
  "compatibility": {
    "protocol": "1.0",
    "tested_kernel": "1.0.x"
  },
  "license": {
    "required": true,
    "edition": "pro",
    "features": [
      "browser-pro.evidence",
      "browser-pro.assertions",
      "browser-pro.sessions"
    ]
  },
  "health": {
    "startup_timeout_ms": 15000
  }
}
```

---

## 13. 安装期与运行期校验

### 13.1 安装期建议校验

- schema 是否可解析
- `schema_version` 是否受支持
- `kind` 是否冲突
- runtime type 是否受支持
- entrypoint / binding 引用是否存在
- kernel / protocol 兼容范围是否满足

### 13.2 运行期建议校验

- binary / endpoint 是否可达
- expected methods 是否满足
- license gate 是否通过
- health timeout 是否满足

---

## 14. 演进规则

为了让 Manifest 真能撑 3-5 年，建议冻结这几条：

1. `schema_version` 只在 breaking change 时升级 major
2. 新字段优先以“可选字段”扩展
3. 既有字段语义 MUST NOT 悄悄漂移
4. `runtime.type` 的既有枚举值 MUST 保持兼容
5. `kind` 与 `capabilities` 一旦公开，SHOULD 稳定维护

---

## 15. 一句话结论

`Brain Manifest` 是 v3 里每个 brain 的机器可读身份证。

Kernel 不是去认识一个“二进制文件”或一个“MCP server”，而是先认识一个有 Manifest 的 `Brain`，再根据 Manifest 去装配 runtime、policy、license 与 package。
