package processor

import (
	"math"
	"testing"

	"github.com/leef-l/brain/brains/data/provider"
)

func makeTrade(side string, price, size float64, ts int64) provider.Trade {
	return provider.Trade{
		InstID:    "BTC-USDT",
		TradeID:   "t1",
		Price:     price,
		Size:      size,
		Side:      side,
		Timestamp: ts,
	}
}

func TestFlowWindow_AllBuy(t *testing.T) {
	w := NewFlowWindow(300000)
	now := int64(1000000)
	for i := 0; i < 10; i++ {
		w.OnTrade(makeTrade("buy", 100, 1.0, now+int64(i)*1000))
	}
	tox := w.Toxicity()
	if math.Abs(tox-1.0) > 1e-9 {
		t.Fatalf("全 buy 时 Toxicity 应为 1，得到 %f", tox)
	}
	bsr := w.BuySellRatio()
	if math.Abs(bsr-1.0) > 1e-9 {
		t.Fatalf("全 buy 时 BuySellRatio 应为 1，得到 %f", bsr)
	}
}

func TestFlowWindow_Balanced(t *testing.T) {
	w := NewFlowWindow(300000)
	now := int64(1000000)
	for i := 0; i < 10; i++ {
		w.OnTrade(makeTrade("buy", 100, 1.0, now+int64(i)*1000))
		w.OnTrade(makeTrade("sell", 100, 1.0, now+int64(i)*1000+500))
	}
	tox := w.Toxicity()
	if tox > 0.01 {
		t.Fatalf("均衡买卖时 Toxicity 应接近 0，得到 %f", tox)
	}
	bsr := w.BuySellRatio()
	if math.Abs(bsr-0.5) > 0.01 {
		t.Fatalf("均衡买卖时 BuySellRatio 应接近 0.5，得到 %f", bsr)
	}
}

func TestFlowWindow_BigTrade(t *testing.T) {
	w := NewFlowWindow(300000)
	now := int64(1000000)
	// 先喂 10 笔正常成交建立 avgSize
	for i := 0; i < 10; i++ {
		w.OnTrade(makeTrade("buy", 100, 1.0, now+int64(i)*1000))
	}
	// avgSize 约 1.0，大单阈值约 3.0
	// 喂入一笔大单
	w.OnTrade(makeTrade("buy", 100, 5.0, now+11000))

	if w.BigBuyRatio() <= 0 {
		t.Fatal("大单买入后 BigBuyRatio 应大于 0")
	}
	if w.BigSellRatio() != 0 {
		t.Fatal("无大单卖出时 BigSellRatio 应为 0")
	}
}

func TestFlowWindow_Cleanup(t *testing.T) {
	windowMs := int64(5000) // 5 秒窗口
	w := NewFlowWindow(windowMs)
	base := int64(1000000)

	// 第 1 秒写入
	w.OnTrade(makeTrade("buy", 100, 10.0, base))
	// 第 2 秒写入
	w.OnTrade(makeTrade("sell", 100, 5.0, base+1000))

	if w.tradeCount != 2 {
		t.Fatalf("应有 2 笔交易，得到 %d", w.tradeCount)
	}

	// 第 7 秒写入 -> 第 1 秒的数据过期（7000 - 5000 = 2000 > 1000 的 base）
	w.OnTrade(makeTrade("buy", 100, 1.0, base+6000))

	// base 的 trade (ts=1000000) 过期，因为 1000000 < 1006000 - 5000 = 1001000
	if w.tradeCount != 2 {
		t.Fatalf("清理后应有 2 笔交易，得到 %d", w.tradeCount)
	}
}

func TestFlowWindow_TradeDensityRatio(t *testing.T) {
	w := NewFlowWindow(10000) // 10 秒窗口
	now := int64(1000000)
	for i := 0; i < 20; i++ {
		w.OnTrade(makeTrade("buy", 100, 1.0, now+int64(i)*100))
	}
	// 20 笔 / 10 秒 = 2.0
	density := w.TradeDensityRatio()
	if math.Abs(density-2.0) > 0.01 {
		t.Fatalf("TradeDensityRatio 应约为 2.0，得到 %f", density)
	}
}

func TestFlowWindow_Empty(t *testing.T) {
	w := NewFlowWindow(300000)
	// 空窗口不 panic
	if w.Toxicity() != 0 {
		t.Fatal("空窗口 Toxicity 应为 0")
	}
	if w.BuySellRatio() != 0 {
		t.Fatal("空窗口 BuySellRatio 应为 0")
	}
	if w.BigBuyRatio() != 0 {
		t.Fatal("空窗口 BigBuyRatio 应为 0")
	}
	if w.BigSellRatio() != 0 {
		t.Fatal("空窗口 BigSellRatio 应为 0")
	}
	if w.TradeDensityRatio() != 0 {
		t.Fatal("空窗口 TradeDensityRatio 应为 0")
	}
}

func TestTradeFlowTracker(t *testing.T) {
	tracker := NewTradeFlowTracker(300000)
	now := int64(1000000)
	tracker.OnTrade("BTC-USDT", makeTrade("buy", 100, 1.0, now))

	w := tracker.Get("BTC-USDT")
	if w == nil {
		t.Fatal("Get 不应返回 nil")
	}
	if w.tradeCount != 1 {
		t.Fatalf("tradeCount 应为 1，得到 %d", w.tradeCount)
	}
	if tracker.Count() != 1 {
		t.Fatalf("Count 应为 1，得到 %d", tracker.Count())
	}
	if tracker.Get("NONEXIST") != nil {
		t.Fatal("不存在的品种应返回 nil")
	}
}
