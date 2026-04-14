package processor

import (
	"math"
	"sync"

	"github.com/leef-l/brain/brains/data/provider"
)

const defaultHistoryCap = 500

// CandleWindow -- 每品种每时间框架一个窗口
type CandleWindow struct {
	InstID    string
	Timeframe string
	Current   provider.Candle
	History   *RingSlice        // 500 根滚动窗口，存 Close 价格（供指标用）
	HistoryCandles []provider.Candle // 最近 500 根完整 K 线（供回测/查询用）

	// 增量指标
	EMA9  *IncrementalEMA
	EMA21 *IncrementalEMA
	EMA55 *IncrementalEMA
	RSI14 *IncrementalRSI
	ATR14 *IncrementalATR
	MACD  *IncrementalMACD
	ADX14 *IncrementalADX
	BB20  *IncrementalBB

	hasCandle bool // Current 是否已设置
}

// NewCandleWindow 创建并初始化一个 K 线窗口。
func NewCandleWindow(instID, timeframe string) *CandleWindow {
	return &CandleWindow{
		InstID:    instID,
		Timeframe: timeframe,
		History:   NewRingSlice(defaultHistoryCap),
		EMA9:      NewEMA(9),
		EMA21:     NewEMA(21),
		EMA55:     NewEMA(55),
		RSI14:     NewRSI(14),
		ATR14:     NewATR(14),
		MACD:      NewMACD(),
		ADX14:     NewADX(14),
		BB20:      NewBB(20, 2.0),
	}
}

// OnCandle 处理新 K 线到达。
func (w *CandleWindow) OnCandle(c provider.Candle) {
	// 1. 如果 Current 不为空且 c.Timestamp != Current.Timestamp -> rotate
	if w.hasCandle && c.Timestamp != w.Current.Timestamp {
		w.History.Push(w.Current.Close)
		w.HistoryCandles = append(w.HistoryCandles, w.Current)
		if len(w.HistoryCandles) > defaultHistoryCap {
			w.HistoryCandles = w.HistoryCandles[len(w.HistoryCandles)-defaultHistoryCap:]
		}
	}

	// 2. 更新 Current
	w.Current = c
	w.hasCandle = true

	// 3. 更新所有增量指标
	w.EMA9.Update(c.Close)
	w.EMA21.Update(c.Close)
	w.EMA55.Update(c.Close)
	w.RSI14.Update(c.Close)
	w.MACD.Update(c.Close)
	w.ATR14.UpdateOHLC(c.Open, c.High, c.Low, c.Close)
	w.ADX14.UpdateOHLC(c.Open, c.High, c.Low, c.Close)
	w.BB20.Update(c.Close)
}

// LoadHistory 从历史 K 线序列初始化（启动时从 PG 加载）。
func (w *CandleWindow) LoadHistory(candles []provider.Candle) {
	for _, c := range candles {
		w.OnCandle(c)
	}
}

// PriceChangeRate 计算当前价格相对 n 根前价格的变化率。
// 如果 History 不够 n 根，返回 0。
func (w *CandleWindow) PriceChangeRate(n int) float64 {
	hLen := w.History.Len()
	if hLen < n || n <= 0 {
		return 0
	}
	oldPrice := w.History.Get(hLen - n)
	if oldPrice == 0 {
		return 0
	}
	return (w.Current.Close - oldPrice) / oldPrice
}

// Volatility 计算最近 n 根 Close 的标准差 / 均值（变异系数）。
func (w *CandleWindow) Volatility(n int) float64 {
	hLen := w.History.Len()
	if hLen < n || n <= 0 {
		return 0
	}
	var sum float64
	start := hLen - n
	for i := start; i < hLen; i++ {
		sum += w.History.Get(i)
	}
	mean := sum / float64(n)
	if mean == 0 {
		return 0
	}
	var sumSq float64
	for i := start; i < hLen; i++ {
		d := w.History.Get(i) - mean
		sumSq += d * d
	}
	stddev := math.Sqrt(sumSq / float64(n))
	return stddev / mean
}

// BBPosition 返回当前价格在布林带中的相对位置。
func (w *CandleWindow) BBPosition() float64 {
	return w.BB20.Position(w.Current.Close)
}

// CandleAggregator -- 管理所有品种所有时间框架的窗口
type CandleAggregator struct {
	windows map[string]*CandleWindow // key: "instID:timeframe"
	mu      sync.RWMutex
}

// NewCandleAggregator 创建 K 线聚合器。
func NewCandleAggregator() *CandleAggregator {
	return &CandleAggregator{
		windows: make(map[string]*CandleWindow),
	}
}

func candleKey(instID, timeframe string) string {
	return instID + ":" + timeframe
}

// GetWindow 获取或自动创建指定品种/时间框架的窗口。
func (a *CandleAggregator) GetWindow(instID, timeframe string) *CandleWindow {
	key := candleKey(instID, timeframe)
	a.mu.RLock()
	w, ok := a.windows[key]
	a.mu.RUnlock()
	if ok {
		return w
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	// double check
	if w, ok = a.windows[key]; ok {
		return w
	}
	w = NewCandleWindow(instID, timeframe)
	a.windows[key] = w
	return w
}

// OnCandle 将 K 线转发到对应的 CandleWindow。
func (a *CandleAggregator) OnCandle(instID, timeframe string, c provider.Candle) {
	w := a.GetWindow(instID, timeframe)
	w.OnCandle(c)
}

// WindowCount 返回当前窗口数量。
func (a *CandleAggregator) WindowCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.windows)
}
