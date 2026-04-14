package processor

import (
	"sync"

	"github.com/leef-l/brain/brains/data/provider"
)

// OrderBookState 保存某品种的订单簿状态及派生指标。
type OrderBookState struct {
	Bids       [5]provider.PriceLevel
	Asks       [5]provider.PriceLevel
	LastUpdate int64

	// 派生指标（每次更新自动计算）
	Imbalance float64 // (BidDepth - AskDepth) / (BidDepth + AskDepth), [-1, 1]
	Spread    float64 // (bestAsk - bestBid) / midPrice
	MidPrice  float64 // (bestBid + bestAsk) / 2
	BidDepth  float64 // sum of bid sizes
	AskDepth  float64 // sum of ask sizes
}

// Update 用新的订单簿快照更新状态。
func (s *OrderBookState) Update(book provider.OrderBook) {
	s.Bids = book.Bids
	s.Asks = book.Asks
	s.LastUpdate = book.Timestamp
	s.computeDerived()
}

func (s *OrderBookState) computeDerived() {
	s.BidDepth = 0
	s.AskDepth = 0
	for i := 0; i < 5; i++ {
		s.BidDepth += s.Bids[i].Size
		s.AskDepth += s.Asks[i].Size
	}

	total := s.BidDepth + s.AskDepth
	if total == 0 {
		s.Imbalance = 0
		s.Spread = 0
		s.MidPrice = 0
		return
	}
	s.Imbalance = (s.BidDepth - s.AskDepth) / total

	bestBid := s.Bids[0].Price
	bestAsk := s.Asks[0].Price
	s.MidPrice = (bestBid + bestAsk) / 2

	if s.MidPrice == 0 {
		s.Spread = 0
	} else {
		s.Spread = (bestAsk - bestBid) / s.MidPrice
	}
}

// OrderBookTracker 管理多个品种的订单簿状态。
type OrderBookTracker struct {
	books map[string]*OrderBookState // instID -> state
	mu    sync.RWMutex
}

// NewOrderBookTracker 创建订单簿追踪器。
func NewOrderBookTracker() *OrderBookTracker {
	return &OrderBookTracker{
		books: make(map[string]*OrderBookState),
	}
}

// Update 更新指定品种的订单簿。
func (t *OrderBookTracker) Update(instID string, book provider.OrderBook) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.books[instID]
	if !ok {
		s = &OrderBookState{}
		t.books[instID] = s
	}
	s.Update(book)
}

// Get 返回指定品种订单簿状态的副本。不存在时返回 nil。
func (t *OrderBookTracker) Get(instID string) *OrderBookState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.books[instID]
	if !ok {
		return nil
	}
	// 返回副本
	cp := *s
	return &cp
}

// Count 返回追踪的品种数量。
func (t *OrderBookTracker) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.books)
}
