# 34. Brain Package 与 Marketplace 规范

> **状态**：Draft · v1 · 2026-04-13
> **上位规格**：[32-v3-Brain架构.md](./32-v3-Brain架构.md) / [33-Brain-Manifest规格.md](./33-Brain-Manifest规格.md)
> **相关文档**：[29-第三方专精大脑开发.md](./29-第三方专精大脑开发.md) / [30-付费专精大脑授权方案.md](./30-付费专精大脑授权方案.md)

---

## 1. 设计结论

`Brain Package` 是 v3 Specialist Brain 的**分发单位**。

它负责：

- 安装
- 升级
- 回滚
- 签名
- 兼容性声明
- 授权与 edition 打包
- Marketplace 分发

它不负责：

- delegate 决策
- `brain/execute`
- run-time 计划推进

一句话：

> central 调度的是 `Brain`，用户安装的是 `Brain Package`。

---

## 2. 为什么要单独定义 Package

如果没有统一的 package 规范，后面会同时出现这些混乱：

- 第三方不知道应该交付一个二进制、一个 zip、还是一个目录
- 官方收费 brain 不知道 license 放哪
- Marketplace 无法做统一索引
- 安装器无法稳定验证 manifest / checksum / signature
- `native` / `mcp-backed` / `hybrid` runtime 没有统一装载入口

所以 package 必须从一开始就标准化。

---

## 3. Package、Brain、Runtime 的关系

三者分工必须清楚：

- `Brain`
  产品对象，负责被识别与调度
- `Runtime`
  实现方式，负责执行
- `Package`
  分发容器，负责交付

一个 package 里通常包含：

- 1 个主 Manifest
- 0 或 1 个主 runtime entrypoint
- 0 到多个 bindings
- 0 或 1 份 license/edition 元数据

---

## 4. Package 标识与版本

### 4.1 `package_id`

建议 package 使用 `<publisher>/<name>` 形式：

```text
leef-l/browser
leef-l/browser-pro
acme/security-suite
thirdparty/data-postgres
```

规则：

- `publisher` SHOULD 稳定唯一
- `name` SHOULD 只用小写字母、数字、连字符
- `package_id` 在同一 marketplace 内 MUST 唯一

### 4.2 `package_version`

package version 是 package 自己的 semver。

它：

- 可以与 `brain_version` 相同
- 也可以不同
- SHOULD 在发生安装内容变化时递增

推荐做法：

- 单脑包：`package_version == brain_version`
- 套餐包或多资产包：允许不同

### 4.3 发布 channel

建议支持：

- `stable`
- `beta`
- `nightly`

Marketplace 和安装器都 SHOULD 理解 channel，但默认只消费 `stable`。

---

## 5. Package 目录布局

### 5.1 最小布局

```text
brain-browser/
  manifest.json
  README.md
```

### 5.2 Native / Hybrid 推荐布局

```text
brain-browser/
  manifest.json
  README.md
  CHANGELOG.md
  LICENSE
  bin/
    brain-browser
  config.example.json
```

### 5.3 MCP-backed 推荐布局

```text
brain-data/
  manifest.json
  README.md
  bin/
    brain-data
  bindings/
    mcp/
      postgres.json
      fetch.json
```

### 5.4 付费包推荐布局

```text
brain-browser-pro/
  manifest.json
  README.md
  CHANGELOG.md
  LICENSE
  bin/
    brain-browser-pro
  bindings/
    mcp/
      network.json
  license/
    public_key.pem
    license.example.json
```

---

## 6. 必选与可选文件

### 6.1 必选文件

| 文件 | 说明 |
|------|------|
| `manifest.json` | 必选，符合 33 号文档 |
| `README.md` | 必选，说明安装和用途 |

### 6.2 推荐文件

| 文件 | 说明 |
|------|------|
| `CHANGELOG.md` | 版本变化 |
| `LICENSE` | 源码或包的许可条款 |
| `bin/` | 本地 runtime 二进制 |
| `bindings/` | MCP 或其他 backend 绑定声明 |
| `config.example.json` | 推荐配置示例 |
| `license/public_key.pem` | 付费 package 的验签公钥 |
| `checksums.txt` / `SHA256SUMS` | 校验值 |
| `attestation.json` | provenance / 供应链证明 |

---

## 7. `bindings/` 目录

Package 里的 `bindings/` 用来声明 runtime 需要的外部 capability。

v1 重点支持：

- `bindings/mcp/*.json`

示例：

```json
{
  "schema_version": 1,
  "name": "postgres",
  "transport": "stdio",
  "command": ["docker", "run", "--rm", "-i", "mcp/postgres", "${DATABASE_URL}"],
  "tool_prefix": "pg.",
  "expose_to_brain": true
}
```

规则：

- binding 是 runtime 依赖，不是 brain 本体
- binding 文件路径 SHOULD 由 Manifest 的 `runtime.mcp_bindings` 引用

---

## 8. 签名、校验与 provenance

为了让 package 能被可靠安装，推荐安装器至少执行这 4 层校验：

1. `manifest.json` schema 校验
2. 文件 checksum 校验
3. package 签名或 attestation 校验
4. Manifest 兼容性与 license gate 校验

### 8.1 推荐签名材料

- `SHA256SUMS`
- `SHA256SUMS.sig`
- `attestation.json`

### 8.2 最低要求

正式发布包 SHOULD 至少提供：

- checksum
- 一个可验证的签名或供应链证明

---

## 9. 安装流程建议

### 9.1 安装

建议安装器按这个顺序执行：

1. 解析 package 元数据
2. 校验 checksum / 签名
3. 解析并校验 Manifest
4. 检查 kernel / protocol 兼容性
5. 解压或拷贝到本地 package 目录
6. 执行首次 health check
7. 激活 package

### 9.2 升级

升级 SHOULD 保证：

- 新 package 校验通过后再切换
- 保留旧版本可回滚
- Manifest breaking change 时显式告警

### 9.3 卸载

卸载 SHOULD 只删除 package 自己的安装目录，不应顺手删除：

- 用户数据
- 外部 license 文件
- 独立缓存目录

---

## 10. 本地安装目录建议

建议目录：

```text
~/.brain/packages/
  leef-l/
    browser/
      1.0.0/
        manifest.json
        bin/
        bindings/
```

激活态可以通过符号链接或 state 文件维护：

```text
~/.brain/packages-active/
  browser -> ~/.brain/packages/leef-l/browser/1.0.0
```

这样：

- 多版本共存更简单
- 回滚更简单
- debug 更清楚

---

## 11. Marketplace 的职责

Marketplace 不是运行时，不做 delegate。

它只负责：

- 索引 package
- 展示 metadata
- 提供下载入口
- 做基础兼容筛选
- 展示 publisher、edition、capabilities、price tier

Marketplace 不负责：

- 直接执行 brain
- 代替 runtime 做 license 验签

---

## 12. Marketplace 索引结构

推荐每个 package 至少暴露下面这些字段：

```json
{
  "schema_version": 1,
  "package_id": "leef-l/browser-pro",
  "display_name": "Browser Brain Pro",
  "publisher": "leef-l",
  "brain_kind": "browser-pro",
  "package_version": "2.0.0",
  "channel": "stable",
  "runtime_types": ["hybrid"],
  "capabilities": [
    "web.browse",
    "web.assert",
    "web.trace"
  ],
  "license_required": true,
  "edition": "pro",
  "source": {
    "type": "github-release",
    "url": "https://github.com/leef-l/brain/releases/download/v2.0.0/browser-pro-linux-amd64.tar.gz"
  },
  "checksums_url": "https://example.com/SHA256SUMS",
  "manifest_url": "https://example.com/manifest.json"
}
```

### 12.1 必选索引字段

| 字段 | 说明 |
|------|------|
| `package_id` | 唯一标识 |
| `publisher` | 发布者 |
| `brain_kind` | 对应 brain kind |
| `package_version` | 包版本 |
| `runtime_types` | 支持的 runtime 类型 |
| `capabilities` | 能力标签 |
| `source` | 下载源 |

### 12.2 推荐索引字段

- `license_required`
- `edition`
- `channel`
- `homepage`
- `icon_url`
- `docs_url`
- `signature_url`

---

## 13. 免费包与付费包

### 13.1 免费包

免费包通常具备：

- 无运行授权
- 开源或开放使用
- 直接安装即可激活

### 13.2 付费包

付费包通常具备：

- Manifest 里 `license.required = true`
- package 内嵌公钥或授权说明
- runtime 启动阶段执行 license gate

**注意**：

> Marketplace 可以标注“这是付费包”，  
> 但真正的授权生效点仍然在 brain runtime，而不是 Marketplace 页面。

---

## 14. 发布与分发建议

### 14.1 官方包

官方包建议通过：

- GitHub Releases
- 官方 Marketplace 索引
- npm/pnpm/Homebrew/Scoop 等包装分发

### 14.2 第三方包

第三方包建议至少提供：

- 包下载地址
- Manifest
- checksum
- 版本兼容说明

### 14.3 推荐发布单元

一个安装资产 SHOULD 尽量是“一个 package 一份压缩包”，而不是散落文件。

这样安装器和 Marketplace 都更容易处理。

---

## 15. 与现有仓库的衔接

这份规范不是推翻现有发布方式，而是把它上收成稳定模型。

当前仓库已经具备的基础：

- sidecar 二进制分发
- GitHub Release 资产
- SHA256SUMS
- npm/pnpm wrapper
- 付费 brain license 方案雏形

v3 要新增的是：

- 标准 `manifest.json`
- 标准 package 目录布局
- package 安装目录约定
- marketplace 索引结构

---

## 16. 一句话结论

`Brain Package` 解决的是“怎么交付一个 brain”，不是“怎么让 brain 思考”。

如果 `Manifest` 是 brain 的身份证，`Package` 就是它的标准运输箱，而 `Marketplace` 只是运输箱的索引与货架。
