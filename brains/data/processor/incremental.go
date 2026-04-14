package processor

import "math"

// ─────────────────────────────────────────────
// RingSlice — 固定容量环形切片
// ─────────────────────────────────────────────

// RingSlice 是一个固定容量的环形缓冲区，Push 时若已满则驱逐最旧元素。
type RingSlice struct {
	data  []float64
	head  int // 指向最旧元素的位置
	count int
	cap   int
}

// NewRingSlice 创建容量为 capacity 的环形切片。
func NewRingSlice(capacity int) *RingSlice {
	return &RingSlice{
		data: make([]float64, capacity),
		cap:  capacity,
	}
}

// Push 向环形切片中写入新值。
// 若已满，返回被驱逐的旧值及 wasEvicted=true；否则返回 0, false。
func (r *RingSlice) Push(v float64) (evicted float64, wasEvicted bool) {
	if r.count == r.cap {
		// 驱逐最旧元素（head 位置）
		evicted = r.data[r.head]
		wasEvicted = true
		r.data[r.head] = v
		r.head = (r.head + 1) % r.cap
	} else {
		// 写入位置 = (head + count) % cap
		pos := (r.head + r.count) % r.cap
		r.data[pos] = v
		r.count++
	}
	return
}

// Len 返回当前元素数量。
func (r *RingSlice) Len() int { return r.count }

// Get 按逻辑索引（0 = 最旧）取值。
func (r *RingSlice) Get(i int) float64 {
	return r.data[(r.head+i)%r.cap]
}

// ─────────────────────────────────────────────
// 1. IncrementalEMA — 指数移动平均
// ─────────────────────────────────────────────

// IncrementalEMA 以增量方式计算指数移动平均。
// 前 period 个数据用 SMA 作为种子，之后使用标准 EMA 公式。
type IncrementalEMA struct {
	period     int
	multiplier float64 // 2 / (period + 1)
	value      float64
	count      int
	sum        float64 // 用于 SMA 种子阶段的累加
}

// NewEMA 创建周期为 period 的 EMA 计算器。
func NewEMA(period int) *IncrementalEMA {
	return &IncrementalEMA{
		period:     period,
		multiplier: 2.0 / float64(period+1),
	}
}

// Update 输入新价格，更新 EMA。
func (e *IncrementalEMA) Update(price float64) {
	e.count++
	if e.count <= e.period {
		// 种子阶段：累加求 SMA
		e.sum += price
		if e.count == e.period {
			e.value = e.sum / float64(e.period)
		}
	} else {
		// EMA 阶段
		e.value = price*e.multiplier + e.value*(1-e.multiplier)
	}
}

// Value 返回当前 EMA 值。Ready() 为 false 时返回 0。
func (e *IncrementalEMA) Value() float64 {
	if !e.Ready() {
		return 0
	}
	return e.value
}

// Ready 在已收集至少 period 个数据后返回 true。
func (e *IncrementalEMA) Ready() bool { return e.count >= e.period }

// ─────────────────────────────────────────────
// 2. IncrementalRSI — 相对强弱指数
// ─────────────────────────────────────────────

// IncrementalRSI 以增量方式计算 RSI（0–100）。
// 前 period+1 个数据用于建立初始 avgGain / avgLoss，之后使用 Wilder 平滑。
type IncrementalRSI struct {
	period    int
	avgGain   float64
	avgLoss   float64
	prevPrice float64
	count     int

	// 种子阶段累加
	seedGainSum float64
	seedLossSum float64
}

// NewRSI 创建周期为 period 的 RSI 计算器（通常 period=14）。
func NewRSI(period int) *IncrementalRSI {
	return &IncrementalRSI{period: period}
}

// Update 输入新价格，更新 RSI。
func (r *IncrementalRSI) Update(price float64) {
	r.count++
	if r.count == 1 {
		// 第一个价格只记录，不产生涨跌幅
		r.prevPrice = price
		return
	}

	change := price - r.prevPrice
	gain := math.Max(change, 0)
	loss := math.Max(-change, 0)
	r.prevPrice = price

	if r.count <= r.period+1 {
		// 种子阶段：累加涨跌幅（count=2 ~ period+1，共 period 个差值）
		r.seedGainSum += gain
		r.seedLossSum += loss
		if r.count == r.period+1 {
			// 建立初始平均
			r.avgGain = r.seedGainSum / float64(r.period)
			r.avgLoss = r.seedLossSum / float64(r.period)
		}
	} else {
		// Wilder 平滑
		r.avgGain = (r.avgGain*float64(r.period-1) + gain) / float64(r.period)
		r.avgLoss = (r.avgLoss*float64(r.period-1) + loss) / float64(r.period)
	}
}

// Value 返回当前 RSI（0–100）。Ready() 为 false 时返回 0。
func (r *IncrementalRSI) Value() float64 {
	if !r.Ready() {
		return 0
	}
	if r.avgLoss == 0 {
		return 100
	}
	rs := r.avgGain / r.avgLoss
	return 100 - 100/(1+rs)
}

// Ready 在已收集超过 period 个数据后返回 true。
func (r *IncrementalRSI) Ready() bool { return r.count > r.period }

// ─────────────────────────────────────────────
// 3. IncrementalMACD — 移动平均收敛/发散
// ─────────────────────────────────────────────

// IncrementalMACD 固定使用 12/26/9 参数。
// MACD Line  = EMA(12) - EMA(26)
// Signal     = EMA(9) of MACD Line
// Histogram  = MACD Line - Signal
type IncrementalMACD struct {
	ema12  *IncrementalEMA
	ema26  *IncrementalEMA
	signal *IncrementalEMA // 9 周期 EMA of MACD Line
	count  int
}

// NewMACD 创建固定参数 12/26/9 的 MACD 计算器。
func NewMACD() *IncrementalMACD {
	return &IncrementalMACD{
		ema12:  NewEMA(12),
		ema26:  NewEMA(26),
		signal: NewEMA(9),
	}
}

// Update 输入新价格，更新 MACD 各分量。
func (m *IncrementalMACD) Update(price float64) {
	m.count++
	m.ema12.Update(price)
	m.ema26.Update(price)
	// 只有 ema26 就绪后，才向 signal 喂 MACD Line
	if m.ema26.Ready() {
		m.signal.Update(m.Line())
	}
}

// Line 返回 MACD 线（EMA12 - EMA26）。
func (m *IncrementalMACD) Line() float64 {
	if !m.ema26.Ready() {
		return 0
	}
	return m.ema12.Value() - m.ema26.Value()
}

// Signal 返回信号线（MACD Line 的 9 周期 EMA）。
func (m *IncrementalMACD) Signal() float64 {
	if !m.signal.Ready() {
		return 0
	}
	return m.signal.Value()
}

// Histogram 返回柱状图（Line - Signal）。
func (m *IncrementalMACD) Histogram() float64 {
	return m.Line() - m.Signal()
}

// Ready 在 EMA(26) 就绪后返回 true。
func (m *IncrementalMACD) Ready() bool { return m.ema26.Ready() }

// ─────────────────────────────────────────────
// 4. IncrementalATR — 平均真实波幅
// ─────────────────────────────────────────────

// IncrementalATR 以增量方式计算 ATR（平均真实波幅）。
// 前 period 个 TR 用 SMA 做种子，之后使用 Wilder 平滑。
type IncrementalATR struct {
	period    int
	value     float64
	prevClose float64
	count     int
	sum       float64 // 种子阶段 TR 累加
}

// NewATR 创建周期为 period 的 ATR 计算器（通常 period=14）。
func NewATR(period int) *IncrementalATR {
	return &IncrementalATR{period: period}
}

// UpdateOHLC 输入新 OHLC 数据，更新 ATR。
func (a *IncrementalATR) UpdateOHLC(o, h, l, c float64) {
	_ = o // open 不参与 ATR 计算
	a.count++

	var tr float64
	if a.count == 1 {
		// 第一根 K 线没有 prevClose，TR = H - L
		tr = h - l
	} else {
		tr = math.Max(h-l, math.Max(math.Abs(h-a.prevClose), math.Abs(l-a.prevClose)))
	}
	a.prevClose = c

	if a.count <= a.period {
		// 种子阶段：累加 TR
		a.sum += tr
		if a.count == a.period {
			a.value = a.sum / float64(a.period)
		}
	} else {
		// Wilder 平滑
		a.value = (a.value*float64(a.period-1) + tr) / float64(a.period)
	}
}

// Value 返回当前 ATR。Ready() 为 false 时返回 0。
func (a *IncrementalATR) Value() float64 {
	if !a.Ready() {
		return 0
	}
	return a.value
}

// Ready 在已收集至少 period 个数据后返回 true。
func (a *IncrementalATR) Ready() bool { return a.count >= a.period }

// ─────────────────────────────────────────────
// 5. IncrementalADX — 平均方向性指数
// ─────────────────────────────────────────────

// IncrementalADX 以增量方式计算 ADX、+DI、-DI。
// Ready 条件: count >= 2*period（前 period 根建立平滑 DM/TR，后 period 个 DX 建立 ADX 种子）。
type IncrementalADX struct {
	period int
	plusDI float64
	minusDI float64
	adx    float64

	prevHigh  float64
	prevLow   float64
	prevClose float64
	count     int

	// Wilder 平滑后的方向运动和真实波幅
	smoothPlusDM  float64
	smoothMinusDM float64
	smoothTR      float64

	// ADX 种子阶段：前 period 个 DX 累加
	dxSum      float64
	dxCount    int
	adxReady   bool
}

// NewADX 创建周期为 period 的 ADX 计算器（通常 period=14）。
func NewADX(period int) *IncrementalADX {
	return &IncrementalADX{period: period}
}

// UpdateOHLC 输入新 OHLC 数据，更新 ADX。
func (a *IncrementalADX) UpdateOHLC(o, h, l, c float64) {
	_ = o
	a.count++

	if a.count == 1 {
		// 第一根 K 线：只记录高低收，不产生方向运动
		a.prevHigh = h
		a.prevLow = l
		a.prevClose = c
		return
	}

	// 计算 +DM 和 -DM
	upMove := h - a.prevHigh
	downMove := a.prevLow - l

	var plusDM, minusDM float64
	if upMove > downMove && upMove > 0 {
		plusDM = upMove
	}
	if downMove > upMove && downMove > 0 {
		minusDM = downMove
	}

	// 计算 TR
	tr := math.Max(h-l, math.Max(math.Abs(h-a.prevClose), math.Abs(l-a.prevClose)))

	a.prevHigh = h
	a.prevLow = l
	a.prevClose = c

	// Wilder 平滑：smooth = prev - prev/period + current
	if a.count == 2 {
		// 初始化平滑值（第一个差值）
		a.smoothPlusDM = plusDM
		a.smoothMinusDM = minusDM
		a.smoothTR = tr
	} else {
		a.smoothPlusDM = a.smoothPlusDM - a.smoothPlusDM/float64(a.period) + plusDM
		a.smoothMinusDM = a.smoothMinusDM - a.smoothMinusDM/float64(a.period) + minusDM
		a.smoothTR = a.smoothTR - a.smoothTR/float64(a.period) + tr
	}

	// 计算 +DI 和 -DI（需要至少 period 个差值，即 count >= period+1）
	if a.count < a.period+1 {
		return
	}

	if a.smoothTR == 0 {
		a.plusDI = 0
		a.minusDI = 0
		return
	}
	a.plusDI = 100 * a.smoothPlusDM / a.smoothTR
	a.minusDI = 100 * a.smoothMinusDM / a.smoothTR

	// 计算 DX
	diSum := a.plusDI + a.minusDI
	var dx float64
	if diSum != 0 {
		dx = 100 * math.Abs(a.plusDI-a.minusDI) / diSum
	}

	// ADX 种子阶段：积累 period 个 DX
	if !a.adxReady {
		a.dxSum += dx
		a.dxCount++
		if a.dxCount == a.period {
			a.adx = a.dxSum / float64(a.period)
			a.adxReady = true
		}
	} else {
		// Wilder 平滑 ADX
		a.adx = (a.adx*float64(a.period-1) + dx) / float64(a.period)
	}
}

// Value 返回当前 ADX（0–100）。
func (a *IncrementalADX) Value() float64 { return a.adx }

// PlusDI 返回 +DI。
func (a *IncrementalADX) PlusDI() float64 { return a.plusDI }

// MinusDI 返回 -DI。
func (a *IncrementalADX) MinusDI() float64 { return a.minusDI }

// Ready 在 count >= 2*period 时返回 true。
func (a *IncrementalADX) Ready() bool { return a.count >= 2*a.period }

// ─────────────────────────────────────────────
// 6. IncrementalBB — 布林带
// ─────────────────────────────────────────────

// IncrementalBB 以增量方式计算布林带（上轨、中轨、下轨）。
// 维护 sum 和 sumSq 避免每次重新遍历。
type IncrementalBB struct {
	period int
	mult   float64    // 标准差倍数，通常 2.0
	prices *RingSlice // 保留最近 period 个价格
	sum    float64
	sumSq  float64
	count  int
}

// NewBB 创建周期为 period、标准差倍数为 mult 的布林带计算器（通常 20, 2.0）。
func NewBB(period int, mult float64) *IncrementalBB {
	return &IncrementalBB{
		period: period,
		mult:   mult,
		prices: NewRingSlice(period),
	}
}

// Update 输入新价格，更新布林带。
func (b *IncrementalBB) Update(price float64) {
	evicted, wasEvicted := b.prices.Push(price)
	if wasEvicted {
		// 减去被驱逐的旧值
		b.sum -= evicted
		b.sumSq -= evicted * evicted
	} else {
		b.count++
	}
	b.sum += price
	b.sumSq += price * price
}

func (b *IncrementalBB) sma() float64 {
	n := b.prices.Len()
	if n == 0 {
		return 0
	}
	return b.sum / float64(n)
}

func (b *IncrementalBB) stddev() float64 {
	n := b.prices.Len()
	if n == 0 {
		return 0
	}
	mean := b.sum / float64(n)
	variance := b.sumSq/float64(n) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}

// Upper 返回上轨（SMA + mult * StdDev）。
func (b *IncrementalBB) Upper() float64 {
	return b.sma() + b.mult*b.stddev()
}

// Middle 返回中轨（SMA）。
func (b *IncrementalBB) Middle() float64 { return b.sma() }

// Lower 返回下轨（SMA - mult * StdDev）。
func (b *IncrementalBB) Lower() float64 {
	return b.sma() - b.mult*b.stddev()
}

// Position 返回价格在布林带中的相对位置（0–1）。
// 当上下轨重合时返回 0.5。
func (b *IncrementalBB) Position(price float64) float64 {
	upper := b.Upper()
	lower := b.Lower()
	if upper == lower {
		return 0.5
	}
	return (price - lower) / (upper - lower)
}

// Ready 在已收集至少 period 个数据后返回 true。
func (b *IncrementalBB) Ready() bool { return b.count >= b.period }
