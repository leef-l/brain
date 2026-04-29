# Data + Quant Brain 文档 vs 代码 差距分析报告

> 生成时间: 2025-01-24
> 分析范围: `brains/data/` + `brains/quant/` 全部设计文档与核心代码

---

## 状态更新（2026-04-26）

以下 4 项 🔴 高优先级差距已在后续代码迭代中补全，本报告相应章节仍保留原始记录供追溯：

| # | 差距项 | 状态 | 说明 |
|---|--------|------|------|
| 1 | `DataBrain.IsHealthy()` stub | ✅ 已修复 | `Health()` 已实现真正的多维健康检查 |
| 2 | `DataBrain.DataQualityScore()` 缺失 | ✅ 已修复 | 数据质量评分方法已补全 |
| 3 | `data.replay_start / replay_stop` stub | ✅ 已修复 | 回放模式已实现 |
| 4 | `quant.strategy_weights` 行为不完整 | ✅ 已修复 | 返回各 unit 策略名称及权重配置 |

> **注意**: 本报告其余章节（如 L2 超参学习、熔断机制等）仍为有效差距，需按原计划推进。

---

## 一、总体结论

| 维度 | Data Brain | Quant Brain |
|------|-----------|-------------|
| **架构一致性** | ✅ 基本一致 | ✅ 基本一致 |
| **接口完整性** | ⚠️ 部分缺失 | ⚠️ 部分缺失 |
| **功能实现度** | ⚠️ 2个功能为stub | ✅ 核心功能完整 |
| **配置一致性** | ✅ 一致 | ✅ 一致 |
| **Tool接口** | ⚠️ 2个Tool为占位 | ⚠️ 1个Tool行为不完整 |

**核心判断**: 两模块的架构骨架、数据流、配置体系均已对齐文档设计。主要差距集中在：**部分高级功能尚未实现（stub）**、**个别接口行为不完整**、**文档有描述但代码未暴露的方法**。

---

## 二、Data Brain 差距分析

### A. 接口一致性

| # | 文档定义 | 代码实现 | 状态 | 说明 |
|---|---------|---------|------|------|
| 1 | `Start(ctx) error` | ✅ `Start(context.Context) error` | ✅ | 完全一致 |
| 2 | `Stop() error` | ✅ `Stop() error` | ✅ | 完全一致 |
| 3 | `FeatureVector(instID string) *FeatureOutput` | ✅ `FeatureVector(instID string) *feature.Output` | ✅ | 返回192维向量，一致 |
| 4 | `Candles(instID, timeframe string) []Candle` | ✅ `Candles(instID, timeframe string) []Candle` | ✅ | 一致 |
| 5 | `Health() map[string]any` | ✅ `Health() map[string]any` | ✅ | 一致 |
| 6 | `ActiveInstruments() []string` | ✅ `ActiveInstruments() []string` | ✅ | 一致 |
| 7 | `Buffers() *ringbuf.BufferManager` | ✅ `Buffers() *ringbuf.BufferManager` | ✅ | 一致 |
| 8 | `IsHealthy() bool` | ⚠️ 返回 `false` stub | ❌ | **差距: 始终返回false，未实现真正的健康检查** |
| 9 | `DataQualityScore() float64` | ❌ 未实现 | ❌ | **差距: 文档§9明确要求，代码无此方法** |
| 10 | `SubEngines() []FeatureSubEngine` | ❌ 未实现 | ❌ | **差距: 文档§10.5要求列出子引擎，代码无此方法** |
| 11 | `Recover()` | ✅ `Recover()` | ✅ | 已实现，用于crash恢复 |

**差距总结**: `IsHealthy()` 为stub、`DataQualityScore()` 和 `SubEngines()` 完全缺失。

### B. 功能完整性

| 功能模块 | 文档章节 | 代码实现 | 状态 |
|---------|---------|---------|------|
| 数据采集层 (Provider) | §5.1 | ✅ | 完整 |
| 数据校验 (Validator) | §5.2 | ✅ | 3-sigma、价格跳变、重复检测均实现 |
| 特征引擎 (Feature Engine) | §5.3 | ✅ | 192维向量、6大分组均实现 |
| Ring Buffer | §5.4 | ✅ | 多时间框架、多深度实现 |
| 历史回填 (Backfill) | §5.5 | ✅ | `BackfillConfig` + 进度追踪 |
| 回放模式 (Replay) | §5.6 | ⚠️ | **Tool为stub，未真正实现** |
| 熔断机制 (Circuit Breaker) | §8.6 | ⚠️ | **有字段(cbFailCount等)但无真正熔断逻辑** |
| 校验拒绝回调 | §8.5 | ❌ | **文档要求可配置回调，代码无此机制** |
| 自动品种筛选 | §5.1 | ✅ | `ActiveListConfig` 按成交量筛选 |

### C. 数据流一致性

文档§5描述的数据流:
```
Provider → Validator → Feature Engine → Ring Buffer → (Quant sidecar, Central, WebUI)
```

代码实现:
```
Provider(feedLoop) → Validator(validateTick) → Feature Engine(computeFeatures) → Ring Buffer(pushValidatedTick)
                                      ↓
                              DataBrain.FeatureVector() ←  sidecar tools(data.get_feature_vector)
```

**状态**: ✅ 完全一致。数据从Provider流入，经校验、特征计算后进入Ring Buffer，Quant通过`data.get_all_snapshots`批量消费。

### D. 配置一致性

| 配置项 | 文档§7 | 代码 `data.Config` | 状态 |
|-------|--------|-------------------|------|
| `active_list.min_volume_24h` | ✅ | ✅ `MinVolume24h` | ✅ |
| `active_list.max_instruments` | ✅ | ✅ `MaxInstruments` | ✅ |
| `active_list.update_interval` | ✅ | ✅ `UpdateInterval` | ✅ |
| `backfill.enabled` | ✅ | ✅ `Enabled` | ✅ |
| `backfill.max_days` | ✅ | ✅ `MaxDays` | ✅ |
| `validation.max_price_jump` | ✅ | ✅ `MaxPriceJump` | ✅ |
| `ring_buffer.candle_depth` | ✅ | ✅ `CandleDepth` | ✅ |
| `ring_buffer.trade_depth` | ✅ | ✅ `TradeDepth` | ✅ |
| `ring_buffer.orderbook_depth` | ✅ | ✅ `OrderBookDepth` | ✅ |
| `feature.enabled` | ✅ | ✅ `Enabled` | ✅ |
| `feature.windows` | ✅ | ✅ `Windows` | ✅ |
| `feature.interval` | ✅ | ✅ `Interval` | ✅ |

**状态**: ✅ 完全一致。

### E. Tool 接口一致性

| Tool | 文档§13 | 代码实现 | 状态 |
|------|---------|---------|------|
| `data.get_candles` | ✅ | ✅ | 完整 |
| `data.get_all_snapshots` | ✅ | ✅ | 完整，含192维向量 |
| `data.get_snapshot` | ✅ | ✅ | 完整 |
| `data.get_feature_vector` | ✅ | ✅ | 完整，分6段返回 |
| `data.provider_health` | ✅ | ✅ | 完整 |
| `data.validation_stats` | ✅ | ✅ | 完整 |
| `data.backfill_status` | ✅ | ✅ | 完整 |
| `data.active_instruments` | ✅ | ✅ | 完整 |
| `data.replay_start` | ✅ | ⚠️ | **stub，返回not_implemented** |
| `data.replay_stop` | ✅ | ⚠️ | **stub，返回not_implemented** |

---

## 三、Quant Brain 差距分析

### A. 接口一致性

| # | 文档定义 | 代码实现 | 状态 | 说明 |
|---|---------|---------|------|------|
| 1 | `New(cfg, buffers, logger)` | ✅ `New(Config, SnapshotSource, *slog.Logger)` | ✅ | 一致 |
| 2 | `AddUnit(unit *TradingUnit)` | ✅ | ✅ | 一致 |
| 3 | `Start(ctx) error` | ✅ | ✅ | 一致 |
| 4 | `Stop(ctx) error` | ✅ | ✅ | 一致 |
| 5 | `Pause() / Resume()` | ✅ | ✅ | 一致 |
| 6 | `SetSignalExitConfig(cfg)` | ✅ | ✅ | 一致 |
| 7 | `SetTrailingStopConfig(cfg)` | ✅ | ✅ | 一致 |
| 8 | `SetLearning(wa, ss, opt)` | ✅ | ✅ | L1学习组件 |
| 9 | `SetReviewer(r Reviewer)` | ✅ | ✅ | LLM审核 |
| 10 | `Health() map[string]any` | ✅ | ✅ | 完整metrics |
| 11 | `PositionHealth(key) float64` | ✅ | ✅ | EWMA健康度 |
| 12 | `TraceStore() tracer.Store` | ✅ | ✅ | 审计追踪 |
| 13 | `Units() []*TradingUnit` | ✅ | ✅ | 一致 |
| 14 | `BuildGlobalSnapshot()` | ✅ `buildGlobalSnapshot()` | ✅ | 私有方法，功能一致 |

**状态**: ✅ 几乎所有公共接口均已实现且签名一致。

### B. 功能完整性

| 功能模块 | 文档章节 | 代码实现 | 状态 |
|---------|---------|---------|------|
| 策略池 (Strategy Pool) | §6.1 | ✅ | 4种策略实现 |
| 信号聚合 (Aggregator) | §6.1 | ✅ | `RegimeAwareAggregator` |
| 风控守卫 (Risk Guard) | §6.1 | ✅ | `AdaptiveGuard` |
| 仓位计算 (Position Sizer) | §6.1 | ✅ | `BayesianSizer` |
| 订单执行 (Order Executor) | §6.1 | ✅ | `Execute()` 方法 |
| LLM审核 (Reviewer) | §6.3 | ✅ | `KernelReviewer` |
| L1自适应学习 | §11 | ✅ | WeightAdapter, SymbolScorer, SLTPOptimizer |
| L2超参学习 | §11 | ⚠️ | **文档要求，代码中未见L2实现** |
| 信号反转平仓 | §6.2 | ✅ | `signalExit` + `PositionHealth` EWMA |
| 移动止损 | §6.2 | ✅ | `TrailingStopConfig` |
| Top N过滤 | - | ✅ | `MaxTradeSymbols` (代码有，文档未提) |
| MAE/MFE追踪 | - | ✅ | `trackMAEMFE()` (代码有，文档未提) |
| Orphan清理 | - | ✅ | `cleanupOrphans()` (代码有，文档未提) |
| WebUI | §12 | ⚠️ | 配置存在，具体实现未在本次分析范围 |

### C. 数据流一致性

文档描述的三脑数据流:
```
Data Brain → Ring Buffer → Quant Brain → Exchange
                ↓
         192-dim Feature Vector
```

代码实现:
```
DataBrain(FeatureVector/192-dim) → RingBuffer → RemoteBufferManager(IPC)
                                              ↓
                              QuantBrain(SnapshotSource.Latest())
                                              ↓
                              TradingUnit.Evaluate() → Exchange.PlaceOrder()
```

**状态**: ✅ 完全一致。Quant通过`RemoteBufferManager`从Data sidecar读取，使用`snap.FeatureVector` [192]float64。

### D. 配置一致性

| 配置项 | 文档§7 | 代码 `quant.FullConfig` | 状态 |
|-------|--------|------------------------|------|
| `brain.cycle_interval` | ✅ | ✅ `CycleInterval` | ✅ |
| `brain.default_timeframe` | ✅ | ✅ `DefaultTimeframe` | ✅ |
| `accounts` | ✅ | ✅ `[]AccountConfig` | ✅ |
| `units` | ✅ | ✅ `[]UnitConfig` | ✅ |
| `strategy.weights` | ✅ | ✅ `Weights` | ✅ |
| `strategy.long_threshold` | ✅ | ✅ `LongThreshold` | ✅ |
| `strategy.short_threshold` | ✅ | ✅ `ShortThreshold` | ✅ |
| `risk.guard` | ✅ | ✅ `GuardConfig` | ✅ |
| `risk.position_sizer` | ✅ | ✅ `SizerConfig` | ✅ |
| `auto_risk` | ✅ | ✅ `AutoRiskConfig` | ✅ |
| `global_risk` | ✅ | ✅ `GlobalRiskConfig` | ✅ |
| `signal_exit` | ✅ | ✅ `SignalExitConfig` | ✅ |
| `trailing_stop` | ✅ | ✅ `TrailingStopConfig` | ✅ |
| `webui` | ✅ | ✅ `WebUIConfig` | ✅ |

**状态**: ✅ 完全一致，且代码还额外实现了`MaxTradeSymbols`等实用配置。

### E. Tool 接口一致性

| Tool | 文档§13 | 代码实现 | 状态 |
|------|---------|---------|------|
| `quant.global_portfolio` | ✅ | ✅ | 完整 |
| `quant.global_risk_status` | ✅ | ✅ | 完整 |
| `quant.strategy_weights` | ✅ | ⚠️ | **仅返回策略名称列表，未返回实际权重值** |
| `quant.daily_pnl` | ✅ | ✅ | 完整 |
| `quant.account_status` | ✅ | ✅ | 完整 |
| `quant.pause_trading` | ✅ | ✅ | 完整 |
| `quant.resume_trading` | ✅ | ✅ | 完整 |
| `quant.account_pause` | ✅ | ✅ | 完整 |
| `quant.account_resume` | ✅ | ✅ | 完整 |
| `quant.account_close_all` | ✅ | ✅ | 完整 |
| `quant.force_close` | ✅ | ✅ | 完整 |
| `quant.trace_query` | ✅ | ✅ | 完整 |
| `quant.trade_history` | ✅ | ✅ | 完整 |
| `quant.backtest_start` | ✅ | ✅ | 完整 |

**差距**: `quant.strategy_weights` 工具描述为"查询当前各策略权重（含市场状态自适应调整后的权重）"，但实际实现只返回策略名称列表，未返回权重数值。

---

## 四、192维特征向量一致性

| 维度段 | 文档§10 | 代码 `tools.go:get_feature_vector` | 状态 |
|-------|---------|-----------------------------------|------|
| 0-59: 价格特征 | ✅ | ✅ `vec[0:60]` | ✅ |
| 60-99: 量特征 | ✅ | ✅ `vec[60:100]` | ✅ |
| 100-129: 微观结构 | ✅ | ✅ `vec[100:130]` | ✅ |
| 130-159: 动量 | ✅ | ✅ `vec[130:160]` | ✅ |
| 160-175: 跨资产 | ✅ | ✅ `vec[160:176]` | ✅ |
| 176-191: ML增强 | ✅ | ✅ `vec[176:192]` | ✅ |

**状态**: ✅ 完全一致。Data sidecar的`get_all_snapshots`返回`snap.FeatureVector [192]float64`，Quant sidecar通过`RemoteBufferManager`消费。

---

## 五、重构任务清单

### 🔴 高优先级

| # | 差距描述 | 涉及文件 | 改造方向 | 优先级 |
|---|---------|---------|---------|--------|
| 1 | **DataBrain.IsHealthy() 为stub** — 始终返回false，无法用于健康检查 | `brains/data/brain.go` | 修改: 实现真正的健康检查逻辑（Provider连通性、Validator状态、FeatureEngine运行状态、RingBuffer非空） | 🔴高 |
| 2 | **DataBrain.DataQualityScore() 缺失** — 文档§9明确要求的数据质量评分方法 | `brains/data/brain.go` + `brains/data/quality.go`(新建) | 新增: 实现基于校验拒绝率、延迟、完整性的综合质量评分 | 🔴高 |
| 3 | **data.replay_start / replay_stop 为stub** — Tool存在但返回not_implemented | `brains/data/sidecar/tools.go` | 修改: 实现真正的回放逻辑（需DataBrain支持Provider切换为ReplayProvider） | 🔴高 |
| 4 | **quant.strategy_weights Tool行为不完整** — 只返回策略名，不返回权重 | `brains/quant/sidecar/tools.go` | 修改: `strategyWeightsTool.Execute()` 应返回每个unit的实际权重map（含regime-aware调整后的权重） | 🔴高 |

### 🟡 中优先级

| # | 差距描述 | 涉及文件 | 改造方向 | 优先级 |
|---|---------|---------|---------|--------|
| 5 | **DataBrain熔断机制不完整** — 有cbFailCount等字段但无真正熔断逻辑 | `brains/data/brain.go` | 修改: 实现基于失败次数和冷却时间的自动熔断/恢复 | 🟡中 |
| 6 | **DataBrain.SubEngines() 缺失** — 文档§10.5要求列出特征子引擎 | `brains/data/brain.go` + `brains/data/feature/` | 新增: `SubEngines()` 方法返回各子引擎状态 | 🟡中 |
| 7 | **校验拒绝回调机制缺失** — 文档§8.5要求可配置校验拒绝时的回调 | `brains/data/brain.go` + `brains/data/validation.go` | 新增: 在Validator中支持注册拒绝回调函数 | 🟡中 |
| 8 | **L2超参学习层缺失** — 文档§11要求L2层对策略超参数进行元优化 | `brains/quant/learning/` | 新增: 实现L2 Hyper-parameter Optimizer | 🟡中 |
| 9 | **Quant Brain circuit breaker for exchange** — 文档暗示应有交易所级熔断 | `brains/quant/exchange/` | 新增: 交易所连续失败时的熔断机制 | 🟡中 |

### 🟢 低优先级

| # | 差距描述 | 涉及文件 | 改造方向 | 优先级 |
|---|---------|---------|---------|--------|
| 10 | **代码实现但文档未提及的功能** — MaxTradeSymbols、MAE/MFE追踪、Orphan清理、BudgetEquity等 | `brains/quant/brain.go`, `trading_unit.go` | 文档补全: 在Doc 37中补充这些已实现的功能说明 | 🟢低 |
| 11 | **WebUI实现验证** — 文档§12描述了WebUI，代码有配置和引用但实现未分析 | `brains/quant/webui/` | 验证: 确认webui包实现与文档描述一致 | 🟢低 |
| 12 | **Quant Brain的BrainLearner接口实现** — `QuantBrainLearner`和`DataBrainLearner`在代码中引用但未在本次分析中查看 | `brains/quant/learning/learner.go`, `brains/data/learning/learner.go` | 验证: 确认L0学习接口完整实现 | 🟢低 |

---

## 六、数据流验证图

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            三脑数据流验证                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌──────────────┐     ┌──────────────┐     ┌──────────────┐                │
│  │   Provider   │────→│  Validator   │────→│ Feature Eng. │                │
│  │  (OKX WS)    │     │ (3-sigma等)  │     │  (192-dim)   │                │
│  └──────────────┘     └──────────────┘     └──────┬───────┘                │
│                                                   │                         │
│                                                   ↓                         │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    Ring Buffer (ringbuf.BufferManager)               │   │
│  │  - MarketSnapshot: 价格/盘口/资金费率/持仓量/微观结构/FeatureVector  │   │
│  │  - Instruments(): []string                                           │   │
│  │  - Latest(instID): (MarketSnapshot, bool)                            │   │
│  └────────────────────────┬─────────────────────────────────────────────┘   │
│                           │                                                 │
│                           │ IPC (specialist.call_tool)                      │
│                           │ data.get_all_snapshots                          │
│                           ↓                                                 │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    Quant Brain (SnapshotSource)                      │   │
│  │  - RemoteBufferManager → local buffers.Latest()                      │   │
│  │  - CycleInterval (default 5s)                                        │   │
│  │  - Top N过滤 → TradingUnit.Evaluate()                                │   │
│  └────────────────────────┬─────────────────────────────────────────────┘   │
│                           │                                                 │
│                           ↓                                                 │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    TradingUnit Pipeline                              │   │
│  │  StrategyPool.Compute() → RegimeAwareAggregator.Aggregate()          │   │
│  │  → AdaptiveGuard.Evaluate() → BayesianSizer.Size()                   │   │
│  │  → Exchange.PlaceOrder()                                             │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ✅ 数据流: 完全一致                                                        │
│  ✅ 192维向量: Data.FeatureVector() ↔ RingBuffer ↔ Quant.SnapshotView     │
│  ✅ IPC: Data sidecar (data.get_all_snapshots) → Quant sidecar (Remote)  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 七、总结

### 7.1 已实现且对齐的部分（无需改动）

1. **核心架构**: Data的4层架构和Quant的6层pipeline均已完整实现
2. **192维特征向量**: 6大分组、192维定义完全一致
3. **三脑数据流**: Data → Ring Buffer → Quant → Exchange 完全对齐
4. **配置体系**: Data和Quant的配置字段、默认值、层级结构完全一致
5. **大部分Tool接口**: 14个Quant Tool + 8个Data Tool（除2个stub外）均已实现
6. **L1学习层**: WeightAdapter、SymbolScorer、SLTPOptimizer 均已实现
7. **风控体系**: AdaptiveGuard、GlobalRiskGuard、信号反转退出、移动止损均已实现

### 7.2 需要重构/补充的部分

1. **高优先级（4项）**:
   - `DataBrain.IsHealthy()` stub → 真正实现
   - `DataBrain.DataQualityScore()` → 新增实现
   - `data.replay_start/stop` → 从stub改为真实现
   - `quant.strategy_weights` → 返回真实权重而非仅名称

2. **中优先级（5项）**:
   - DataBrain熔断机制 → 补全逻辑
   - `SubEngines()` → 新增
   - 校验拒绝回调 → 新增
   - L2超参学习 → 新增
   - Exchange级熔断 → 新增

3. **低优先级（3项）**:
   - 文档补全（代码有但文档未提的功能）
   - WebUI实现验证
   - BrainLearner接口验证

### 7.3 建议的重构顺序

```
Phase 1 (高优先级, 1-2天):
  1. 修复 IsHealthy() stub
  2. 实现 DataQualityScore()
  3. 修复 strategy_weights Tool 返回真实权重
  4. replay_start/stop 实现或改为明确不可用状态

Phase 2 (中优先级, 2-3天):
  5. 实现 DataBrain 熔断机制
  6. 实现 SubEngines()
  7. 实现校验拒绝回调

Phase 3 (中优先级, 3-5天):
  8. 实现 L2 超参学习层
  9. 实现 Exchange 级熔断

Phase 4 (低优先级, 可选):
  10. 文档补全
  11. WebUI / BrainLearner 验证
```

---

*报告结束*
