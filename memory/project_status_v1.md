---
name: 项目真实状态 v0.5.1
description: BrainKernel SDK 版本号核定和真实进度（2026-04-13 全量审计+修复后）
type: project
originSessionId: 1e8ed975-fc50-4814-81a8-d3afac80357b
---
## 版本号：v0.5.1

VERSION.json / version.go / doc.go / CHANGELOG.md / npm 包已统一为 0.5.1。

## 编译 & 测试状态

- `go build/vet/test -race` 全部通过
- 21 个包 ok，0 数据竞争
- 730 个测试（含 133 骨架 + 151 合规）

## v3 重构计划完成度

Wave-1（P0/P1 安全修复）和 Wave-2（P2 架构改善）全部完成。
剩余：6 个包缺测试（Wave-3）、TurnExecutor 架构决策。

**Why:** 用户要求全面检测修复
**How to apply:** 后续版本号为 v0.6.0，重点是补测试（executionpolicy/toolguard/toolpolicy）
