# MACCS v1.x 历史缺口归档

> **归档日期**：2026-04-29  
> **状态**：全部完成 ✅  
> **说明**：本文档归档 v1.x 架构阶段的所有已修复缺口。当前项目已进入 MACCS v2.0 阶段，新缺口体系见 [`MACCS-实施路线图.md`](MACCS-实施路线图.md)。

---

## 归档概要

v1.x 阶段共追踪 **38 项缺口**，按严重度分布：

| 严重度 | 数量 | 说明 |
|--------|------|------|
| 🔴 P0/高 | 8 项 | 死代码、缺失 handler、空实现 |
| 🟠 P1/中 | 8 项 | 接口未打通、调用链断裂 |
| 🟡 P2/中 | 16 项 | 设计预留、功能待完善 |
| 🟢 低 | 6 项 | 增强功能、优化项 |

**全部已于 2026-04-26 ~ 2026-04-27 完成修复。**

---

## 关键缺口分类

### 1. 学习系统（L0-L3）
- Quant/Data/Desktop L0 学习链路打通
- L2 `RecommendOrder` / `RecordSequence` 接口落地
- L3 `RecordUserFeedback` / `GetPreference` 用户反馈闭环
- CLI 模式 `LearningEngine` 持久化存储

### 2. 工作流引擎
- Streaming Edge 真正跨 brain 流式效果（方案 B：文档诚实标记）
- `central.delegate` 支持 capMatcher 自动选脑（`target_kind` 可选）

### 3. 安全与审批
- Sandbox L2 容器沙箱校验
- SemanticApprover manifest 最小审批等级检查

### 4. Dashboard / WebUI
- `/v1/dashboard/brains/:kind` 细粒度端点
- 交易专用 REST API（portfolio / accounts / trading）
- 策略监控、风控面板、K线图、成交历史
- SSE streaming 聊天面板
- 认证机制（Bearer Token）

### 5. Browser Brain 语义理解
- `browser.understand` 工具 + SQLite 语义缓存
- 模式匹配引擎（含 PostConditions 校验）
- 模式统计 Dashboard
- `browser.check_anomaly` 工具
- 多模态兜底 + 疑难页面截图联合推理

### 6. 量化大脑（Quant）
- FeatureView 接口 + LiveFeatureView
- MarketAdapter（Ring Buffer → MarketView v2）
- 4 策略 v2 重构 + RegimeAwareAggregator
- BacktestEngine.Run() + 绩效统计
- ConfidenceCalibrator + AdaptiveGuard + BayesianSizer

### 7. 基础设施
- BrainPool `ReturnBrain` / `HealthCheck` / `Drain` / `WarmUp`
- LeaseManager 全功能实现
- Marketplace 本地查询
- Context Engine `Compress` LLM 摘要路径

---

## 原始文档

- 原 `缺口清单与实现追踪.md` — 追踪"接口/字段已存在，但生产链路未打通"
- 原 `未实现缺口追踪文档.md` — 追踪"设计存在但代码未实现或仅有占位"

两份原文档内容已完全归档于本文档，原始文件已删除。

---

## 下一步

当前 MACCS v2.0 的缺口与实施路线见：

- [`MACCS-实施路线图.md`](MACCS-实施路线图.md) — 6-Wave 实施计划
- [`MACCS-架构总纲-v2.md`](MACCS-架构总纲-v2.md) — 新架构定义
