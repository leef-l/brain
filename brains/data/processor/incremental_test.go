package processor

import (
	"math"
	"testing"
)

const eps = 1e-6

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < eps
}

// ─────────────────────────────────────────────
// RingSlice 测试
// ─────────────────────────────────────────────

func TestRingSlice_Basic(t *testing.T) {
	r := NewRingSlice(3)
	if r.Len() != 0 {
		t.Fatalf("初始 Len 应为 0，得到 %d", r.Len())
	}

	// 填入 3 个元素（未满）
	for i, v := range []float64{1, 2, 3} {
		evicted, wasEvicted := r.Push(v)
		if wasEvicted {
			t.Fatalf("未满时不应驱逐，第 %d 次 Push", i)
		}
		_ = evicted
	}
	if r.Len() != 3 {
		t.Fatalf("Len 应为 3，得到 %d", r.Len())
	}
	// Get(0) = 最旧 = 1
	if r.Get(0) != 1 || r.Get(1) != 2 || r.Get(2) != 3 {
		t.Fatalf("Get 值错误: %v %v %v", r.Get(0), r.Get(1), r.Get(2))
	}

	// 第 4 次 Push 驱逐最旧的 1
	evicted, wasEvicted := r.Push(4)
	if !wasEvicted || evicted != 1 {
		t.Fatalf("应驱逐 1，得到 wasEvicted=%v evicted=%v", wasEvicted, evicted)
	}
	if r.Get(0) != 2 || r.Get(1) != 3 || r.Get(2) != 4 {
		t.Fatalf("驱逐后 Get 值错误")
	}
}

// ─────────────────────────────────────────────
// EMA 测试
// ─────────────────────────────────────────────

func TestEMA_SeedPhaseNotReady(t *testing.T) {
	ema := NewEMA(10)
	prices := []float64{22, 22.27, 22.19, 22.08, 22.17, 22.18, 22.13, 22.23, 22.43}
	for _, p := range prices {
		ema.Update(p)
		if ema.Ready() {
			t.Fatalf("前 9 个数据 Ready() 应为 false")
		}
		if ema.Value() != 0 {
			t.Fatalf("未就绪时 Value() 应为 0")
		}
	}
}

func TestEMA_SeedSMA(t *testing.T) {
	// 第 10 个数据后 Ready=true，Value = SMA(前 10 个)
	prices := []float64{22, 22.27, 22.19, 22.08, 22.17, 22.18, 22.13, 22.23, 22.43, 22.24}
	ema := NewEMA(10)
	for _, p := range prices {
		ema.Update(p)
	}
	if !ema.Ready() {
		t.Fatal("10 个数据后 Ready() 应为 true")
	}
	// SMA = sum / 10
	var sum float64
	for _, p := range prices {
		sum += p
	}
	expected := sum / 10
	if !almostEqual(ema.Value(), expected) {
		t.Fatalf("EMA 种子 SMA 期望 %.6f，得到 %.6f", expected, ema.Value())
	}
}

func TestEMA_Correctness(t *testing.T) {
	// 用题目给定数据验证
	// [22,22.27,22.19,22.08,22.17,22.18,22.13,22.23,22.43,22.24,22.29,22.15]
	// SMA(10) = sum(前10) / 10 = 221.92 / 10 = 22.192
	// 第 11 个（price=22.29）: EMA = 22.29*(2/11) + 22.192*(9/11) ≈ 22.209818
	// 第 12 个（price=22.15）: EMA = 22.15*(2/11) + prev*(9/11) ≈ 22.198942
	prices := []float64{22, 22.27, 22.19, 22.08, 22.17, 22.18, 22.13, 22.23, 22.43, 22.24, 22.29, 22.15}
	ema := NewEMA(10)

	var lastVal float64
	for i, p := range prices {
		ema.Update(p)
		if i == 9 { // 第 10 个（0-based index 9）
			// SMA(10) = 22.192
			if !almostEqual(ema.Value(), 22.192) {
				t.Fatalf("第 10 个 EMA 期望 22.192000，得到 %.6f", ema.Value())
			}
		}
		if i == 10 { // 第 11 个
			// multiplier = 2/11
			mult := 2.0 / 11.0
			expected11 := 22.29*mult + 22.192*(1-mult)
			if !almostEqual(ema.Value(), expected11) {
				t.Fatalf("第 11 个 EMA 期望 %.6f，得到 %.6f", expected11, ema.Value())
			}
			lastVal = ema.Value()
		}
		if i == 11 { // 第 12 个
			mult := 2.0 / 11.0
			expected12 := 22.15*mult + lastVal*(1-mult)
			if !almostEqual(ema.Value(), expected12) {
				t.Fatalf("第 12 个 EMA 期望 %.6f，得到 %.6f", expected12, ema.Value())
			}
		}
	}
}

// ─────────────────────────────────────────────
// RSI 测试
// ─────────────────────────────────────────────

func TestRSI_NotReadyDuringSeed(t *testing.T) {
	rsi := NewRSI(14)
	// 前 14 个数据（count <= 14）不应就绪
	for i := 0; i < 14; i++ {
		rsi.Update(float64(100 + i))
		if rsi.Ready() {
			t.Fatalf("第 %d 个数据时 Ready() 应为 false（count=%d）", i+1, rsi.count)
		}
	}
}

func TestRSI_ReadyAfterPeriodPlusOne(t *testing.T) {
	rsi := NewRSI(14)
	for i := 0; i < 15; i++ {
		rsi.Update(float64(100 + i))
	}
	if !rsi.Ready() {
		t.Fatal("15 个数据后 Ready() 应为 true")
	}
}

func TestRSI_Range(t *testing.T) {
	rsi := NewRSI(14)
	// 使用 20 个价格点
	prices := []float64{
		44.34, 44.09, 44.15, 43.61, 44.33,
		44.83, 45.10, 45.15, 43.61, 44.33,
		44.83, 45.10, 45.15, 43.61, 44.33,
		44.83, 45.10, 45.15, 43.61, 44.33,
	}
	for _, p := range prices {
		rsi.Update(p)
	}
	if !rsi.Ready() {
		t.Fatal("20 个数据后 RSI 应就绪")
	}
	v := rsi.Value()
	if v < 0 || v > 100 {
		t.Fatalf("RSI 值 %.4f 超出 [0,100] 范围", v)
	}
}

func TestRSI_AllSamePrice(t *testing.T) {
	// 价格不变，涨跌幅均为 0，avgLoss=0，RSI 应为 100
	rsi := NewRSI(14)
	for i := 0; i < 20; i++ {
		rsi.Update(50.0)
	}
	if !almostEqual(rsi.Value(), 100) {
		t.Fatalf("价格不变时 RSI 期望 100，得到 %.6f", rsi.Value())
	}
}

// ─────────────────────────────────────────────
// MACD 测试
// ─────────────────────────────────────────────

func TestMACD_NotReadyBeforeEMA26(t *testing.T) {
	m := NewMACD()
	for i := 0; i < 25; i++ {
		m.Update(float64(100 + i))
		if m.Ready() {
			t.Fatalf("第 %d 个数据时 Ready() 应为 false", i+1)
		}
	}
}

func TestMACD_ReadyAfter26(t *testing.T) {
	m := NewMACD()
	for i := 0; i < 26; i++ {
		m.Update(float64(100 + i))
	}
	if !m.Ready() {
		t.Fatal("26 个数据后 MACD Ready() 应为 true")
	}
}

func TestMACD_HistogramEqualsLineMinusSignal(t *testing.T) {
	m := NewMACD()
	prices := make([]float64, 35)
	for i := range prices {
		prices[i] = 100 + float64(i)*0.5 + math.Sin(float64(i)*0.3)*2
	}
	for _, p := range prices {
		m.Update(p)
	}
	if !m.Ready() {
		t.Fatal("35 个数据后 MACD 应就绪")
	}
	expected := m.Line() - m.Signal()
	if !almostEqual(m.Histogram(), expected) {
		t.Fatalf("Histogram(%.6f) != Line(%.6f) - Signal(%.6f)", m.Histogram(), m.Line(), m.Signal())
	}
}

func TestMACD_LineEqualsEMA12MinusEMA26(t *testing.T) {
	m := NewMACD()
	for i := 0; i < 30; i++ {
		m.Update(float64(100 + i))
	}
	// MACD Line = ema12 - ema26
	expected := m.ema12.Value() - m.ema26.Value()
	if !almostEqual(m.Line(), expected) {
		t.Fatalf("MACD Line 期望 %.6f，得到 %.6f", expected, m.Line())
	}
}

// ─────────────────────────────────────────────
// ATR 测试
// ─────────────────────────────────────────────

func TestATR_NotReadyDuringSeed(t *testing.T) {
	atr := NewATR(14)
	for i := 0; i < 13; i++ {
		h := 100.0 + float64(i)
		l := 99.0 + float64(i)
		atr.UpdateOHLC(h-0.5, h, l, (h+l)/2)
		if atr.Ready() {
			t.Fatalf("第 %d 个数据时 ATR Ready() 应为 false", i+1)
		}
	}
}

func TestATR_ReadyAfterPeriod(t *testing.T) {
	atr := NewATR(14)
	for i := 0; i < 14; i++ {
		h := 100.0 + float64(i)
		l := 99.0 + float64(i)
		atr.UpdateOHLC(h-0.5, h, l, (h+l)/2)
	}
	if !atr.Ready() {
		t.Fatal("14 个数据后 ATR 应就绪")
	}
}

func TestATR_FirstBarNoPreClose(t *testing.T) {
	// 第一根 K 线 TR = H - L，没有 prevClose
	atr := NewATR(1)
	atr.UpdateOHLC(10, 15, 8, 12)
	if !atr.Ready() {
		t.Fatal("period=1 时第一个数据后应就绪")
	}
	// TR = 15 - 8 = 7
	if !almostEqual(atr.Value(), 7.0) {
		t.Fatalf("ATR(1) 期望 7.0，得到 %.6f", atr.Value())
	}
}

func TestATR_WilderSmoothing(t *testing.T) {
	// period=2，验证 Wilder 平滑
	atr := NewATR(2)
	// 第 1 根：TR = 4-2 = 2
	atr.UpdateOHLC(3, 4, 2, 3)
	// 第 2 根：prevClose=3, TR = max(5-1, |5-3|, |1-3|) = max(4,2,2) = 4
	// 初始 ATR = (2+4)/2 = 3
	atr.UpdateOHLC(3, 5, 1, 3)
	if !atr.Ready() {
		t.Fatal("period=2 时第 2 个数据后应就绪")
	}
	if !almostEqual(atr.Value(), 3.0) {
		t.Fatalf("ATR 种子 SMA 期望 3.0，得到 %.6f", atr.Value())
	}
	// 第 3 根：prevClose=3, TR = max(5-2, |5-3|, |2-3|) = max(3,2,1) = 3
	// Wilder: ATR = (3*(2-1) + 3) / 2 = 3
	atr.UpdateOHLC(3, 5, 2, 4)
	if !almostEqual(atr.Value(), 3.0) {
		t.Fatalf("ATR Wilder 平滑期望 3.0，得到 %.6f", atr.Value())
	}
}

// ─────────────────────────────────────────────
// ADX 测试
// ─────────────────────────────────────────────

func TestADX_NotReadyBelow2Period(t *testing.T) {
	adx := NewADX(14)
	for i := 0; i < 27; i++ {
		h := 100.0 + float64(i)*0.3
		l := 99.0 + float64(i)*0.2
		adx.UpdateOHLC(h-0.1, h, l, (h+l)/2)
		if adx.Ready() {
			t.Fatalf("第 %d 个数据时 ADX Ready() 应为 false（需要 28 个）", i+1)
		}
	}
}

func TestADX_ReadyAt2Period(t *testing.T) {
	adx := NewADX(14)
	for i := 0; i < 28; i++ {
		h := 100.0 + float64(i)*0.5
		l := 99.0 + float64(i)*0.3
		adx.UpdateOHLC(h-0.2, h, l, (h+l)/2)
	}
	if !adx.Ready() {
		t.Fatal("28 个数据后 ADX 应就绪")
	}
}

func TestADX_Range(t *testing.T) {
	adx := NewADX(14)
	for i := 0; i < 50; i++ {
		h := 100.0 + float64(i)*0.5 + math.Sin(float64(i)*0.2)*3
		l := h - 1.5 - math.Abs(math.Sin(float64(i)*0.3))
		c := (h + l) / 2
		adx.UpdateOHLC(c-0.1, h, l, c)
	}
	if !adx.Ready() {
		t.Fatal("50 个数据后 ADX 应就绪")
	}
	v := adx.Value()
	if v < 0 || v > 100 {
		t.Fatalf("ADX 值 %.4f 超出 [0,100] 范围", v)
	}
	plusDI := adx.PlusDI()
	minusDI := adx.MinusDI()
	if plusDI < 0 || minusDI < 0 {
		t.Fatalf("+DI(%.4f) 或 -DI(%.4f) 为负", plusDI, minusDI)
	}
}

// ─────────────────────────────────────────────
// BB 测试
// ─────────────────────────────────────────────

func TestBB_NotReadyDuringSeed(t *testing.T) {
	bb := NewBB(20, 2.0)
	for i := 0; i < 19; i++ {
		bb.Update(float64(100 + i))
		if bb.Ready() {
			t.Fatalf("第 %d 个数据时 BB Ready() 应为 false", i+1)
		}
	}
}

func TestBB_ReadyAfterPeriod(t *testing.T) {
	bb := NewBB(20, 2.0)
	for i := 0; i < 20; i++ {
		bb.Update(float64(100 + i))
	}
	if !bb.Ready() {
		t.Fatal("20 个数据后 BB 应就绪")
	}
}

func TestBB_UpperMiddleLowerOrder(t *testing.T) {
	bb := NewBB(20, 2.0)
	for i := 0; i < 30; i++ {
		bb.Update(100.0 + float64(i)*0.5 + math.Sin(float64(i))*2)
	}
	if !bb.Ready() {
		t.Fatal("BB 未就绪")
	}
	upper := bb.Upper()
	middle := bb.Middle()
	lower := bb.Lower()
	if !(upper >= middle && middle >= lower) {
		t.Fatalf("布林带顺序错误: upper=%.4f middle=%.4f lower=%.4f", upper, middle, lower)
	}
}

func TestBB_MiddleEqualsSMA(t *testing.T) {
	bb := NewBB(5, 2.0)
	prices := []float64{10, 11, 12, 13, 14}
	for _, p := range prices {
		bb.Update(p)
	}
	var sum float64
	for _, p := range prices {
		sum += p
	}
	expectedSMA := sum / 5
	if !almostEqual(bb.Middle(), expectedSMA) {
		t.Fatalf("BB Middle 期望 %.6f，得到 %.6f", expectedSMA, bb.Middle())
	}
}

func TestBB_Position_AllSamePrice(t *testing.T) {
	// 所有价格相同时 Upper == Lower，Position 返回 0.5
	bb := NewBB(5, 2.0)
	for i := 0; i < 5; i++ {
		bb.Update(50.0)
	}
	pos := bb.Position(50.0)
	if !almostEqual(pos, 0.5) {
		t.Fatalf("所有价格相同时 Position 期望 0.5，得到 %.6f", pos)
	}
}

func TestBB_Position_Range(t *testing.T) {
	// 准备有波动的数据，Position 应在 [0,1] 附近
	// （极端情况下允许稍微越界，但正常价格应在 [0,1]）
	bb := NewBB(20, 2.0)
	for i := 0; i < 30; i++ {
		bb.Update(100.0 + math.Sin(float64(i)*0.4)*5)
	}
	// 用中轨价格测试，Position 应接近 0.5
	pos := bb.Position(bb.Middle())
	if !almostEqual(pos, 0.5) {
		t.Fatalf("中轨 Position 期望 0.5，得到 %.6f", pos)
	}
	// 用上轨价格，Position 应为 1.0
	posUpper := bb.Position(bb.Upper())
	if !almostEqual(posUpper, 1.0) {
		t.Fatalf("上轨 Position 期望 1.0，得到 %.6f", posUpper)
	}
	// 用下轨价格，Position 应为 0.0
	posLower := bb.Position(bb.Lower())
	if !almostEqual(posLower, 0.0) {
		t.Fatalf("下轨 Position 期望 0.0，得到 %.6f", posLower)
	}
}

func TestBB_SumConsistency(t *testing.T) {
	// 验证滑动窗口下 sum 的一致性：直接求和 vs 增量维护
	bb := NewBB(5, 2.0)
	prices := []float64{10, 20, 30, 40, 50, 60, 70}
	for _, p := range prices {
		bb.Update(p)
	}
	// 最后 5 个价格：30,40,50,60,70
	expectedSum := 30.0 + 40 + 50 + 60 + 70
	if !almostEqual(bb.sum, expectedSum) {
		t.Fatalf("滑动 sum 期望 %.1f，得到 %.6f", expectedSum, bb.sum)
	}
	if !almostEqual(bb.Middle(), expectedSum/5) {
		t.Fatalf("Middle 期望 %.1f，得到 %.6f", expectedSum/5, bb.Middle())
	}
}
