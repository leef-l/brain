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
	feature *feature.Engine
	buffers *ringbuf.BufferManager

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

// New 创建 DataBrain（不启动）。store 允许为 nil（仅测试用途）。
func New(cfg Config, st store.Store, logger *slog.Logger) *DataBrain {
	if logger == nil {
		logger = slog.Default()
	}

	// 处理器
	candles := processor.NewCandleAggregator()
	orderbook := processor.NewOrderBookTracker()
	tradeflow := processor.NewTradeFlowTracker(300_000) // 5 分钟窗口

	// Feature Engine
	feat := feature.NewEngine(candles, orderbook, tradeflow)

	// ActiveList
	alCfg := active.Config{
		RESTURL:        "https://www.okx.com",
		MinVolume24h:   cfg.ActiveList.MinVolume24h,
		MaxInstruments: cfg.ActiveList.MaxInstruments,
		UpdateInterval: cfg.ActiveList.UpdateInterval,
		AlwaysInclude:  cfg.ActiveList.AlwaysInclude,
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

	// Validator
	vCfg := validator.Config{
		MaxPriceChangePct:    cfg.Validation.MaxPriceJump * 100, // Config 用比率，Validator 用百分比
		MaxFutureTSMs:        5000,
		MaxStaleTSMs:         300000,
		GapBackfillThreshold: 3,
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
	})

	return &DataBrain{
		config:    cfg,
		logger:    logger,
		store:     st,
		activeList: al,
		validator: v,
		router:    router.New(),
		candles:   candles,
		orderbook: orderbook,
		tradeflow: tradeflow,
		feature:   feat,
		buffers:   ringbuf.NewBufferManager(1024),
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
	}

	// 2. 后台历史回填（不阻塞启动）
	if b.config.Backfill.Enabled && b.store != nil {
		bf := backfill.New(&http.Client{Timeout: 30 * time.Second}, b.store, backfill.Config{
			RESTURL:    "https://www.okx.com",
			GoBack:     time.Duration(b.config.Backfill.MaxDays) * 24 * time.Hour,
			Timeframes: []string{"1m", "5m", "15m", "1H", "4H"},
			MaxBars:    b.config.Backfill.BatchSize,
			RateLimit:  20,
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
			}
		}()
	}

	// 3. 创建 OKX Provider
	instIDs := b.activeList.List()
	if len(instIDs) == 0 {
		instIDs = []string{"BTC-USDT-SWAP", "ETH-USDT-SWAP"}
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
	case []provider.Candle:
		for _, c := range p {
			tf := extractTimeframe(event.Topic)
			b.candles.OnCandle(event.Symbol, tf, c)
			if b.activeList.IsActive(event.Symbol) {
				b.persistCandle(c, tf)
			}
		}
	case []provider.Trade:
		for _, t := range p {
			b.tradeflow.OnTrade(event.Symbol, t)
		}
	case *provider.OrderBook:
		b.orderbook.Update(event.Symbol, *p)
	case []provider.FundingRate:
		// 暂存：未来供 feature engine 使用
		_ = p
	}
}

// featureLoop 每秒计算特征向量并写入 Ring Buffer。
func (b *DataBrain) featureLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
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
		vec := b.feature.ComputeArray(instID)

		snap := ringbuf.MarketSnapshot{
			InstID:        instID,
			Timestamp:     time.Now().UnixMilli(),
			FeatureVector: vec,
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
		if tf := b.tradeflow.Get(instID); tf != nil {
			snap.TradeFlowToxicity = tf.Toxicity()
			snap.BigBuyRatio = tf.BigBuyRatio()
			snap.TradeDensityRatio = tf.TradeDensityRatio()
		}

		b.buffers.Write(instID, snap)
		b.metrics.RingBufWriteTotal.Add(1)
	}
	b.metrics.FeatureComputeMs.Store(time.Since(start).Milliseconds())
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
			if _, err := b.activeList.Refresh(ctx); err != nil {
				b.logger.Error("refresh active list failed", "err", err)
			} else {
				b.logger.Info("active list refreshed", "count", b.activeList.Count())
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
