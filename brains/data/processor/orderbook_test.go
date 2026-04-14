package processor

import (
	"math"
	"testing"

	"github.com/leef-l/brain/brains/data/provider"
)

func makeOrderBook(bidPrices, bidSizes, askPrices, askSizes [5]float64, ts int64) provider.OrderBook {
	var bids, asks [5]provider.PriceLevel
	for i := 0; i < 5; i++ {
		bids[i] = provider.PriceLevel{Price: bidPrices[i], Size: bidSizes[i]}
		asks[i] = provider.PriceLevel{Price: askPrices[i], Size: askSizes[i]}
	}
	return provider.OrderBook{
		InstID:    "BTC-USDT",
		Bids:      bids,
		Asks:      asks,
		Timestamp: ts,
	}
}

func TestOrderBookState_Update(t *testing.T) {
	book := makeOrderBook(
		[5]float64{100, 99, 98, 97, 96},
		[5]float64{10, 20, 30, 40, 50},
		[5]float64{101, 102, 103, 104, 105},
		[5]float64{5, 10, 15, 20, 25},
		1000,
	)
	s := &OrderBookState{}
	s.Update(book)

	if s.Bids[0].Price != 100 || s.Asks[0].Price != 101 {
		t.Fatal("Bids/Asks 未正确写入")
	}
	if s.LastUpdate != 1000 {
		t.Fatalf("LastUpdate 应为 1000，得到 %d", s.LastUpdate)
	}
}

func TestOrderBookState_Imbalance(t *testing.T) {
	// 买深 > 卖深 -> Imbalance > 0
	book := makeOrderBook(
		[5]float64{100, 99, 98, 97, 96},
		[5]float64{10, 10, 10, 10, 10}, // bidDepth = 50
		[5]float64{101, 102, 103, 104, 105},
		[5]float64{5, 5, 5, 5, 5}, // askDepth = 25
		1000,
	)
	s := &OrderBookState{}
	s.Update(book)
	if s.Imbalance <= 0 {
		t.Fatalf("买深 > 卖深时 Imbalance 应为正，得到 %f", s.Imbalance)
	}
	expected := (50.0 - 25.0) / (50.0 + 25.0)
	if math.Abs(s.Imbalance-expected) > 1e-9 {
		t.Fatalf("Imbalance 应为 %f，得到 %f", expected, s.Imbalance)
	}

	// 卖深 > 买深 -> Imbalance < 0
	book2 := makeOrderBook(
		[5]float64{100, 99, 98, 97, 96},
		[5]float64{1, 1, 1, 1, 1}, // bidDepth = 5
		[5]float64{101, 102, 103, 104, 105},
		[5]float64{10, 10, 10, 10, 10}, // askDepth = 50
		2000,
	)
	s.Update(book2)
	if s.Imbalance >= 0 {
		t.Fatalf("卖深 > 买深时 Imbalance 应为负，得到 %f", s.Imbalance)
	}
}

func TestOrderBookState_Spread(t *testing.T) {
	book := makeOrderBook(
		[5]float64{100, 99, 98, 97, 96},
		[5]float64{10, 10, 10, 10, 10},
		[5]float64{102, 103, 104, 105, 106},
		[5]float64{10, 10, 10, 10, 10},
		1000,
	)
	s := &OrderBookState{}
	s.Update(book)
	mid := (100.0 + 102.0) / 2
	expectedSpread := (102.0 - 100.0) / mid
	if math.Abs(s.Spread-expectedSpread) > 1e-9 {
		t.Fatalf("Spread 应为 %f，得到 %f", expectedSpread, s.Spread)
	}
}

func TestOrderBookState_MidPrice(t *testing.T) {
	book := makeOrderBook(
		[5]float64{100, 99, 98, 97, 96},
		[5]float64{10, 10, 10, 10, 10},
		[5]float64{102, 103, 104, 105, 106},
		[5]float64{10, 10, 10, 10, 10},
		1000,
	)
	s := &OrderBookState{}
	s.Update(book)
	expected := (100.0 + 102.0) / 2
	if math.Abs(s.MidPrice-expected) > 1e-9 {
		t.Fatalf("MidPrice 应为 %f，得到 %f", expected, s.MidPrice)
	}
}

func TestOrderBookState_Empty(t *testing.T) {
	// 空订单簿不 panic
	book := provider.OrderBook{
		InstID:    "BTC-USDT",
		Timestamp: 1000,
	}
	s := &OrderBookState{}
	s.Update(book)
	if s.Imbalance != 0 {
		t.Fatalf("空订单簿 Imbalance 应为 0，得到 %f", s.Imbalance)
	}
	if s.MidPrice != 0 {
		t.Fatalf("空订单簿 MidPrice 应为 0，得到 %f", s.MidPrice)
	}
}

func TestOrderBookTracker_GetCopy(t *testing.T) {
	tracker := NewOrderBookTracker()
	book := makeOrderBook(
		[5]float64{100, 99, 98, 97, 96},
		[5]float64{10, 10, 10, 10, 10},
		[5]float64{101, 102, 103, 104, 105},
		[5]float64{5, 5, 5, 5, 5},
		1000,
	)
	tracker.Update("BTC-USDT", book)

	got := tracker.Get("BTC-USDT")
	if got == nil {
		t.Fatal("Get 不应返回 nil")
	}

	// 修改副本不影响原始数据
	got.MidPrice = 999
	orig := tracker.Get("BTC-USDT")
	if orig.MidPrice == 999 {
		t.Fatal("Get 应返回副本，修改副本不应影响原始数据")
	}

	// 不存在的品种返回 nil
	if tracker.Get("NONEXIST") != nil {
		t.Fatal("不存在的品种应返回 nil")
	}

	if tracker.Count() != 1 {
		t.Fatalf("Count 应为 1，得到 %d", tracker.Count())
	}
}
