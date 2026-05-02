# SDK 设计文档索引

> **版本**: v4.0(对齐批次 1-4 文档重写工程)
> **更新日期**: 2026-05-02
> **顶级权威入口**: [`../../docs/README.md`](../../docs/README.md) — 30 分钟读完整体理解 brain v3
> **总文档数**: 28 份(批次 1 删除 11 份过期文档 + 批次 2-4 重写 9 份至代码现状)

---

## 必读文档(稳定)

| 文档 | 说明 | 状态 |
|------|------|------|
| [`32-v3-Brain架构.md`](32-v3-Brain架构.md) | v3 总体架构总览(批次 4 重写,18 节) | ✅ v3.0 |
| [`02-BrainKernel设计.md`](02-BrainKernel设计.md) | Kernel 核心:Frame、BidirRPC、生命周期 | ✅ 稳定 |
| [`20-协议规格.md`](20-协议规格.md) | JSON-RPC 2.0 协议、帧格式、方法命名空间 | ✅ 稳定 |
| [`21-错误模型.md`](21-错误模型.md) | 错误码体系、分级、传播规则 | ✅ 稳定 |
| [`23-安全模型.md`](23-安全模型.md) | Zone 模型、审批分级、LLM 访问控制 | ✅ 稳定 |
| [`24-可观测性.md`](24-可观测性.md) | 指标、日志、健康检查 | ✅ 稳定 |
| [`25-测试策略.md`](25-测试策略.md) | 测试分层与策略 | ✅ 稳定 |
| [`26-持久化与恢复.md`](26-持久化与恢复.md) | SQLite WAL 持久化 | ✅ 稳定 |
| [`27-CLI命令契约.md`](27-CLI命令契约.md) | CLI 命令定义与参数规范 | ✅ 稳定 |
| [`28-SDK交付规范.md`](28-SDK交付规范.md) | SDK 发布、版本、兼容性 | ✅ 稳定 |
| [`29-第三方专精大脑开发.md`](29-第三方专精大脑开发.md) | 第三方接入指南(批次 4 重写,15 节) | ✅ v3.0 |

---

## 子系统设计稿(批次 2-3 已重写至 MACCS v2 代码现状)

### 编排与并发(批次 2)

| 文档 | 说明 | 状态 |
|------|------|------|
| [`35-BrainPool实现设计.md`](35-BrainPool实现设计.md) | 进程池管理 + 多实例并发 + 健康监控 | ✅ v2.0(批次 2) |
| [`35-LeaseManager实现设计.md`](35-LeaseManager实现设计.md) | 资源锁 + Wave 7 边界澄清 | ✅ v2.0(批次 2) |
| [`35-Dispatch-Policy-冲突图与Batch分组算法.md`](35-Dispatch-Policy-冲突图与Batch分组算法.md) | 冲突检测 + 重排 + 死锁仲裁 | ✅ v2.0(批次 2) |

### 智能层(批次 3)

| 文档 | 说明 | 状态 |
|------|------|------|
| [`35-Context-Engine详细设计.md`](35-Context-Engine详细设计.md) | 上下文装配 + 项目记忆 + 跨脑共享 | ✅ v2.0(批次 3) |
| [`35-自适应学习L1-L3算法设计.md`](35-自适应学习L1-L3算法设计.md) | L0-L3 + Wave 5 全 6 项升级 | ✅ v2.0(批次 3) |
| [`35-跨脑通信协议设计.md`](35-跨脑通信协议设计.md) | 23 method 全表 + EventBus | ✅ v2.0(批次 3) |
| [`35-BrainCapability标签与匹配算法.md`](35-BrainCapability标签与匹配算法.md) | 三阶段匹配 + 路由公式 0.4/0.25/0.35 | ✅ v2.0(批次 3) |

### 其他子系统(代码已稳定,文档保留)

| 文档 | 说明 | 状态 |
|------|------|------|
| [`33-Brain-Manifest规格.md`](33-Brain-Manifest规格.md) | brain.json 字段规格 | ✅ 稳定 |
| [`34-Brain-Package与Marketplace规范.md`](34-Brain-Package与Marketplace规范.md) | 包与 marketplace | ✅ 稳定 |
| [`35-TaskExecution生命周期状态机.md`](35-TaskExecution生命周期状态机.md) | 12 状态机 | ✅ 稳定 |
| [`35-Manifest解析与版本化设计.md`](35-Manifest解析与版本化设计.md) | Manifest 解析 | ✅ 稳定 |
| [`35-Flow-Edge存储与注册发现设计.md`](35-Flow-Edge存储与注册发现设计.md) | Flow/Edge 注册发现 | ✅ 稳定 |
| [`35-语义审批分级设计.md`](35-语义审批分级设计.md) | 4 级审批分级 | ✅ 稳定 |
| [`35-统一Dashboard设计规格.md`](35-统一Dashboard设计规格.md) | Dashboard | ✅ 稳定(Phase 1-4 已实现) |
| [`35-MCP-backed-Runtime设计.md`](35-MCP-backed-Runtime设计.md) | MCP 运行时 | ✅ 稳定 |
| [`35-项目级记忆与多项目管理.md`](35-项目级记忆与多项目管理.md) | MACCS Wave 7+ 多项目持久化(workdir → N 项目) | ✅ v1.0(批次 C) |
| [`30-付费专精大脑授权方案.md`](30-付费专精大脑授权方案.md) | 商业化授权 | ⚠️ 设计预留 |
| [`31-browser-brain-免费版与Pro版规划.md`](31-browser-brain-免费版与Pro版规划.md) | Browser Brain 版本规划 | ⚠️ 设计预留 |
| [`37-远程专精大脑调用说明.md`](37-远程专精大脑调用说明.md) | 远程调用(stdio/HTTP/WS) | ✅ 已实现 |

---

## Browser Brain 系列

| 文档 | 说明 | 状态 |
|------|------|------|
| [`39-Browser-Brain感知与嗅探增强设计.md`](39-Browser-Brain感知与嗅探增强设计.md) | 感知层设计 | ✅ 已实现 |
| [`40-Browser-Brain语义理解架构.md`](40-Browser-Brain语义理解架构.md) | 语义理解架构 | ✅ 已实现 |
| [`42-Browser-Brain异常感知层设计.md`](42-Browser-Brain异常感知层设计.md) | 异常感知 | ✅ 已实现 |

> 批次 1 删除了 4 份已完成 / 已过期的 Browser Brain 设计稿:
> `41-语义理解阶段0实验设计.md` `43-Browser-Brain必做项.md` `44-Browser-Brain开发计划.md`
> `错误修复支持库-Browser空Pattern中毒问题.md` + `experiments/phase0/` 数据目录

---

## 理论基础

| 文档 | 说明 |
|------|------|
| [`钱学森工程控制论-设计原则.md`](钱学森工程控制论-设计原则.md) | 控制论核心思想提炼(永久保留) |

---

## 批次 1 已删除的过期文档(供历史溯源)

以下 11 份文档在批次 1(2026-05-02 commit `c7ea48f`)被 git rm,因为已自我声明归档完成或被新文档取代:

- `35-v3重构路径与开发计划.md` — Step 1-17 全部完成
- `35-端到端时序与模块依赖图.md` — 需对齐 MACCS,已被 32-v3-Brain架构 v3 取代
- `38-v3后续增强计划.md` — Phase F 已完成

(还有 4 份 Browser Brain 见上方,以及 `docs/` 下的 4 份历史文档:`MACCS-v1.x-历史缺口归档.md`、`工程控制论-总纲落地设计.md`、`自适应学习系统设计方案.md`、`DATA_QUANT_GAP_ANALYSIS.md`)

---

## 入口导航

```
开发者                           外部用户
   │                                 │
   ▼                                 ▼
docs/README.md ⭐                仓根 README.md
单一权威入口                     用户使用手册
   │                                 │
   ├── 1. 它是什么                  ├── 安装
   ├── 2. 架构全景                  ├── CLI 命令
   ├── 3. 关键执行链路              ├── HTTP API
   ├── 4. MACCS 48 项能力对照表     ├── 配置说明
   ├── 5. 配置与运行                └── 大脑架构
   ├── 6. 进一步阅读 ─────────────┐
   ├── 7. 关键事实速查              │
   ├── 8. 代码位置速查              │
   └── 9. 贡献                      │
                                    ▼
                              sdk/docs/(本目录)
                              本索引 + 28 份子系统设计稿
                                    │
                                    ▼
                              brains/<kind>/docs/
                              shared/docs/
                              central/docs/
                              MACCS 顶级文档(docs/MACCS-*)
```

---

*最后更新于 2026-05-02。文档重写工程 4 批 19 commits 完成,所有重写文档对照 sdk/* 真实代码,带 file:line 引用证据。*
