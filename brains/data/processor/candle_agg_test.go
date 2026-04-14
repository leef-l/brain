package processor

import (
	"math"
	"testing"

	"github.com/leef-l/brain/brains/data/provider"
)

func makeCandle(ts int64, open, high, low, close, vol float64) provider.Candle {
	return provider.Candle{
		InstID:    "BTC-USDT",
		Bar:       "1m",
		Timestamp: ts,
		Open:      open,
		High:      high,
		Low:       low,
		Close:     close,
		Volume:    vol,
	}
}

func TestNewCandleWindow(t *testing.T) {
	w := NewCandleWindow("BTC-USDT", "1m")
	if w.InstID != "BTC-USDT" || w.Timeframe != "1m" {
		t.Fatal("InstID 或 Timeframe 不正确")
	}
	if w.EMA9 == nil || w.EMA21 == nil || w.EMA55 == nil {
		t.Fatal("EMA 指标未初始化")
	}
	if w.RSI14 == nil || w.ATR14 == nil || w.MACD == nil {
		t.Fatal("RSI/ATR/MACD 指标未初始化")
	}
	if w.ADX14 == nil || w.BB20 == nil {
		t.Fatal("ADX/BB 指标未初始化")
	}
	if w.History == nil {
		t.Fatal("History 未初始化")
	}
}

func TestOnCandle_IndicatorsNonZero(t *testing.T) {
	w := NewCandleWindow("BTC-USDT", "1m")
	// 喂入 20 根 K 线，价格从 100 到 119
	for i := 0; i < 20; i++ {
		price := 100.0 + float64(i)
		c := makeCandle(int64(i+1)*60000, price, price+2, price-1, price, 100)
		w.OnCandle(c)
	}

	// EMA9 需要 9 根数据就 ready
	if !w.EMA9.Ready() {
		t.Fatal("EMA9 应该 Ready")
	}
	if w.EMA9.Value() == 0 {
		t.Fatal("EMA9 值不应为 0")
	}

	// RSI14 需要 > 14 根
	if !w.RSI14.Ready() {
		t.Fatal("RSI14 应该 Ready（20 根数据）")
	}
	if w.RSI14.Value() == 0 {
		t.Fatal("RSI14 值不应为 0")
	}

	// MACD 需要 EMA26 ready，20 根不够，不 ready 是正常的
	// 但 EMA12 应该 ready
	if !w.MACD.ema12.Ready() {
		t.Fatal("MACD.ema12 应该 Ready")
	}
}

func TestLoadHistory_AllReady(t *testing.T) {
	w := NewCandleWindow("BTC-USDT", "1m")
	candles := make([]provider.Candle, 500)
	for i := 0; i < 500; i++ {
		price := 100.0 + float64(i)*0.1
		candles[i] = makeCandle(int64(i+1)*60000, price, price+2, price-1, price, 100)
	}
	w.LoadHistory(candles)

	if !w.EMA9.Ready() {
		t.Fatal("EMA9 未 Ready")
	}
	if !w.EMA21.Ready() {
		t.Fatal("EMA21 未 Ready")
	}
	if !w.EMA55.Ready() {
		t.Fatal("EMA55 未 Ready")
	}
	if !w.RSI14.Ready() {
		t.Fatal("RSI14 未 Ready")
	}
	if !w.ATR14.Ready() {
		t.Fatal("ATR14 未 Ready")
	}
	if !w.MACD.Ready() {
		t.Fatal("MACD 未 Ready")
	}
	if !w.ADX14.Ready() {
		t.Fatal("ADX14 未 Ready")
	}
	if !w.BB20.Ready() {
		t.Fatal("BB20 未 Ready")
	}
}

func TestPriceChangeRate(t *testing.T) {
	w := NewCandleWindow("BTC-USDT", "1m")
	// 喂入 5 根 K 线: Close = 100, 110, 120, 130, 140
	for i := 0; i < 5; i++ {
		price := 100.0 + float64(i)*10
		w.OnCandle(makeCandle(int64(i+1)*60000, price, price+5, price-5, price, 100))
	}
	// History 里有前 4 根的 Close: 100, 110, 120, 130
	// Current.Close = 140
	// PriceChangeRate(4) = (140 - 100) / 100 = 0.4
	rate := w.PriceChangeRate(4)
	if math.Abs(rate-0.4) > 1e-9 {
		t.Fatalf("PriceChangeRate(4) 应为 0.4，得到 %f", rate)
	}

	// PriceChangeRate(2) = (140 - 120) / 120 = 0.1666...
	rate2 := w.PriceChangeRate(2)
	expected := (140.0 - 120.0) / 120.0
	if math.Abs(rate2-expected) > 1e-9 {
		t.Fatalf("PriceChangeRate(2) 应为 %f，得到 %f", expected, rate2)
	}

	// n 超过 History 长度
	if w.PriceChangeRate(10) != 0 {
		t.Fatal("History 不够时应返回 0")
	}
}

func TestVolatility(t *testing.T) {
	w := NewCandleWindow("BTC-USDT", "1m")
	// 喂入 6 根 K 线: Close = 100, 100, 100, 100, 100, 100
	// 全部相同 -> 标准差 = 0 -> Volatility = 0
	for i := 0; i < 6; i++ {
		w.OnCandle(makeCandle(int64(i+1)*60000, 100, 105, 95, 100, 100))
	}
	v := w.Volatility(5)
	if v != 0 {
		t.Fatalf("相同价格 Volatility 应为 0，得到 %f", v)
	}

	// 喂入不同价格
	w2 := NewCandleWindow("ETH-USDT", "1m")
	prices := []float64{100, 110, 90, 120, 80, 100}
	for i, p := range prices {
		w2.OnCandle(makeCandle(int64(i+1)*60000, p, p+5, p-5, p, 100))
	}
	// History: 100, 110, 90, 120, 80 (5 根), Current.Close = 100
	vol := w2.Volatility(5)
	if vol <= 0 {
		t.Fatal("不同价格 Volatility 应大于 0")
	}
}

func TestCandleAggregator_GetWindow(t *testing.T) {
	a := NewCandleAggregator()
	w1 := a.GetWindow("BTC-USDT", "1m")
	if w1 == nil {
		t.Fatal("GetWindow 不应返回 nil")
	}
	w2 := a.GetWindow("BTC-USDT", "1m")
	if w1 != w2 {
		t.Fatal("同 key 应返回同一个窗口")
	}
	if a.WindowCount() != 1 {
		t.Fatalf("WindowCount 应为 1，得到 %d", a.WindowCount())
	}

	// 不同 key
	a.GetWindow("ETH-USDT", "5m")
	if a.WindowCount() != 2 {
		t.Fatalf("WindowCount 应为 2，得到 %d", a.WindowCount())
	}
}

func TestCandleAggregator_OnCandle(t *testing.T) {
	a := NewCandleAggregator()
	c := makeCandle(60000, 100, 105, 95, 102, 1000)
	a.OnCandle("BTC-USDT", "1m", c)

	w := a.GetWindow("BTC-USDT", "1m")
	if w.Current.Close != 102 {
		t.Fatalf("OnCandle 转发后 Current.Close 应为 102，得到 %f", w.Current.Close)
	}
}
