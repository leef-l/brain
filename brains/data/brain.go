package data

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leef-l/brain/brains/data/active"
	"github.com/leef-l/brain/brains/data/backfill"
	"github.com/leef-l/brain/brains/data/feature"
	"github.com/leef-l/brain/brains/data/processor"
	"github.com/leef-l/brain/brains/data/provider"
	"github.com/leef-l/brain/brains/data/ringbuf"
	"github.com/leef-l/brain/brains/data/router"
	"github.com/leef-l/brain/brains/data/store"
	"github.com/leef-l/brain/brains/data/validator"
)

// DataBrain 是数据大脑的核心结构，串联所有数据处理组件。
type DataBrain struct {
	config Config
	logger *slog.Logger

	// 组件
	store      store.Store
	provider   provider.DataProvider
	backfiller *backfill.Backfiller
	activeList *active.ActiveList
	validator  *validator.Validator
	router     *router.EventRouter

	// 处理器
	candles   *processor.CandleAggregator
	orderbook *processor.OrderBookTracker
	tradeflow *processor.TradeFlowTracker

	// 输出
	feature   *feature.Engine
	assembler *feature.FeatureAssembler
	buffers   *ringbuf.BufferManager

	// 状态
	running atomic.Bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// 指标
	metrics Metrics
}

// Metrics 持有 DataBrain 的运行指标。
type Metrics struct {
	WSMessagesTotal   atomic.Int64
	ValidatorRejected atomic.Int64
	RingBufWriteTotal atomic.Int64
	PGWriteTotal      atomic.Int64
	PGWriteErrors     atomic.Int64
	FeatureComputeMs  atomic.Int64 // 最后一次计算耗时（毫秒）
}

// candleDepth returns the ring buffer candle depth from config with a default.
func candleDepth(cfg Config) int {
	if cfg.RingBuffer.CandleDepth > 0 {
		return cfg.RingBuffer.CandleDepth
	}
	return 1024
}

// New 创建 DataBrain（不启动）。store 允许为 nil（仅测试用途）。
func New(cfg Config, st store.Store, logger *slog.Logger) *DataBrain {
	if logger == nil {
		logger = slog.Default()
	}

	// 处理器
	candles := processor.NewCandleAggregator()
	orderbook := processor.NewOrderBookTracker()
	tradeflow := processor.NewTradeFlowTracker(300_000) // 5 分钟窗口

	// Feature Engine + Assembler
	feat := feature.NewEngine(candles, orderbook, tradeflow)
	fallback := feature.NewRuleFallback(candles, orderbook, tradeflow)
	mlEngine := feature.NewStatisticalMLEngine() // 统计增强引擎，50 次 Update 后自动就绪
	assembler := feature.NewFeatureAssembler(feat, mlEngine, fallback)

	// ActiveList
	alCfg := active.Config{
		RESTURL:          "https://www.okx.com",
		MinVolume24h:     cfg.ActiveList.MinVolume24h,
		MaxInstruments:   cfg.ActiveList.MaxInstruments,
		UpdateInterval:   cfg.ActiveList.UpdateInterval,
		AlwaysInclude:    cfg.ActiveList.AlwaysInclude,
		RankByVolatility: cfg.ActiveList.RankByVolatility,
		MinAmplitudePct:  cfg.ActiveList.MinAmplitudePct,
	}
	if alCfg.MaxInstruments == 0 {
		alCfg.MaxInstruments = 100
	}
	if alCfg.MinVolume24h == 0 {
		alCfg.MinVolume24h = 10_000_000
	}
	if alCfg.UpdateInterval == 0 {
		alCfg.UpdateInterval = 7 * 24 * time.Hour
	}
	al := active.New(alCfg, &http.Client{Timeout: 30 * time.Second})

	// Validator — use config values with sensible fallbacks.
	staleMs := int64(300000)
	if cfg.Validation.StaleTimeout > 0 {
		staleMs = cfg.Validation.StaleTimeout.Milliseconds()
	}
	gapThreshold := 3
	if cfg.Validation.MaxGapDuration > 0 {
		// Convert gap duration to number of candle periods (assume 1m default).
		gapThreshold = int(cfg.Validation.MaxGapDuration / time.Minute)
		if gapThreshold < 1 {
			gapThreshold = 3
		}
	}
	// Allow future timestamps up to 120s — covers 1m candle period-end timestamps
	// and minor clock skew, but rejects truly invalid far-future data (e.g. 8h ahead).
	futureMs := int64(120_000)
	vCfg := validator.Config{
		MaxPriceChangePct:    cfg.Validation.MaxPriceJump * 100, // Config 用比率，Validator 用百分比
		MaxFutureTSMs:        futureMs,
		MaxStaleTSMs:         staleMs,
		GapBackfillThreshold: gapThreshold,
	}
	if vCfg.MaxPriceChangePct == 0 {
		vCfg.MaxPriceChangePct = 10.0
	}
	v := validator.New(vCfg, func(alert validator.Alert) {
		logger.Warn("data quality alert",
			"level", alert.Level,
			"type", alert.Type,
			"symbol", alert.Symbol,
			"detail", alert.Detail,
		)
		if st != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = st.InsertAlert(ctx, store.AlertRecord{
				Level:     alert.Level,
				AlertType: alert.Type,
				Symbol:    alert.Symbol,
				Detail:    alert.Detail,
				EventTS:   alert.EventTS.UnixMilli(),
			})
		}
	})

	return &DataBrain{
		config:     cfg,
		logger:     logger,
		store:      st,
		activeList: al,
		validator:  v,
		router:     router.New(),
		candles:    candles,
		orderbook:  orderbook,
		tradeflow:  tradeflow,
		feature:    feat,
		assembler:  assembler,
		buffers:    ringbuf.NewBufferManager(candleDepth(cfg)),
	}
}

// Start 启动数据大脑。
func (b *DataBrain) Start(ctx context.Context) error {
	if b.running.Load() {
		return fmt.Errorf("data brain already running")
	}

	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel

	// 1. 刷新活跃品种列表
	instruments, err := b.activeList.Refresh(ctx)
	if err != nil {
		b.logger.Warn("refresh active list failed, using defaults", "err", err)
	} else {
		b.logger.Info("active instruments refreshed", "count", len(instruments))
		b.persistActiveInstruments(ctx, instruments)
	}

	// 2. 从数据库加载历史 K 线（用于特征计算）
	if b.store != nil {
		instIDs := b.activeList.List()
		loaded := 0
		for _, instID := range instIDs {
			for _, tf := range []string{"1m", "5m", "15m", "1H", "4H"} {
				candles, err := b.loadCandlesForFeature(ctx, instID, tf, 500)
				if err != nil || len(candles) == 0 {
					continue
				}
				w := b.candles.GetWindow(instID, tf)
				w.LoadHistory(candles)
				loaded++
			}
		}
		b.logger.Info("loaded historical candles for feature computation", "windows", loaded)
	}

	// 3. 后台历史回填（不阻塞启动）
	if b.config.Backfill.Enabled && b.store != nil {
		bf := backfill.New(&http.Client{Timeout: 30 * time.Second}, b.store, backfill.Config{
			RESTURL:    "https://www.okx.com",
			GoBack:     time.Duration(b.config.Backfill.MaxDays) * 24 * time.Hour,
			Timeframes: []string{"1m", "5m", "15m", "1H", "4H"},
			MaxBars:    b.config.Backfill.BatchSize,
			RateLimit:  5,
		})
		b.backfiller = bf

		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			instIDs := b.activeList.List()
			if err := bf.BackfillAll(ctx, instIDs); err != nil {
				b.logger.Error("backfill failed", "err", err)
			} else {
				b.logger.Info("backfill completed", "instruments", len(instIDs))
				// Reload CandleWindows from PG so the feature engine sees
				// the freshly backfilled data — without this, strategies
				// that started before backfill finished see zero vectors.
				reloaded := 0
				for _, id := range instIDs {
					for _, tf := range []string{"1m", "5m", "15m", "1H", "4H"} {
						candles, err := b.loadCandlesForFeature(ctx, id, tf, 500)
						if err != nil || len(candles) == 0 {
							continue
						}
						w := b.candles.GetWindow(id, tf)
						w.LoadHistory(candles)
						reloaded++
					}
				}
				b.logger.Info("reloaded candle windows after backfill", "windows", reloaded)
			}
		}()
	}

	// 3. 创建 OKX Provider
	instIDs := b.activeList.List()
	if len(instIDs) == 0 {
		// Refresh failed (e.g. network issue) — seed with defaults so that
		// the WebSocket subscription AND the activeList agree on the set of
		// tracked instruments.  Without this, data arrives via WS but
		// dispatchEvent drops it because activeList.IsActive returns false.
		instIDs = []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP", "SOL-USDT-SWAP"}
		b.activeList.Seed(instIDs)
		b.logger.Warn("active list empty after refresh, seeded defaults", "instruments", instIDs)
	}
	p := provider.NewOKXSwapProvider("okx-swap", provider.OKXSwapConfig{
		Instruments: instIDs,
	})
	b.provider = p

	// 4. Provider.Subscribe(router) — router 实现了 DataSink
	if err := b.provider.Subscribe(b.router); err != nil {
		cancel()
		return fmt.Errorf("subscribe provider: %w", err)
	}

	// 5. 启动消费 goroutine
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.consumeRealtime(ctx)
	}()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.consumeNearRT(ctx)
	}()

	// 6. 启动 feature 更新 goroutine
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.featureLoop(ctx)
	}()

	// 7. 启动活跃列表定期刷新
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.refreshLoop(ctx)
	}()

	// 8. Provider.Start()
	if err := b.provider.Start(ctx); err != nil {
		cancel()
		return fmt.Errorf("start provider: %w", err)
	}

	b.running.Store(true)
	b.logger.Info("data brain started", "instruments", len(instIDs))
	return nil
}

// Stop 优雅停止数据大脑。
func (b *DataBrain) Stop(ctx context.Context) error {
	if !b.running.Load() {
		return nil
	}

	b.logger.Info("stopping data brain")

	// 取消 context，通知所有 goroutine 退出
	if b.cancel != nil {
		b.cancel()
	}

	// 等待所有 goroutine 完成
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		b.logger.Warn("stop timeout, some goroutines may still be running")
	}

	// 停止 Provider
	if b.provider != nil {
		if err := b.provider.Stop(ctx); err != nil {
			b.logger.Error("stop provider failed", "err", err)
		}
	}

	// 关闭 store
	if b.store != nil {
		if err := b.store.Close(); err != nil {
			b.logger.Error("close store failed", "err", err)
		}
	}

	b.running.Store(false)
	b.logger.Info("data brain stopped")
	return nil
}

// consumeRealtime 消费实时事件（tick/订单簿/逐笔成交）。
func (b *DataBrain) consumeRealtime(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-b.router.RealtimeCh:
			b.metrics.WSMessagesTotal.Add(1)
			if !b.validator.Validate(&event) {
				b.metrics.ValidatorRejected.Add(1)
				continue
			}
			b.dispatchEvent(event)
		}
	}
}

// consumeNearRT 消费近实时事件（K 线/资金费率）。
func (b *DataBrain) consumeNearRT(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-b.router.NearRTCh:
			b.metrics.WSMessagesTotal.Add(1)
			if !b.validator.Validate(&event) {
				b.metrics.ValidatorRejected.Add(1)
				continue
			}
			b.dispatchEvent(event)
		}
	}
}

// dispatchEvent 分发事件到对应处理器。
func (b *DataBrain) dispatchEvent(event provider.DataEvent) {
	switch p := event.Payload.(type) {
	case provider.Candle:
		tf := extractTimeframe(event.Topic)
		b.candles.OnCandle(event.Symbol, tf, p)
		if b.activeList.IsActive(event.Symbol) {
			b.persistCandle(p, tf)
		}
	case []provider.Candle:
		for _, c := range p {
			tf := extractTimeframe(event.Topic)
			b.candles.OnCandle(event.Symbol, tf, c)
			if b.activeList.IsActive(event.Symbol) {
				b.persistCandle(c, tf)
			}
		}
	case provider.Trade:
		b.tradeflow.OnTrade(event.Symbol, p)
	case []provider.Trade:
		for _, t := range p {
			b.tradeflow.OnTrade(event.Symbol, t)
		}
	case *provider.OrderBook:
		b.orderbook.Update(event.Symbol, *p)
	case provider.FundingRate:
		_ = p
	case []provider.FundingRate:
		_ = p
	}
}

// featureLoop 每秒计算特征向量并写入 Ring Buffer。
func (b *DataBrain) featureLoop(ctx context.Context) {
	interval := b.config.Feature.Interval
	if interval <= 0 {
		interval = 1 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	persistTicker := time.NewTicker(1 * time.Minute)
	defer persistTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.updateFeatures()
		case <-persistTicker.C:
			b.persistFeatures()
		}
	}
}

// updateFeatures 计算所有活跃品种的特征向量，写入 Ring Buffer。
func (b *DataBrain) updateFeatures() {
	start := time.Now()
	instruments := b.activeList.List()
	for _, instID := range instruments {
		output := b.assembler.Compute(instID)

		snap := ringbuf.MarketSnapshot{
			InstID:        instID,
			Timestamp:     time.Now().UnixMilli(),
			FeatureVector: output.Vector,
			MLSource:      output.MLSource,
			MLReady:       output.MLReady,
			MarketRegime:  output.MarketRegimeLabel(),
			AnomalyLevel:  output.AnomalyLevel(),
			VolPercentile: output.VolPercentile(),
		}

		// 填充价格数据
		if w := b.candles.GetWindow(instID, "1m"); w != nil && w.Current.Close != 0 {
			snap.CurrentPrice = w.Current.Close
		}
		if ob := b.orderbook.Get(instID); ob != nil {
			snap.BidPrice = ob.Bids[0].Price
			snap.AskPrice = ob.Asks[0].Price
			snap.OrderBookImbalance = ob.Imbalance
			snap.Spread = ob.Spread
		}
		if flow := b.tradeflow.Get(instID); flow != nil {
			snap.TradeFlowToxicity = flow.Toxicity()
			snap.BigBuyRatio = flow.BigBuyRatio()
			snap.BigSellRatio = flow.BigSellRatio()
			snap.TradeDensityRatio = flow.TradeDensityRatio()
			snap.BuySellRatio = flow.BuySellRatio()
		}

		b.buffers.Write(instID, snap)
		b.metrics.RingBufWriteTotal.Add(1)
	}
	b.metrics.FeatureComputeMs.Store(time.Since(start).Milliseconds())
}

// loadCandlesForFeature 从数据库加载最近 N 条 K 线用于特征计算。
func (b *DataBrain) loadCandlesForFeature(ctx context.Context, instID, tf string, limit int) ([]provider.Candle, error) {
	if b.store == nil {
		return nil, nil
	}
	latest, err := b.store.LatestTimestamp(ctx, instID, tf)
	if err != nil || latest == 0 {
		return nil, nil
	}
	from := latest - int64(limit)*barMinutes(tf)*60*1000
	to := latest + 1
	rows, err := b.store.QueryRange(ctx, instID, tf, from, to)
	if err != nil {
		return nil, err
	}
	candles := make([]provider.Candle, len(rows))
	for i, r := range rows {
		candles[i] = provider.Candle{
			InstID:    r.InstID,
			Bar:       r.Bar,
			Timestamp: r.Timestamp,
			Open:      r.Open,
			High:      r.High,
			Low:       r.Low,
			Close:     r.Close,
			Volume:    r.Volume,
			VolumeCcy: r.VolumeCcy,
		}
	}
	return candles, nil
}

// barMinutes 返回每个 bar 对应的分钟数。
func barMinutes(bar string) int64 {
	switch bar {
	case "1m":
		return 1
	case "5m":
		return 5
	case "15m":
		return 15
	case "1H":
		return 60
	case "4H":
		return 240
	default:
		return 1
	}
}

// persistActiveInstruments 将活跃品种快照写入 PG。
func (b *DataBrain) persistActiveInstruments(ctx context.Context, instruments []active.InstrumentInfo) {
	if b.store == nil || len(instruments) == 0 {
		return
	}
	nowMS := time.Now().UnixMilli()
	records := make([]store.ActiveInstrumentRecord, len(instruments))
	for i, inst := range instruments {
		records[i] = store.ActiveInstrumentRecord{
			InstID:      inst.InstID,
			VolUSDT24h:  inst.VolUsdt24h,
			Rank:        i + 1,
			RefreshedAt: nowMS,
		}
	}
	if err := b.store.InsertActiveInstruments(ctx, records); err != nil {
		b.logger.Error("persist active instruments failed", "err", err)
	}
}

// persistCandle 将 K 线写入 PG。
func (b *DataBrain) persistCandle(c provider.Candle, tf string) {
	if b.store == nil {
		return
	}
	sc := store.Candle{
		InstID:    c.InstID,
		Bar:       tf,
		Timestamp: c.Timestamp,
		Open:      c.Open,
		High:      c.High,
		Low:       c.Low,
		Close:     c.Close,
		Volume:    c.Volume,
		VolumeCcy: c.VolumeCcy,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.store.Upsert(ctx, sc); err != nil {
		b.metrics.PGWriteErrors.Add(1)
		b.logger.Error("persist candle failed", "err", err, "inst", c.InstID)
	} else {
		b.metrics.PGWriteTotal.Add(1)
	}
}

// persistFeatures 将活跃品种的特征向量写入 PG。
func (b *DataBrain) persistFeatures() {
	if b.store == nil {
		return
	}
	instruments := b.activeList.List()
	for _, instID := range instruments {
		vec := b.feature.Compute(instID)
		sv := store.FeatureVector{
			Collection: "default",
			InstID:     instID,
			Timeframe:  "1m",
			Timestamp:  time.Now().UnixMilli(),
			Vector:     feature.Serialize(vec),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := b.store.Insert(ctx, sv); err != nil {
			b.metrics.PGWriteErrors.Add(1)
		} else {
			b.metrics.PGWriteTotal.Add(1)
		}
		cancel()
	}
}

// refreshLoop 定期刷新活跃品种列表。
func (b *DataBrain) refreshLoop(ctx context.Context) {
	interval := b.config.ActiveList.UpdateInterval
	if interval == 0 {
		interval = 7 * 24 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			instruments, err := b.activeList.Refresh(ctx)
			if err != nil {
				b.logger.Error("refresh active list failed", "err", err)
			} else {
				b.logger.Info("active list refreshed", "count", b.activeList.Count())
				b.persistActiveInstruments(ctx, instruments)
			}
		}
	}
}

// extractTimeframe 从 topic 提取时间框架。
// "candle.1m.BTC-USDT-SWAP" → "1m"
func extractTimeframe(topic string) string {
	parts := strings.SplitN(topic, ".", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return "1m"
}

// Health 返回健康状态摘要。
func (b *DataBrain) Health() map[string]any {
	return map[string]any{
		"running":            b.running.Load(),
		"ws_messages":        b.metrics.WSMessagesTotal.Load(),
		"validator_rejected": b.metrics.ValidatorRejected.Load(),
		"ringbuf_writes":     b.metrics.RingBufWriteTotal.Load(),
		"pg_writes":          b.metrics.PGWriteTotal.Load(),
		"pg_errors":          b.metrics.PGWriteErrors.Load(),
		"feature_compute_ms": b.metrics.FeatureComputeMs.Load(),
		"active_instruments": b.activeList.Count(),
	}
}

// Buffers 返回 BufferManager（供 Quant Brain 使用）。
func (b *DataBrain) Buffers() *ringbuf.BufferManager {
	return b.buffers
}

// Candles 返回指定品种和时间框架的历史 K 线（最近 500 根）。
// 供 Quant Brain 获取 candle history 传给策略（如 BreakoutMomentum）。
func (b *DataBrain) Candles(instID, timeframe string) []provider.Candle {
	w := b.candles.GetWindow(instID, timeframe)
	if w == nil {
		return nil
	}
	return append([]provider.Candle(nil), w.HistoryCandles...)
}

// ActiveInstruments 返回当前活跃品种列表。
func (b *DataBrain) ActiveInstruments() []string {
	return b.activeList.List()
}

// OrderBook 返回指定品种的订单簿状态。
func (b *DataBrain) OrderBook(instID string) *processor.OrderBookState {
	return b.orderbook.Get(instID)
}

// TradeFlow 返回指定品种的逐笔成交窗口。
func (b *DataBrain) TradeFlow(instID string) *processor.FlowWindow {
	return b.tradeflow.Get(instID)
}

// FeatureVector 返回指定品种的完整 192 维特征向量。
func (b *DataBrain) FeatureVector(instID string) feature.FeatureOutput {
	return b.assembler.Compute(instID)
}

// ProviderHealth 返回数据源健康状态。
func (b *DataBrain) ProviderHealth() *provider.ProviderHealth {
	if b.provider == nil {
		return nil
	}
	h := b.provider.Health()
	return &h
}

// Store 返回底层存储（供 sidecar 查询回填进度等）。
func (b *DataBrain) Store() store.Store {
	return b.store
}
