// causal_learning.go — 因果学习引擎
// 从统计相关升级为因果推理，分析"为什么"而不仅仅是"什么"。
package kernel

import (
	"math"
	"sync"
	"time"
)

// CausalFactor 因果因子
type CausalFactor struct {
	FactorID  string  `json:"factor_id"`
	Name      string  `json:"name"`      // 如 "brain_kind", "task_complexity", "tool_count"
	Value     string  `json:"value"`     // 因子的值
	Weight    float64 `json:"weight"`    // 因果强度 0-1
	Direction string  `json:"direction"` // positive/negative/neutral
}

// Intervention 干预记录
type Intervention struct {
	InterventionID string    `json:"intervention_id"`
	Factor         string    `json:"factor"`         // 被干预的因子
	OldValue       string    `json:"old_value"`
	NewValue       string    `json:"new_value"`
	OutcomeBefore  float64   `json:"outcome_before"` // 干预前效果
	OutcomeAfter   float64   `json:"outcome_after"`  // 干预后效果
	Timestamp      time.Time `json:"timestamp"`
}

// CausalRelation 因果关系
type CausalRelation struct {
	RelationID    string         `json:"relation_id"`
	Cause         CausalFactor   `json:"cause"`
	Effect        string         `json:"effect"`      // 效果描述（如 "task_success", "budget_overrun"）
	Strength      float64        `json:"strength"`    // 因果强度 0-1
	Confidence    float64        `json:"confidence"`  // 置信度 0-1
	SampleCount   int            `json:"sample_count"`
	Interventions []Intervention `json:"interventions,omitempty"`
}

// CausalObservation 观测数据
type CausalObservation struct {
	ObsID      string            `json:"obs_id"`
	Factors    map[string]string `json:"factors"`     // factor_name -> value
	Outcome    string            `json:"outcome"`     // success/failure/partial
	OutcomeVal float64           `json:"outcome_val"` // 量化结果 0-1
	Timestamp  time.Time         `json:"timestamp"`
	ProjectID  string            `json:"project_id,omitempty"`
	TaskID     string            `json:"task_id,omitempty"`
}

// CausalGraph 因果图
type CausalGraph struct {
	mu        sync.RWMutex
	Relations map[string]*CausalRelation `json:"relations"`
	Nodes     map[string]bool            `json:"nodes"`
}

// NewCausalGraph 创建空因果图
func NewCausalGraph() *CausalGraph {
	return &CausalGraph{
		Relations: make(map[string]*CausalRelation),
		Nodes:     make(map[string]bool),
	}
}

// AddRelation 添加或更新因果关系
func (g *CausalGraph) AddRelation(r *CausalRelation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Relations[r.RelationID] = r
	g.Nodes[r.Cause.Name+"="+r.Cause.Value] = true
	g.Nodes[r.Effect] = true
}

// GetRelations 获取所有因果关系（只读快照）
func (g *CausalGraph) GetRelations() []*CausalRelation {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]*CausalRelation, 0, len(g.Relations))
	for _, r := range g.Relations {
		out = append(out, r)
	}
	return out
}

// CausalLearner 因果学习器接口
type CausalLearner interface {
	Observe(obs CausalObservation)
	LearnRelations() []CausalRelation
	QueryCause(effect string) []CausalRelation
	QueryEffect(factorName, factorValue string) []CausalRelation
	Counterfactual(obs CausalObservation, changedFactor, newValue string) float64
	Suggest(targetOutcome string) []CausalFactor
}

// factorOutcomeStat 因子-效果的内部统计
type factorOutcomeStat struct {
	count    int     // factor=value 且 outcome 出现的次数
	sumVal   float64 // outcome_val 累计
	totalHit int     // factor=value 出现的总次数
}

// DefaultCausalLearner 基于共现统计的因果学习器
type DefaultCausalLearner struct {
	mu            sync.RWMutex
	observations  []CausalObservation
	graph         *CausalGraph
	stats         map[string]*factorOutcomeStat // "factor_name:factor_value:outcome" -> stat
	factorCounts  map[string]int                // "factor_name:factor_value" -> count
	outcomeCounts map[string]int                // outcome -> count
	totalObs      int
}

// NewCausalLearner 创建因果学习器
func NewCausalLearner() *DefaultCausalLearner {
	return &DefaultCausalLearner{
		observations:  make([]CausalObservation, 0, 64),
		graph:         NewCausalGraph(),
		stats:         make(map[string]*factorOutcomeStat),
		factorCounts:  make(map[string]int),
		outcomeCounts: make(map[string]int),
	}
}

// Observe 存储观测数据，更新因子-效果的统计
func (cl *DefaultCausalLearner) Observe(obs CausalObservation) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.observations = append(cl.observations, obs)
	cl.totalObs++
	cl.outcomeCounts[obs.Outcome]++
	for name, value := range obs.Factors {
		fk := name + ":" + value
		cl.factorCounts[fk]++
		sk := fk + ":" + obs.Outcome
		st, ok := cl.stats[sk]
		if !ok {
			st = &factorOutcomeStat{}
			cl.stats[sk] = st
		}
		st.count++
		st.sumVal += obs.OutcomeVal
		st.totalHit = cl.factorCounts[fk]
	}
}

// LearnRelations 基于共现统计计算因果强度
// 对每对 (factor, outcome)，计算 P(outcome|factor) vs P(outcome)，
// 差异 >0.1 视为显著因果关系。
func (cl *DefaultCausalLearner) LearnRelations() []CausalRelation {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	if cl.totalObs == 0 {
		return nil
	}
	var relations []CausalRelation
	for sk, st := range cl.stats {
		fname, fval, outcome := parseCausalStatKey(sk)
		if fname == "" {
			continue
		}
		fk := fname + ":" + fval
		factorTotal := cl.factorCounts[fk]
		if factorTotal == 0 {
			continue
		}
		pOutcomeGivenFactor := float64(st.count) / float64(factorTotal)
		pOutcome := float64(cl.outcomeCounts[outcome]) / float64(cl.totalObs)
		causalEffect := pOutcomeGivenFactor - pOutcome
		if math.Abs(causalEffect) < 0.1 {
			continue
		}
		direction := "neutral"
		if causalEffect > 0 {
			direction = "positive"
		} else if causalEffect < 0 {
			direction = "negative"
		}
		confidence := math.Min(float64(st.count)/20.0, 1.0)
		rid := fname + ":" + fval + "->" + outcome
		rel := CausalRelation{
			RelationID: rid,
			Cause: CausalFactor{
				FactorID:  fk,
				Name:      fname,
				Value:     fval,
				Weight:    math.Abs(causalEffect),
				Direction: direction,
			},
			Effect:      outcome,
			Strength:    math.Abs(causalEffect),
			Confidence:  confidence,
			SampleCount: st.count,
		}
		relations = append(relations, rel)
		cl.graph.AddRelation(&rel)
	}
	return relations
}

// QueryCause 查找导致某效果的所有因子
func (cl *DefaultCausalLearner) QueryCause(effect string) []CausalRelation {
	rels := cl.graph.GetRelations()
	var matched []CausalRelation
	for _, r := range rels {
		if r.Effect == effect {
			matched = append(matched, *r)
		}
	}
	return matched
}

// QueryEffect 查找某因子值产生的所有效果
func (cl *DefaultCausalLearner) QueryEffect(factorName, factorValue string) []CausalRelation {
	rels := cl.graph.GetRelations()
	var matched []CausalRelation
	for _, r := range rels {
		if r.Cause.Name == factorName && r.Cause.Value == factorValue {
			matched = append(matched, *r)
		}
	}
	return matched
}

// Counterfactual 简单反事实推理：找到相似观测中改变了该因子的案例，返回预估 outcome 值
func (cl *DefaultCausalLearner) Counterfactual(obs CausalObservation, changedFactor, newValue string) float64 {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	var sumVal float64
	var count int
	for i := range cl.observations {
		o := &cl.observations[i]
		if v, ok := o.Factors[changedFactor]; !ok || v != newValue {
			continue
		}
		if causalFactorSimilarity(obs.Factors, o.Factors, changedFactor) >= 0.5 {
			sumVal += o.OutcomeVal
			count++
		}
	}
	if count == 0 {
		return obs.OutcomeVal
	}
	return sumVal / float64(count)
}

// Suggest 为达到目标 outcome，建议调整哪些因子（按因果强度降序）
func (cl *DefaultCausalLearner) Suggest(targetOutcome string) []CausalFactor {
	causes := cl.QueryCause(targetOutcome)
	var suggestions []CausalFactor
	for i := range causes {
		c := &causes[i]
		if c.Cause.Direction == "positive" && c.Strength > 0.15 {
			suggestions = append(suggestions, c.Cause)
		}
	}
	// 按因果强度降序
	for i := 0; i < len(suggestions); i++ {
		for j := i + 1; j < len(suggestions); j++ {
			if suggestions[j].Weight > suggestions[i].Weight {
				suggestions[i], suggestions[j] = suggestions[j], suggestions[i]
			}
		}
	}
	return suggestions
}

// parseCausalStatKey 解析 "factor_name:factor_value:outcome" 格式的 key
func parseCausalStatKey(key string) (fname, fval, outcome string) {
	first, last := -1, -1
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			if first == -1 {
				first = i
			}
			last = i
		}
	}
	if first == -1 || first == last {
		return "", "", ""
	}
	return key[:first], key[first+1 : last], key[last+1:]
}

// causalFactorSimilarity 计算两组因子的相似度（排除 excludeKey）
func causalFactorSimilarity(a, b map[string]string, excludeKey string) float64 {
	if len(a) == 0 {
		return 0
	}
	total, matched := 0, 0
	for k, va := range a {
		if k == excludeKey {
			continue
		}
		total++
		if vb, ok := b[k]; ok && vb == va {
			matched++
		}
	}
	if total == 0 {
		return 1.0
	}
	return float64(matched) / float64(total)
}
