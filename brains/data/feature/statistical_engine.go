package feature

import (
	"context"
	"math"
	"sync"
)

// StatisticalMLEngine 使用纯统计方法实现 MLEngine 接口。
// 内部维护在线学习状态，通过 Update 方法接收 ruleFeatures 并
// 更新内部统计量，Predict 时基于最新状态做推理。
//
// 核心方法：
//   - MarketRegime: 隐马尔可夫思想 + 贝叶斯更新的 4 状态分类
//   - VolPredict:   EWMA 短期/长期波动率跟踪
//   - AnomalyScore: 滑动窗口 Z-score 异常检测
//
// 需至少 50 次 Update 才 Ready()=true（冷启动保护）。
type StatisticalMLEngine struct {
	mu sync.RWMutex

	// 更新计数
	updateCount int

	// --- MarketRegime 状态 ---
	// 4 状态概率分布: trend, range, breakout, panic
	regimeProb [4]float64
	// 4x4 状态转移矩阵 transitionMatrix[from][to]
	transitionMatrix [4][4]float64

	// --- VolPredict 状态 ---
	// EWMA 短期(5)和长期(20)波动率
	ewmaShort float64 // α = 2/(5+1)
	ewmaLong  float64 // α = 2/(20+1)
	// 波动率百分位滑动窗口
	volWindow    []float64
	volWindowMax int
	volWindowIdx int
	volWindowFull bool

	// --- AnomalyScore 状态 ---
	// 滑动均值和方差（Welford 在线算法）
	anomalyN     int
	anomalyMean  []float64 // 每个聚合特征的均值
	anomalyM2    []float64 // 每个聚合特征的 M2（方差×N）

	// 上一次 ruleFeatures 的聚合统计量（供 Predict 使用）
	lastAggStats aggStats
}

// aggStats 是从 ruleFeatures 提取的聚合统计量。
// 不硬编码特征索引，而是计算通用统计信息。
type aggStats struct {
	mean     float64 // 全向量均值
	variance float64 // 全向量方差
	maxVal   float64 // 最大值
	minVal   float64 // 最小值
	absSum   float64 // 绝对值之和
	range_   float64 // maxVal - minVal
	energy   float64 // 平方和
	skew     float64 // 偏度近似
}

const (
	// 冷启动保护：至少需要这么多次 Update 才 Ready
	minUpdatesForReady = 50

	// EWMA 衰减因子
	ewmaAlphaShort = 2.0 / (5.0 + 1.0)  // 短期 5 周期
	ewmaAlphaLong  = 2.0 / (20.0 + 1.0) // 长期 20 周期

	// 波动率百分位窗口大小
	volWindowSize = 100

	// 聚合统计量数量（用于异常检测）
	numAggFeatures = 6

	// Z-score 异常阈值
	anomalyZStart = 2.0 // 开始计分
	anomalyZFull  = 3.0 // 满分 1.0
)

// NewStatisticalMLEngine 创建并初始化 StatisticalMLEngine。
func NewStatisticalMLEngine() *StatisticalMLEngine {
	e := &StatisticalMLEngine{
		volWindowMax: volWindowSize,
		volWindow:    make([]float64, volWindowSize),
		anomalyMean:  make([]float64, numAggFeatures),
		anomalyM2:    make([]float64, numAggFeatures),
	}

	// 初始化均匀状态概率分布
	e.regimeProb = [4]float64{0.25, 0.25, 0.25, 0.25}

	// 初始化状态转移矩阵：高自转移概率 + 低跳转概率
	// 状态: 0=trend, 1=range, 2=breakout, 3=panic
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			if i == j {
				e.transitionMatrix[i][j] = 0.7
			} else {
				e.transitionMatrix[i][j] = 0.1
			}
		}
	}

	return e
}

// extractAggStats 从 ruleFeatures 提取聚合统计量，不硬编码特征索引。
func extractAggStats(features []float64) aggStats {
	n := len(features)
	if n == 0 {
		return aggStats{}
	}

	var s aggStats
	s.maxVal = math.Inf(-1)
	s.minVal = math.Inf(1)

	sum := 0.0
	sqSum := 0.0
	absSum := 0.0
	cubeSum := 0.0

	for _, v := range features {
		sum += v
		sqSum += v * v
		absSum += math.Abs(v)
		cubeSum += v * v * v
		if v > s.maxVal {
			s.maxVal = v
		}
		if v < s.minVal {
			s.minVal = v
		}
	}

	fn := float64(n)
	s.mean = sum / fn
	s.variance = sqSum/fn - s.mean*s.mean
	if s.variance < 0 {
		s.variance = 0 // 浮点误差保护
	}
	s.absSum = absSum
	s.range_ = s.maxVal - s.minVal
	s.energy = sqSum

	// 偏度近似
	std := math.Sqrt(s.variance)
	if std > 1e-10 {
		s.skew = (cubeSum/fn - 3*s.mean*s.variance - s.mean*s.mean*s.mean) / (std * std * std)
	}

	return s
}

// aggStatsToSlice 将聚合统计量转为 slice，用于异常检测。
func aggStatsToSlice(s aggStats) []float64 {
	return []float64{
		s.mean,
		s.variance,
		s.range_,
		s.absSum,
		s.energy,
		s.skew,
	}
}

// Update 在线学习：用新的 ruleFeatures 更新内部状态。
func (e *StatisticalMLEngine) Update(ruleFeatures []float64) {
	if len(ruleFeatures) == 0 {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.updateCount++
	stats := extractAggStats(ruleFeatures)
	e.lastAggStats = stats

	e.updateRegime(stats)
	e.updateVolatility(stats)
	e.updateAnomaly(stats)
}

// updateRegime 用贝叶斯更新 4 状态概率分布。
// 核心思想：根据观测特征计算各状态的似然，乘以先验（当前概率 × 转移概率）后归一化。
func (e *StatisticalMLEngine) updateRegime(stats aggStats) {
	// 计算各状态的观测似然 (emission probability)
	var likelihood [4]float64

	std := math.Sqrt(stats.variance)
	normalizedRange := 0.0
	if stats.mean != 0 {
		normalizedRange = stats.range_ / math.Abs(stats.mean)
	}

	// Trend: 高方差 + 非零均值偏移 + 正偏度绝对值大
	trendScore := std*math.Abs(stats.mean)/(math.Abs(stats.mean)+1) + math.Abs(stats.skew)*0.1
	likelihood[0] = sigmoid(trendScore - 0.3)

	// Range: 低方差 + 小范围
	rangeScore := 1.0 / (1.0 + normalizedRange*10)
	likelihood[1] = sigmoid(rangeScore - 0.3)

	// Breakout: 大范围 + 高能量
	breakoutScore := normalizedRange + stats.energy/(stats.energy+1)*0.5
	likelihood[2] = sigmoid(breakoutScore - 0.7)

	// Panic: 极端偏度 + 高方差 + 大范围
	panicScore := math.Abs(stats.skew)*0.3 + normalizedRange*0.5 + std/(std+1)*0.2
	likelihood[3] = sigmoid(panicScore - 0.8)

	// 贝叶斯更新：先验 = 转移概率 × 当前分布
	var prior [4]float64
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			prior[j] += e.regimeProb[i] * e.transitionMatrix[i][j]
		}
	}

	// 后验 = prior × likelihood
	for i := 0; i < 4; i++ {
		e.regimeProb[i] = prior[i] * likelihood[i]
	}

	// 归一化
	normalizeProb(e.regimeProb[:])
}

// updateVolatility 更新 EWMA 波动率跟踪和滑动窗口。
func (e *StatisticalMLEngine) updateVolatility(stats aggStats) {
	currentVol := math.Sqrt(stats.variance)

	if e.updateCount == 1 {
		// 首次：直接赋值
		e.ewmaShort = currentVol
		e.ewmaLong = currentVol
	} else {
		// EWMA 更新
		e.ewmaShort = ewmaAlphaShort*currentVol + (1-ewmaAlphaShort)*e.ewmaShort
		e.ewmaLong = ewmaAlphaLong*currentVol + (1-ewmaAlphaLong)*e.ewmaLong
	}

	// 滑动窗口记录波动率
	e.volWindow[e.volWindowIdx] = currentVol
	e.volWindowIdx = (e.volWindowIdx + 1) % e.volWindowMax
	if e.updateCount >= e.volWindowMax {
		e.volWindowFull = true
	}
}

// updateAnomaly 用 Welford 在线算法更新均值和方差。
func (e *StatisticalMLEngine) updateAnomaly(stats aggStats) {
	values := aggStatsToSlice(stats)
	e.anomalyN++
	n := float64(e.anomalyN)

	for i := 0; i < numAggFeatures && i < len(values); i++ {
		delta := values[i] - e.anomalyMean[i]
		e.anomalyMean[i] += delta / n
		delta2 := values[i] - e.anomalyMean[i]
		e.anomalyM2[i] += delta * delta2
	}
}

// Predict 基于当前内部状态做推理，返回 MLFeatures。
func (e *StatisticalMLEngine) Predict(_ context.Context, ruleFeatures []float64) (MLFeatures, error) {
	// 先用最新数据做一次 Update
	e.Update(ruleFeatures)

	e.mu.RLock()
	defer e.mu.RUnlock()

	var ml MLFeatures

	e.predictRegime(&ml)
	e.predictVolatility(&ml)
	e.predictAnomaly(&ml)

	return ml, nil
}

// predictRegime 填充 MarketRegime 维度。
func (e *StatisticalMLEngine) predictRegime(ml *MLFeatures) {
	ml.MarketRegime = e.regimeProb
}

// predictVolatility 填充 VolPredict 维度。
func (e *StatisticalMLEngine) predictVolatility(ml *MLFeatures) {
	// vol1H: 短期 EWMA 波动率
	ml.VolPredict[0] = e.ewmaShort

	// vol4H: 长期 EWMA 波动率
	ml.VolPredict[1] = e.ewmaLong

	// volPercentile: 当前波动率在滑动窗口中的排名
	ml.VolPredict[2] = e.computeVolPercentile()

	// volDirection: 短期/长期 - 1
	if e.ewmaLong > 1e-10 {
		ml.VolPredict[3] = e.ewmaShort/e.ewmaLong - 1
	}
}

// computeVolPercentile 计算当前波动率在滑动窗口中的百分位。
func (e *StatisticalMLEngine) computeVolPercentile() float64 {
	windowLen := e.volWindowMax
	if !e.volWindowFull {
		windowLen = e.volWindowIdx
	}
	if windowLen <= 1 {
		return 0.5
	}

	currentVol := e.ewmaShort
	lessCount := 0
	for i := 0; i < windowLen; i++ {
		if e.volWindow[i] < currentVol {
			lessCount++
		}
	}

	return float64(lessCount) / float64(windowLen)
}

// predictAnomaly 填充 AnomalyScore 维度。
func (e *StatisticalMLEngine) predictAnomaly(ml *MLFeatures) {
	if e.anomalyN < 2 {
		return
	}

	values := aggStatsToSlice(e.lastAggStats)
	n := float64(e.anomalyN)

	// 分别计算 price(mean)、volume(absSum)、orderbook(range)、combined 的异常分
	featureMap := [3]int{0, 3, 2} // mean → price, absSum → volume, range → orderbook
	for idx, fi := range featureMap {
		if fi >= numAggFeatures || fi >= len(values) {
			continue
		}
		variance := e.anomalyM2[fi] / (n - 1)
		std := math.Sqrt(math.Max(variance, 0))
		if std < 1e-10 {
			continue
		}
		z := math.Abs(values[fi]-e.anomalyMean[fi]) / std
		ml.AnomalyScore[idx] = zScoreToAnomaly(z)
	}

	// Combined: 取最大值
	ml.AnomalyScore[3] = math.Max(ml.AnomalyScore[0],
		math.Max(ml.AnomalyScore[1], ml.AnomalyScore[2]))
}

// Ready 报告引擎是否准备好。至少需要 50 次 Update。
func (e *StatisticalMLEngine) Ready() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.updateCount >= minUpdatesForReady
}

// Name 返回引擎标识符。
func (e *StatisticalMLEngine) Name() string {
	return "statistical"
}

// --- 辅助函数 ---

// sigmoid 将值映射到 (0, 1) 区间。
func sigmoid(x float64) float64 {
	return 1.0 / (1.0 + math.Exp(-x*5))
}

// zScoreToAnomaly 将 Z-score 转换为 [0, 1] 异常分。
// 2σ 开始计分，3σ 为 1.0。
func zScoreToAnomaly(z float64) float64 {
	if z <= anomalyZStart {
		return 0
	}
	if z >= anomalyZFull {
		return 1.0
	}
	return (z - anomalyZStart) / (anomalyZFull - anomalyZStart)
}

// 编译时接口检查
var _ MLEngine = (*StatisticalMLEngine)(nil)
