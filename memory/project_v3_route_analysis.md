---
name: v3 路线对标分析（OpenClaw + Hermes）
description: 2026-04-16 对比 OpenClaw/Hermes 后的 v3 路线调整建议，含设计与代码脱节审计
type: project
---

## 行业对标（2026-04-16）

### OpenClaw（350K+ stars，MIT）
- 多通道 AI Agent 运行时（20+ 聊天平台）
- Skills + Plugins 扩展模型（5400+ 社区技能）
- 本地优先，Markdown 文件存储
- Node.js daemon，创始人加入 OpenAI 后由基金会接管

### Hermes Agent（57K+ stars，MIT，NousResearch）
- 自我改进 Agent 框架，Learning Loop 自动生成 skill
- **原生 MCP 客户端**，启动自动发现工具服务器
- 5 种运行后端（local/Docker/SSH/Singularity/Modal）
- 多平台网关（Telegram/Discord/Slack/WhatsApp）
- v0.8.0（2026-04）

## 设计 vs 代码脱节审计

### 完全未实现（3项）
1. **Brain Manifest**（Doc 33）— 无 parser/struct/校验
2. **Brain Package & Marketplace**（Doc 34）— 无安装器/签名/marketplace
3. **MCP-backed Runtime**（Doc 32）— 无 mcp_bindings 解析

### 部分实现（3项）
1. Runtime Pluggable — 有 sidecar 分离，无统一 Runtime interface
2. Brain Registry — 无全局注册表
3. WebUI P1/P2 — 框架有，功能待完善

### 完全实现（11项）
- FeatureView、RegimeAwareAggregator、AdaptiveGuard、BayesianSizer、BacktestEngine
- 4 个策略 v2、L0-L3 自适应学习全栈、WebUI P0 + WebSocket

### WebUI 文档冲突
- `Web-UI交易界面架构方案.md` 用 Preact（brain serve 入口）
- `WebUI交易面板设计方案.md` 用 Alpine.js（sidecar 嵌入）
- 实际代码用原生 JS

## v3 路线调整建议

| 原计划 | 建议 | 理由 |
|--------|------|------|
| Brain Manifest 12 字段 | 精简到 5 字段 | 行业趋势轻量化 |
| Brain Package + Marketplace | 降级或砍掉 | 无市场验证 |
| MCP-backed Runtime | **提升到 P0** | 已成行业标准 |
| 多通道接入 | 新增 Telegram 通知 | 量化场景刚需 |
| 自适应学习 | 抽象到 SDK 层 | 释放量化层能力 |
| WebUI 双文档 | 合并为一份 | 消除歧义 |

**Why:** 行业风向已变，重量级 Manifest/Package 模式无人采用，MCP 成为事实标准
**How to apply:** v3 开发优先做 MCP 客户端，简化 Manifest，砍 Marketplace
