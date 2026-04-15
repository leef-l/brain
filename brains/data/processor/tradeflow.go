package processor

import (
	"sync"
	"time"

	"github.com/leef-l/brain/brains/data/provider"
)

type tradeEntry struct {
	Price float64
	Size  float64
	Side  string // "buy" / "sell"
	TS    int64
	IsBig bool // 入场时是否被判定为大单
}

// FlowWindow 逐笔成交滑动窗口分析器。
type FlowWindow struct {
	mu         sync.RWMutex
	trades     []tradeEntry
	windowMs   int64   // 窗口大小（毫秒）
	buyVolume  float64
	sellVolume float64
	bigBuyVol  float64
	bigSellVol float64
	tradeCount int
	avgSize    float64 // 滑动平均成交量，用于判断大单
	totalSize  float64 // 累计成交量（用于计算 avgSize）
	totalCount int     // 累计成交笔数
}

// NewFlowWindow 创建逐笔成交窗口，windowMs 为窗口大小（毫秒），默认 300000 (5min)。
func NewFlowWindow(windowMs int64) *FlowWindow {
	if windowMs <= 0 {
		windowMs = 300000
	}
	return &FlowWindow{
		windowMs: windowMs,
	}
}

// OnTrade 处理新成交。
func (w *FlowWindow) OnTrade(t provider.Trade) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 累加买卖量
	if t.Side == "buy" {
		w.buyVolume += t.Size
	} else {
		w.sellVolume += t.Size
	}

	// 更新滑动平均成交量
	w.totalCount++
	w.totalSize += t.Size
	w.avgSize = w.totalSize / float64(w.totalCount)

	// 判断大单: size > avgSize * 3
	// 必须在 append 之前确定 IsBig，因为 append 是值拷贝。
	isBig := w.avgSize > 0 && t.Size > w.avgSize*3
	if isBig {
		if t.Side == "buy" {
			w.bigBuyVol += t.Size
		} else {
			w.bigSellVol += t.Size
		}
	}

	entry := tradeEntry{
		Price: t.Price,
		Size:  t.Size,
		Side:  t.Side,
		TS:    t.Timestamp,
		IsBig: isBig,
	}
	w.trades = append(w.trades, entry)
	w.tradeCount++

	// 清理过期数据
	now := t.Timestamp
	if now == 0 {
		now = time.Now().UnixMilli()
	}
	w.cleanup(now)
}

func (w *FlowWindow) cleanup(now int64) {
	cutoff := now - w.windowMs
	i := 0
	for i < len(w.trades) && w.trades[i].TS < cutoff {
		e := w.trades[i]
		// 减去过期的 volume
		if e.Side == "buy" {
			w.buyVolume -= e.Size
			if e.IsBig {
				w.bigBuyVol -= e.Size
			}
		} else {
			w.sellVolume -= e.Size
			if e.IsBig {
				w.bigSellVol -= e.Size
			}
		}
		w.tradeCount--
		i++
	}
	if i > 0 {
		w.trades = w.trades[i:]
	}
	// Clamp to avoid floating-point drift below zero.
	if w.buyVolume < 0 {
		w.buyVolume = 0
	}
	if w.sellVolume < 0 {
		w.sellVolume = 0
	}
	if w.bigBuyVol < 0 {
		w.bigBuyVol = 0
	}
	if w.bigSellVol < 0 {
		w.bigSellVol = 0
	}
}

// Toxicity 返回成交毒性指标。
// |buyVolume - sellVolume| / (buyVolume + sellVolume)
// 0 = 均衡, 1 = 完全单边
func (w *FlowWindow) Toxicity() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	total := w.buyVolume + w.sellVolume
	if total <= 0 {
		return 0
	}
	diff := w.buyVolume - w.sellVolume
	if diff < 0 {
		diff = -diff
	}
	return diff / total
}

// BigBuyRatio 返回大单买入占比。
func (w *FlowWindow) BigBuyRatio() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	total := w.buyVolume + w.sellVolume
	if total <= 0 {
		return 0
	}
	return w.bigBuyVol / total
}

// BigSellRatio 返回大单卖出占比。
func (w *FlowWindow) BigSellRatio() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	total := w.buyVolume + w.sellVolume
	if total <= 0 {
		return 0
	}
	return w.bigSellVol / total
}

// TradeDensityRatio 返回每秒成交笔数。
func (w *FlowWindow) TradeDensityRatio() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	windowSec := float64(w.windowMs) / 1000.0
	if windowSec <= 0 {
		return 0
	}
	return float64(w.tradeCount) / windowSec
}

// BuySellRatio 返回买入量占比，0.5 = 均衡。
func (w *FlowWindow) BuySellRatio() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	total := w.buyVolume + w.sellVolume
	if total <= 0 {
		return 0
	}
	return w.buyVolume / total
}

// TradeFlowTracker 管理多个品种的逐笔成交窗口。
type TradeFlowTracker struct {
	windows  map[string]*FlowWindow // instID -> window
	windowMs int64
	mu       sync.RWMutex
}

// NewTradeFlowTracker 创建逐笔成交追踪器。
func NewTradeFlowTracker(windowMs int64) *TradeFlowTracker {
	if windowMs <= 0 {
		windowMs = 300000
	}
	return &TradeFlowTracker{
		windows:  make(map[string]*FlowWindow),
		windowMs: windowMs,
	}
}

// OnTrade 将成交转发到对应品种的窗口。
func (t *TradeFlowTracker) OnTrade(instID string, trade provider.Trade) {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.windows[instID]
	if !ok {
		w = NewFlowWindow(t.windowMs)
		t.windows[instID] = w
	}
	w.OnTrade(trade)
}

// Get 返回指定品种的 FlowWindow 指针。不存在时返回 nil。
func (t *TradeFlowTracker) Get(instID string) *FlowWindow {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.windows[instID]
}

// Count 返回追踪的品种数量。
func (t *TradeFlowTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.windows)
}
