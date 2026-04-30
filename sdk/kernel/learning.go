// learning.go — 自适应学习引擎 L1-L3 骨架
//
// L1: 协作级学习 — Brain 能力画像 (EWMA + Wilson 置信度)
// L2: 流程级学习 — 任务序列排序/组合优化
// L3: 偏好级学习 — 用户偏好建模
package kernel

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/persistence"
)

// ---------------------------------------------------------------------------
// L1: 协作级学习 — Brain 能力画像
// ---------------------------------------------------------------------------

// EWMAScore 指数加权移动平均评分
type EWMAScore struct {
	Value   float64   // 当前 EWMA 值 [0, 1]
	Alpha   float64   // 衰减系数 0.1~0.3
	Updated time.Time // 最后更新时间
}

// Update 更新 EWMA 值: V_new = α * newVal + (1-α) * V_old
func (e *EWMAScore) Update(newVal float64) {
	if e.Alpha <= 0 || e.Alpha > 1 {
		e.Alpha = 0.2 // 默认衰减系数
	}
	e.Value = e.Alpha*newVal + (1-e.Alpha)*e.Value
	e.Updated = time.Now()
}

// TaskTypeScore 某 brain 对某类任务的综合评分
type TaskTypeScore struct {
	TaskType    string
	SampleCount int
	Accuracy    EWMAScore
	Speed       EWMAScore
	Cost        EWMAScore
	Stability   EWMAScore
	// LatencyMs 记录该 brain 执行该类任务的平均延迟（毫秒，EWMA）。
	// 用于自适应超时估算：EstimateTimeout 基于此计算建议超时。
	LatencyMs EWMAScore
}

// BrainCapabilityProfile 是对某个 brain 的能力认知模型
type BrainCapabilityProfile struct {
	BrainKind  agent.Kind
	UpdatedAt  time.Time
	TaskScores map[string]*TaskTypeScore // key: taskType
	ColdStart  bool                      // true = 样本不足
}

// coldStartThreshold 冷启动样本阈值
const coldStartThreshold = 5

// WeightPolicy 权重调整策略
type WeightPolicy struct {
	LatencyPriority bool
	CostPriority    bool
	QualityPriority bool
}

// ComputeWeights 根据策略计算四维权重（归一化后总和=1）
// 返回: wAcc(准确率), wSpd(速度), wCst(成本), wStab(稳定性)
func ComputeWeights(policy WeightPolicy) (wAcc, wSpd, wCst, wStab float64) {
	// 基础权重
	wAcc, wSpd, wCst, wStab = 0.4, 0.2, 0.2, 0.2

	if policy.QualityPriority {
		wAcc += 0.2
		wStab += 0.1
	}
	if policy.LatencyPriority {
		wSpd += 0.3
	}
	if policy.CostPriority {
		wCst += 0.3
	}

	// 归一化
	total := wAcc + wSpd + wCst + wStab
	if total > 0 {
		wAcc /= total
		wSpd /= total
		wCst /= total
		wStab /= total
	}
	return
}

// WilsonConfidence 基于样本数计算置信度 [0, 1]
// 使用简化的 Wilson 区间下界思想: confidence = 1 - 1/sqrt(n+1)
func WilsonConfidence(n int) float64 {
	if n <= 0 {
		return 0
	}
	return 1 - 1/math.Sqrt(float64(n)+1)
}

// BrainRanking 排名结果
type BrainRanking struct {
	BrainKind   agent.Kind
	Score       float64
	Confidence  float64
	Explanation string
	IsColdStart bool
}

// ---------------------------------------------------------------------------
// L2: 流程级学习 — 任务序列优化
// ---------------------------------------------------------------------------

// TaskSequenceRecord 记录一次任务序列的执行结果
type TaskSequenceRecord struct {
	SequenceID string
	Steps      []TaskStep
	TotalScore float64
	RecordedAt time.Time
}

// TaskStep 任务序列中的一个步骤
type TaskStep struct {
	BrainKind agent.Kind
	TaskType  string
	Duration  time.Duration
	Score     float64

	// 以下为可选混杂因子（confounders），用于喂给 CausalLearner 以剔除虚假相关。
	// 任何字段缺失（零值）时该因子不会被记录，向后兼容旧调用方。
	ContextSize int    `json:",omitempty"` // 任务上下文 token 数（或近似 = len(Instruction)）
	Complexity  string `json:",omitempty"` // "simple" / "medium" / "hard"
	TimeBucket  string `json:",omitempty"` // "morning" / "afternoon" / "night"（按 RecordedAt 推断）
	ProjectKind string `json:",omitempty"` // 项目领域，如 "frontend" / "data" / "infra"
}

// SequenceLearner 学习任务序列的最优排列
type SequenceLearner struct {
	records []TaskSequenceRecord
	mu      sync.RWMutex
}

// RecordSequence 记录一次完成的任务序列
func (sl *SequenceLearner) RecordSequence(record TaskSequenceRecord) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if record.RecordedAt.IsZero() {
		record.RecordedAt = time.Now()
	}
	sl.records = append(sl.records, record)
}

// RecommendOrder 给定一组步骤，返回推荐的执行顺序。
// 策略：根据历史记录中相同类型步骤的平均得分/耗时比排序，高效步骤优先。
func (sl *SequenceLearner) RecommendOrder(steps []TaskStep) []TaskStep {
	sl.mu.RLock()
	defer sl.mu.RUnlock()

	// 统计每种 (brainKind, taskType) 的平均得分
	type key struct {
		kind     agent.Kind
		taskType string
	}
	avgScore := make(map[key]float64)
	counts := make(map[key]int)

	for _, rec := range sl.records {
		for _, s := range rec.Steps {
			k := key{s.BrainKind, s.TaskType}
			avgScore[k] += s.Score
			counts[k]++
		}
	}
	for k, total := range avgScore {
		avgScore[k] = total / float64(counts[k])
	}

	// 复制并排序：得分高的排前面
	result := make([]TaskStep, len(steps))
	copy(result, steps)
	sort.SliceStable(result, func(i, j int) bool {
		ki := key{result[i].BrainKind, result[i].TaskType}
		kj := key{result[j].BrainKind, result[j].TaskType}
		return avgScore[ki] > avgScore[kj]
	})
	return result
}

// ---------------------------------------------------------------------------
// L3: 偏好级学习 — 用户偏好建模
// ---------------------------------------------------------------------------

// UserPreference 用户偏好记录
type UserPreference struct {
	Category  string  // "output_format", "verbosity", "risk_tolerance" 等
	Value     string  // 偏好值
	Weight    float64 // 偏好强度 [0, 1]
	UpdatedAt time.Time
}

// PreferenceLearner 用户偏好学习器
type PreferenceLearner struct {
	preferences map[string]*UserPreference
	mu          sync.RWMutex
}

// RecordFeedback 记录用户反馈，更新指定类别的偏好
func (pl *PreferenceLearner) RecordFeedback(category, value string, weight float64) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	// 限制 weight 范围
	if weight < 0 {
		weight = 0
	}
	if weight > 1 {
		weight = 1
	}

	if existing, ok := pl.preferences[category]; ok {
		// 已有偏好：EWMA 混合权重
		existing.Value = value
		existing.Weight = 0.3*weight + 0.7*existing.Weight
		existing.UpdatedAt = time.Now()
	} else {
		pl.preferences[category] = &UserPreference{
			Category:  category,
			Value:     value,
			Weight:    weight,
			UpdatedAt: time.Now(),
		}
	}
}

// GetPreference 获取指定类别的偏好，不存在返回 nil
func (pl *PreferenceLearner) GetPreference(category string) *UserPreference {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	p, ok := pl.preferences[category]
	if !ok {
		return nil
	}
	// 返回副本
	cp := *p
	return &cp
}

// AllPreferences 返回所有偏好的快照
func (pl *PreferenceLearner) AllPreferences() map[string]UserPreference {
	pl.mu.RLock()
	defer pl.mu.RUnlock()
	result := make(map[string]UserPreference, len(pl.preferences))
	for k, v := range pl.preferences {
		result[k] = *v
	}
	return result
}

// ---------------------------------------------------------------------------
// LearningEngine — L1+L2+L3 统一入口
// ---------------------------------------------------------------------------

// LearningEngine 是 L1+L2+L3 的统一入口
type LearningEngine struct {
	profiles  map[agent.Kind]*BrainCapabilityProfile // L1
	sequences *SequenceLearner                        // L2
	prefs     *PreferenceLearner                      // L3
	causal    CausalLearner                           // 可选：因果推理引擎，剔除虚假相关
	active    ActiveLearner                           // 可选：主动学习器，评估不确定性并发起反馈请求
	store     persistence.LearningStore               // 可选持久化后端
	mu        sync.RWMutex
}

// NewLearningEngine 创建学习引擎。默认带 DefaultCausalLearner 和 DefaultActiveLearner，
// 调用方可用 SetCausalLearner(nil) / SetActiveLearner(nil) 关闭对应能力。
func NewLearningEngine() *LearningEngine {
	return &LearningEngine{
		profiles:  make(map[agent.Kind]*BrainCapabilityProfile),
		sequences: &SequenceLearner{},
		prefs: &PreferenceLearner{
			preferences: make(map[string]*UserPreference),
		},
		causal: NewCausalLearner(),
		active: NewActiveLearner(),
	}
}

// NewLearningEngineWithStore 创建带持久化的学习引擎
func NewLearningEngineWithStore(store persistence.LearningStore) *LearningEngine {
	le := NewLearningEngine()
	le.store = store
	return le
}

// SetCausalLearner 替换或清除内置 CausalLearner（传 nil 关闭因果学习）。
func (le *LearningEngine) SetCausalLearner(c CausalLearner) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.causal = c
}

// Causal 返回当前 CausalLearner（可能为 nil）。
func (le *LearningEngine) Causal() CausalLearner {
	le.mu.RLock()
	defer le.mu.RUnlock()
	return le.causal
}

// SetActiveLearner 替换或清除内置 ActiveLearner（传 nil 关闭主动学习）。
func (le *LearningEngine) SetActiveLearner(a ActiveLearner) {
	le.mu.Lock()
	defer le.mu.Unlock()
	le.active = a
}

// Active 返回当前 ActiveLearner（可能为 nil）。
func (le *LearningEngine) Active() ActiveLearner {
	le.mu.RLock()
	defer le.mu.RUnlock()
	return le.active
}

// Profiles 返回 L1 能力画像的快照（防御性拷贝）。
func (le *LearningEngine) Profiles() map[agent.Kind]*BrainCapabilityProfile {
	le.mu.RLock()
	defer le.mu.RUnlock()
	out := make(map[agent.Kind]*BrainCapabilityProfile, len(le.profiles))
	for k, v := range le.profiles {
		if v == nil {
			continue
		}
		cp := &BrainCapabilityProfile{
			BrainKind: v.BrainKind,
			UpdatedAt: v.UpdatedAt,
			ColdStart: v.ColdStart,
			TaskScores: make(map[string]*TaskTypeScore, len(v.TaskScores)),
		}
		for tk, tv := range v.TaskScores {
			if tv == nil {
				continue
			}
			cp.TaskScores[tk] = &TaskTypeScore{
				TaskType:    tv.TaskType,
				SampleCount: tv.SampleCount,
				Accuracy:    tv.Accuracy,
				Speed:       tv.Speed,
				Cost:        tv.Cost,
				Stability:   tv.Stability,
			}
		}
		out[k] = cp
	}
	return out
}

// Load 从持久化后端恢复全部学习数据到内存
func (le *LearningEngine) Load(ctx context.Context) error {
	if le.store == nil {
		return nil
	}
	le.mu.Lock()
	defer le.mu.Unlock()

	// L1: profiles + task scores
	profiles, err := le.store.ListProfiles(ctx)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	for _, p := range profiles {
		kind := agent.Kind(p.BrainKind)
		profile := &BrainCapabilityProfile{
			BrainKind:  kind,
			ColdStart:  p.ColdStart,
			UpdatedAt:  p.UpdatedAt,
			TaskScores: make(map[string]*TaskTypeScore),
		}
		scores, _ := le.store.ListTaskScores(ctx, p.BrainKind)
		for _, s := range scores {
			profile.TaskScores[s.TaskType] = &TaskTypeScore{
				TaskType:    s.TaskType,
				SampleCount: s.SampleCount,
				Accuracy:    EWMAScore{Value: s.AccuracyValue, Alpha: s.AccuracyAlpha},
				Speed:       EWMAScore{Value: s.SpeedValue, Alpha: s.SpeedAlpha},
				Cost:        EWMAScore{Value: s.CostValue, Alpha: s.CostAlpha},
				Stability:   EWMAScore{Value: s.StabilityValue, Alpha: s.StabilityAlpha},
			}
		}
		le.profiles[kind] = profile
	}

	// L2: sequences
	seqs, err := le.store.ListSequences(ctx, 0)
	if err != nil {
		return fmt.Errorf("load sequences: %w", err)
	}
	for _, seq := range seqs {
		rec := TaskSequenceRecord{
			SequenceID: seq.SequenceID,
			TotalScore: seq.TotalScore,
			RecordedAt: seq.RecordedAt,
		}
		for _, step := range seq.Steps {
			rec.Steps = append(rec.Steps, TaskStep{
				BrainKind: agent.Kind(step.BrainKind),
				TaskType:  step.TaskType,
				Duration:  time.Duration(step.DurationMs) * time.Millisecond,
				Score:     step.Score,
			})
		}
		le.sequences.records = append(le.sequences.records, rec)
	}

	// L3: preferences
	prefs, err := le.store.ListPreferences(ctx)
	if err != nil {
		return fmt.Errorf("load preferences: %w", err)
	}
	for _, p := range prefs {
		le.prefs.preferences[p.Category] = &UserPreference{
			Category:  p.Category,
			Value:     p.Value,
			Weight:    p.Weight,
			UpdatedAt: p.UpdatedAt,
		}
	}
	return nil
}

// Save 将当前内存中的全部学习数据持久化
func (le *LearningEngine) Save(ctx context.Context) error {
	if le.store == nil {
		return nil
	}
	le.mu.RLock()
	defer le.mu.RUnlock()

	// L1
	for _, profile := range le.profiles {
		if err := le.store.SaveProfile(ctx, &persistence.LearningProfile{
			BrainKind: string(profile.BrainKind),
			ColdStart: profile.ColdStart,
			UpdatedAt: profile.UpdatedAt,
		}); err != nil {
			return err
		}
		for _, ts := range profile.TaskScores {
			if err := le.store.SaveTaskScore(ctx, &persistence.LearningTaskScore{
				BrainKind:      string(profile.BrainKind),
				TaskType:       ts.TaskType,
				SampleCount:    ts.SampleCount,
				AccuracyValue:  ts.Accuracy.Value,
				AccuracyAlpha:  ts.Accuracy.Alpha,
				SpeedValue:     ts.Speed.Value,
				SpeedAlpha:     ts.Speed.Alpha,
				CostValue:      ts.Cost.Value,
				CostAlpha:      ts.Cost.Alpha,
				StabilityValue: ts.Stability.Value,
				StabilityAlpha: ts.Stability.Alpha,
			}); err != nil {
				return err
			}
		}
	}

	// L3
	for _, pref := range le.prefs.preferences {
		if err := le.store.SavePreference(ctx, &persistence.LearningPreference{
			Category:  pref.Category,
			Value:     pref.Value,
			Weight:    pref.Weight,
			UpdatedAt: pref.UpdatedAt,
		}); err != nil {
			return err
		}
	}
	return nil
}

// RecordDelegateResult 记录一次委派结果，更新 Brain 能力画像 (L1 反馈)
func (le *LearningEngine) RecordDelegateResult(
	brainKind agent.Kind,
	taskType string,
	accuracy, speed, cost, stability float64,
) {
	le.mu.Lock()
	defer le.mu.Unlock()

	profile, ok := le.profiles[brainKind]
	if !ok {
		profile = &BrainCapabilityProfile{
			BrainKind:  brainKind,
			TaskScores: make(map[string]*TaskTypeScore),
			ColdStart:  true,
		}
		le.profiles[brainKind] = profile
	}

	ts, ok := profile.TaskScores[taskType]
	if !ok {
		ts = &TaskTypeScore{
			TaskType: taskType,
			Accuracy:  EWMAScore{Alpha: 0.2},
			Speed:     EWMAScore{Alpha: 0.2},
			Cost:      EWMAScore{Alpha: 0.2},
			Stability: EWMAScore{Alpha: 0.2},
			LatencyMs: EWMAScore{Alpha: 0.2},
		}
		profile.TaskScores[taskType] = ts
	}

	ts.SampleCount++
	ts.Accuracy.Update(accuracy)
	ts.Speed.Update(speed)
	ts.Cost.Update(cost)
	ts.Stability.Update(stability)

	profile.ColdStart = ts.SampleCount < coldStartThreshold
	profile.UpdatedAt = time.Now()

	// 异步持久化（best-effort，不阻塞调用��）
	// 在锁内拷贝所有值，避免 goroutine 与后续写操作 data race
	if le.store != nil {
		profileSnap := persistence.LearningProfile{
			BrainKind: string(profile.BrainKind),
			ColdStart: profile.ColdStart,
			UpdatedAt: profile.UpdatedAt,
		}
		scoreSnap := persistence.LearningTaskScore{
			BrainKind:      string(brainKind),
			TaskType:       taskType,
			SampleCount:    ts.SampleCount,
			AccuracyValue:  ts.Accuracy.Value,
			AccuracyAlpha:  ts.Accuracy.Alpha,
			SpeedValue:     ts.Speed.Value,
			SpeedAlpha:     ts.Speed.Alpha,
			CostValue:      ts.Cost.Value,
			CostAlpha:      ts.Cost.Alpha,
			StabilityValue: ts.Stability.Value,
			StabilityAlpha: ts.Stability.Alpha,
			LatencyMsValue: ts.LatencyMs.Value,
			LatencyMsAlpha: ts.LatencyMs.Alpha,
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "learning: save profile panic: %v\n", r)
				}
			}()
			ctx := context.Background()
			le.store.SaveProfile(ctx, &profileSnap)
			le.store.SaveTaskScore(ctx, &scoreSnap)
		}()
	}
}

// RecordDelegateLatency 记录一次委派的实际延迟（毫秒）。
// 与 RecordDelegateResult 分离，避免破坏已有接口。
func (le *LearningEngine) RecordDelegateLatency(brainKind agent.Kind, taskType string, d time.Duration) {
	le.mu.Lock()
	defer le.mu.Unlock()

	profile, ok := le.profiles[brainKind]
	if !ok {
		profile = &BrainCapabilityProfile{
			BrainKind:  brainKind,
			TaskScores: make(map[string]*TaskTypeScore),
			ColdStart:  true,
		}
		le.profiles[brainKind] = profile
	}

	ts, ok := profile.TaskScores[taskType]
	if !ok {
		ts = &TaskTypeScore{
			TaskType:  taskType,
			Accuracy:  EWMAScore{Alpha: 0.2},
			Speed:     EWMAScore{Alpha: 0.2},
			Cost:      EWMAScore{Alpha: 0.2},
			Stability: EWMAScore{Alpha: 0.2},
			LatencyMs: EWMAScore{Alpha: 0.2},
		}
		profile.TaskScores[taskType] = ts
	}

	ms := float64(d.Milliseconds())
	if ms < 0 {
		ms = 0
	}
	ts.LatencyMs.Update(ms)
	profile.UpdatedAt = time.Now()
}

// EstimateTimeout 基于历史 EWMA 延迟估算建议超时。
// 返回 P95 估算值（EWMA * 2），并钳制在 [30s, 300s] 区间内。
// 冷启动（样本不足）时返回 0，表示"无建议，使用默认值"。
func (le *LearningEngine) EstimateTimeout(brainKind agent.Kind, taskType string) time.Duration {
	le.mu.RLock()
	defer le.mu.RUnlock()

	profile, ok := le.profiles[brainKind]
	if !ok {
		return 0
	}
	ts, ok := profile.TaskScores[taskType]
	if !ok || ts.SampleCount < coldStartThreshold {
		return 0
	}

	// P95 估算 = EWMA * 2（基于正态分布近似）。
	p95Ms := ts.LatencyMs.Value * 2
	if p95Ms <= 0 {
		return 0
	}

	timeout := time.Duration(p95Ms) * time.Millisecond
	const minTimeout = 30 * time.Second
	const maxTimeout = 300 * time.Second
	if timeout < minTimeout {
		timeout = minTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	return timeout
}

// RankBrains 对所有已知 brain 按 taskType 排名 (L1 入口)
func (le *LearningEngine) RankBrains(taskType string, policy WeightPolicy) []BrainRanking {
	le.mu.RLock()
	defer le.mu.RUnlock()

	wAcc, wSpd, wCst, wStab := ComputeWeights(policy)

	var rankings []BrainRanking
	for kind, profile := range le.profiles {
		ts, ok := profile.TaskScores[taskType]
		if !ok {
			continue
		}

		score := wAcc*ts.Accuracy.Value +
			wSpd*ts.Speed.Value +
			wCst*ts.Cost.Value +
			wStab*ts.Stability.Value
		confidence := WilsonConfidence(ts.SampleCount)

		rankings = append(rankings, BrainRanking{
			BrainKind:   kind,
			Score:       score,
			Confidence:  confidence,
			IsColdStart: ts.SampleCount < coldStartThreshold,
			Explanation: fmt.Sprintf(
				"acc=%.3f spd=%.3f cst=%.3f stab=%.3f samples=%d",
				ts.Accuracy.Value, ts.Speed.Value, ts.Cost.Value, ts.Stability.Value, ts.SampleCount,
			),
		})
	}

	// 按综合得分降序
	sort.SliceStable(rankings, func(i, j int) bool {
		return rankings[i].Score > rankings[j].Score
	})
	return rankings
}

// RecordSequence 记录任务序列 (L2 入口)
func (le *LearningEngine) RecordSequence(record TaskSequenceRecord) {
	le.sequences.RecordSequence(record)

	// 同步喂给 CausalLearner（nil 安全）：每个 step 一条 observation。
	// outcome 二值化：Score>=0.5 视为 success，否则 failure；OutcomeVal 直接用 Score。
	if causal := le.Causal(); causal != nil {
		ts := record.RecordedAt
		if ts.IsZero() {
			ts = time.Now()
		}
		bucket := timeBucketOf(ts)
		for _, s := range record.Steps {
			factors := map[string]string{
				"brain":     string(s.BrainKind),
				"task_kind": s.TaskType,
			}
			if s.Complexity != "" {
				factors["complexity"] = s.Complexity
			}
			if s.ProjectKind != "" {
				factors["project_kind"] = s.ProjectKind
			}
			if s.TimeBucket != "" {
				factors["time_bucket"] = s.TimeBucket
			} else if bucket != "" {
				factors["time_bucket"] = bucket
			}
			if s.ContextSize > 0 {
				factors["ctx_size"] = ctxSizeBucketOf(s.ContextSize)
			}
			outcome := "failure"
			if s.Score >= 0.5 {
				outcome = "success"
			}
			causal.Observe(CausalObservation{
				Factors:    factors,
				Outcome:    outcome,
				OutcomeVal: s.Score,
				Timestamp:  ts,
				TaskID:     record.SequenceID,
			})
		}
		// 周期性触发关系学习；轻量调用，可每次记录后跑一次。
		causal.LearnRelations()
	}

	if le.store != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "learning: save sequence panic: %v\n", r)
				}
			}()
			seq := &persistence.LearningSequence{
				SequenceID: record.SequenceID,
				TotalScore: record.TotalScore,
				RecordedAt: record.RecordedAt,
			}
			for _, s := range record.Steps {
				seq.Steps = append(seq.Steps, persistence.LearningSeqStep{
					BrainKind:  string(s.BrainKind),
					TaskType:   s.TaskType,
					DurationMs: s.Duration.Milliseconds(),
					Score:      s.Score,
				})
			}
			le.store.SaveSequence(context.Background(), seq)
		}()
	}
}

// RecommendOrder 推荐任务执行顺序 (L2 查询)
func (le *LearningEngine) RecommendOrder(steps []TaskStep) []TaskStep {
	return le.sequences.RecommendOrder(steps)
}

// RecordInteractionSequence 记录一次大脑内部 tool 级动作轨迹(L2 的细粒度
// 补充)。browser brain 调用本函数把完整的 browser action 序列持久化,供
// ui_pattern_learn.go 从 store 读取后聚类成 UIPattern。
//
// 未配置 LearningStore 时静默丢弃——调用方不需做任何判断。
func (le *LearningEngine) RecordInteractionSequence(ctx context.Context, seq *persistence.InteractionSequence) error {
	if le.store == nil || seq == nil {
		return nil
	}
	return le.store.SaveInteractionSequence(ctx, seq)
}

// ListInteractionSequences 查询某个 brain 的交互轨迹(聚类算法用)。
func (le *LearningEngine) ListInteractionSequences(ctx context.Context, brainKind string, limit int) ([]*persistence.InteractionSequence, error) {
	if le.store == nil {
		return nil, nil
	}
	return le.store.ListInteractionSequences(ctx, brainKind, limit)
}

// RecordUserFeedback 记录用户反馈 (L3 入口)
func (le *LearningEngine) RecordUserFeedback(category, value string, weight float64) {
	le.prefs.RecordFeedback(category, value, weight)
	if le.store != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "learning: save preference panic: %v\n", r)
				}
			}()
			pref := le.prefs.GetPreference(category)
			if pref != nil {
				le.store.SavePreference(context.Background(), &persistence.LearningPreference{
					Category:  pref.Category,
					Value:     pref.Value,
					Weight:    pref.Weight,
					UpdatedAt: pref.UpdatedAt,
				})
			}
		}()
	}
}

// GetPreference 获取用户偏好 (L3 查询)
func (le *LearningEngine) GetPreference(category string) *UserPreference {
	return le.prefs.GetPreference(category)
}

// ---------------------------------------------------------------------------
// P3.1 — AnomalyTemplate + SiteAnomalyProfile 持久化入口
// ---------------------------------------------------------------------------
//
// 本包不直接依赖 sdk/tool(避免循环),所以这里的入口仅暴露 persistence 层
// 类型。sdk/tool 侧的 AnomalyTemplate / HostAnomalyEntry 由调用方自己转换
// 成 persistence.AnomalyTemplate / persistence.SiteAnomalyProfile 再调用
// 下面这些方法,和 InteractionSequence 的风格一致。
//
// 没配 store 时静默丢弃,让单元测试和无持久化路径都能顺畅跑。

// SaveAnomalyTemplate 落盘一条异常模板(新建或更新)。
func (le *LearningEngine) SaveAnomalyTemplate(ctx context.Context, tpl *persistence.AnomalyTemplate) error {
	if le.store == nil || tpl == nil {
		return nil
	}
	return le.store.SaveAnomalyTemplate(ctx, tpl)
}

// GetAnomalyTemplate 按 ID 查询。不存在返回 (nil, nil);不存在或 store 未配时
// 调用方当作"无模板"处理。
func (le *LearningEngine) GetAnomalyTemplate(ctx context.Context, id int64) (*persistence.AnomalyTemplate, error) {
	if le.store == nil {
		return nil, nil
	}
	return le.store.GetAnomalyTemplate(ctx, id)
}

// ListAnomalyTemplates 全量读出(数量可控,目前 < 数千条,未来需要再加分页)。
// P3.1 启动时用它把持久化态反序列化回 AnomalyTemplateLibrary。
func (le *LearningEngine) ListAnomalyTemplates(ctx context.Context) ([]*persistence.AnomalyTemplate, error) {
	if le.store == nil {
		return nil, nil
	}
	return le.store.ListAnomalyTemplates(ctx)
}

// DeleteAnomalyTemplate 按 ID 删除。未配 store 或 ID=0 直接返回 nil。
func (le *LearningEngine) DeleteAnomalyTemplate(ctx context.Context, id int64) error {
	if le.store == nil || id == 0 {
		return nil
	}
	return le.store.DeleteAnomalyTemplate(ctx, id)
}

// UpsertSiteAnomalyProfile 落盘一条站点画像(upsert by site+type+subtype)。
// 画像由 browser 工具层的 siteHistory.snapshotProfiles() 生成后转换成 persistence
// 形式再调用。snapshotProfiles 返回切片,调用方按条目循环即可 —— 这里刻意
// 不做批量入口以保持接口最小化。
func (le *LearningEngine) UpsertSiteAnomalyProfile(ctx context.Context, p *persistence.SiteAnomalyProfile) error {
	if le.store == nil || p == nil {
		return nil
	}
	return le.store.UpsertSiteAnomalyProfile(ctx, p)
}

// ListSiteAnomalyProfiles 查询某站所有异常画像。LLM 辅助修复工具里会读它。
// site 为空返回空切片(不遍历全表)。
func (le *LearningEngine) ListSiteAnomalyProfiles(ctx context.Context, site string) ([]*persistence.SiteAnomalyProfile, error) {
	if le.store == nil || site == "" {
		return nil, nil
	}
	return le.store.ListSiteAnomalyProfiles(ctx, site)
}

// IngestBrainMetrics 消费 sidecar 上报的 BrainMetrics，转换为 L1 四维指标
// 喂给 RecordDelegateResult。这是 L0→L1 的桥接入口。
func (le *LearningEngine) IngestBrainMetrics(m BrainMetrics) {
	if m.TaskCount == 0 {
		return
	}
	// 将 BrainMetrics 映射到四维指标：
	// accuracy  ← SuccessRate
	// speed     ← 延迟归一化（30s 内线性映射到 [0,1]）
	// cost      ← 固定 0.5（sidecar 侧无成本信息）
	// stability ← ConfidenceTrend 归一化到 [0,1]
	accuracy := m.SuccessRate

	speed := 0.0
	if m.AvgLatencyMs > 0 && m.AvgLatencyMs < 30000 {
		speed = 1.0 - m.AvgLatencyMs/30000
	}

	cost := 0.5 // sidecar 无成本维度，使用中性值

	stability := 0.5 + m.ConfidenceTrend*0.5
	if stability < 0 {
		stability = 0
	}
	if stability > 1 {
		stability = 1
	}

	le.RecordDelegateResult(m.BrainKind, "aggregated", accuracy, speed, cost, stability)
}

// ---------------------------------------------------------------------------
// L0: Brain 级学习接口 — 每个 sidecar brain 实现
// ---------------------------------------------------------------------------

// BrainLearner 是每个 Brain 实现的 L0 级学习接口。
// sidecar 通过 RPC 上报指标，Orchestrator 消费后喂给 LearningEngine。
type BrainLearner interface {
	// RecordOutcome 记录一次任务执行结果。
	RecordOutcome(ctx context.Context, outcome TaskOutcome) error
	// ExportMetrics 导出当前 brain 的聚合指标快照。
	ExportMetrics() BrainMetrics
	// Adapt 根据历史指标触发领域参数自适应调整。
	// 由 LearningEngine 定期调用（如每小时）或 brain 空闲时自触发。
	Adapt(ctx context.Context) error
}

// ToolOutcomeRecorder 是 BrainLearner 的可选扩展接口。
// 实现此接口的 learner 可以在每次单工具调用后接收更细粒度的成功/失败
// 信号，从而构建领域特化的指标（如编译成功率、页面加载成功率等）。
type ToolOutcomeRecorder interface {
	RecordToolOutcome(toolName string, success bool)
}

// TaskOutcome 是一次任务执行的结果报告。
type TaskOutcome struct {
	TaskType   string        // 任务类型标识
	Success    bool          // 是否成功
	Duration   time.Duration // 执行耗时
	TokensUsed int           // LLM token 消耗
	ToolCalls  int           // 工具调用次数
	ErrorType  string        // 错误类型（成功时为空）
}

// BrainMetrics 是 brain 级聚合指标。
type BrainMetrics struct {
	BrainKind       agent.Kind    `json:"brain_kind"`
	Period          time.Duration `json:"period"`           // 统计周期
	TaskCount       int           `json:"task_count"`
	SuccessRate     float64       `json:"success_rate"`
	AvgLatencyMs    float64       `json:"avg_latency_ms"`
	ConfidenceTrend float64       `json:"confidence_trend"` // 置信度趋势（正=改善，负=下降）
}

// DefaultBrainLearner 是 BrainLearner 的通用默认实现。
// 内部用 EWMA 跟踪成功率和延迟，适用于不需要领域特化学习的 brain。
type DefaultBrainLearner struct {
	kind         agent.Kind
	mu           sync.Mutex
	taskCount    int
	successCount int
	latencyEWMA  EWMAScore
	startTime    time.Time
	store        persistence.LearningStore
}

// NewDefaultBrainLearner 创建默认 BrainLearner 实例。
func NewDefaultBrainLearner(kind agent.Kind) *DefaultBrainLearner {
	return &DefaultBrainLearner{
		kind:        kind,
		latencyEWMA: EWMAScore{Alpha: 0.2},
		startTime:   time.Now(),
	}
}

// NewDefaultBrainLearnerWithStore 创建带持久化的 BrainLearner。
func NewDefaultBrainLearnerWithStore(kind agent.Kind, store persistence.LearningStore) *DefaultBrainLearner {
	d := NewDefaultBrainLearner(kind)
	d.store = store
	return d
}

// Load 从持久化存储恢复 L0 指标。
func (d *DefaultBrainLearner) Load(ctx context.Context) error {
	if d.store == nil {
		return nil
	}
	scores, err := d.store.ListTaskScores(ctx, string(d.kind))
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range scores {
		d.taskCount += s.SampleCount
		d.successCount += int(s.AccuracyValue * float64(s.SampleCount))
		d.latencyEWMA.Value = s.SpeedValue
		if s.SampleCount > 0 {
			d.latencyEWMA.Updated = time.Now()
		}
	}
	return nil
}

// Save 将当前 L0 指标持久化。
func (d *DefaultBrainLearner) Save(ctx context.Context) error {
	if d.store == nil {
		return nil
	}
	d.mu.Lock()
	successRate := 0.0
	if d.taskCount > 0 {
		successRate = float64(d.successCount) / float64(d.taskCount)
	}
	ts := &persistence.LearningTaskScore{
		BrainKind:     string(d.kind),
		TaskType:      "default",
		SampleCount:   d.taskCount,
		AccuracyValue: successRate,
		AccuracyAlpha: 0.2,
		SpeedValue:    d.latencyEWMA.Value,
		SpeedAlpha:    0.2,
	}
	d.mu.Unlock()
	return d.store.SaveTaskScore(ctx, ts)
}

// RecordOutcome 记录一次任务执行结果，更新 EWMA 指标。
func (d *DefaultBrainLearner) RecordOutcome(ctx context.Context, outcome TaskOutcome) error {
	d.mu.Lock()
	d.taskCount++
	if outcome.Success {
		d.successCount++
	}
	d.latencyEWMA.Update(outcome.Duration.Seconds())
	shouldSave := d.store != nil && d.taskCount%5 == 0
	d.mu.Unlock()
	if shouldSave {
		go d.Save(ctx)
	}
	return nil
}

// ExportMetrics 导出当前聚合指标快照。
func (d *DefaultBrainLearner) ExportMetrics() BrainMetrics {
	d.mu.Lock()
	defer d.mu.Unlock()

	successRate := 0.0
	if d.taskCount > 0 {
		successRate = float64(d.successCount) / float64(d.taskCount)
	}

	// 置信度趋势：用最近 EWMA 值与历史平均的差值近似
	// 正值=延迟在改善（降低），负值=延迟在恶化
	trend := 0.0
	if d.taskCount > 1 {
		avgLatency := d.latencyEWMA.Value
		if avgLatency > 0 {
			// 简化：如果成功率 > 0.5 视为正向趋势
			trend = successRate - 0.5
		}
	}

	return BrainMetrics{
		BrainKind:       d.kind,
		Period:          time.Since(d.startTime),
		TaskCount:       d.taskCount,
		SuccessRate:     successRate,
		AvgLatencyMs:    d.latencyEWMA.Value * 1000,
		ConfidenceTrend: trend,
	}
}

// Adapt 触发一次持久化保存（如果配置了 store）。
// DefaultBrainLearner 的领域无关，因此 Adapt 仅做数据落盘。
func (d *DefaultBrainLearner) Adapt(ctx context.Context) error {
	if d.store == nil {
		return nil
	}
	return d.Save(ctx)
}

// timeBucketOf 根据本地时间小时数把时间分桶为 morning/afternoon/night，
// 供 CausalLearner 把"时间段"作为混杂因子。
func timeBucketOf(t time.Time) string {
	h := t.Hour()
	switch {
	case h >= 6 && h < 12:
		return "morning"
	case h >= 12 && h < 18:
		return "afternoon"
	default:
		return "night"
	}
}

// durationComplexity 把执行耗时离散化为 simple/medium/hard，
// 作为 CausalLearner 的 complexity 因子粗代理。零值返回空串。
func durationComplexity(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < 5*time.Second:
		return "simple"
	case d < 30*time.Second:
		return "medium"
	default:
		return "hard"
	}
}

// ctxSizeBucketOf 把 token / 字符数离散化为粗粒度桶，
// 避免 CausalLearner 因连续值产生过多稀疏因子。
func ctxSizeBucketOf(n int) string {
	switch {
	case n <= 0:
		return ""
	case n < 1024:
		return "small"
	case n < 8192:
		return "medium"
	case n < 32768:
		return "large"
	default:
		return "huge"
	}
}
