package tool

// anomaly_template.go — P3.1-A 异常模板库(类比 UIPattern)。
//
// 把"每次遇到异常都交给 LLM 现想 recovery"升级为"模板匹配 → 命中按模板执行,
// 没命中才走 OnAnomaly 静态配置或回退 LLM"。
//
// 数据持久化走 persistence.LearningStore 的 anomaly_templates 表(#16 已加),
// 这里只做内存匹配 + 阈值判定,Save/Load 由 kernel/learning.go 调度。
//
// 和 M3(模式自动停用)共用阈值哲学:FailureCount>=5 && SuccessRate<0.3 → Disabled。
// 和 M5(on_anomaly 路由)集成点:ui_pattern_match.go:540 附近,matchAnomalyHandler
// 之前先过 AnomalyTemplateLibrary.Match。

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// AnomalyTemplateRecoveryAction 是模板 Recovery 序列里的一步。
// Kind 复用 OnAnomaly 的分类语义,多出一个 "custom_steps" —— 模板可以
// 直接描述一个小 ActionSequence(重选元素 + 点击 + 等待 idle)。
type AnomalyTemplateRecoveryAction struct {
	Kind       string                 `json:"kind"` // retry / fallback_pattern / human_intervention / custom_steps
	MaxRetries int                    `json:"max_retries,omitempty"`
	BackoffMS  int                    `json:"backoff_ms,omitempty"`
	FallbackID string                 `json:"fallback_id,omitempty"` // kind=fallback_pattern
	Steps      []AnomalyTemplateStep  `json:"steps,omitempty"`        // kind=custom_steps
	Reason     string                 `json:"reason,omitempty"`
	Params     map[string]interface{} `json:"params,omitempty"`
}

// AnomalyTemplateStep 是 custom_steps 内部的单步,schema 精简版的 ActionStep,
// 保证不引入 UIPattern 的完整依赖(pattern_exec 可以消费这两种 schema)。
type AnomalyTemplateStep struct {
	Tool       string                 `json:"tool"`
	Params     map[string]interface{} `json:"params,omitempty"`
	TargetRole string                 `json:"target_role,omitempty"`
	Optional   bool                   `json:"optional,omitempty"`
}

// AnomalyTemplateSignature 标识一个模板能匹配什么 anomaly。
// SitePattern 是正则(空 = 任意站点);Severity 为空表示任意;Subtype 为空
// 表示只按 Type 匹配(粗模板)。
type AnomalyTemplateSignature struct {
	Type        string `json:"type"`         // 对应 AnomalyType("session_expired" 等)
	Subtype     string `json:"subtype,omitempty"`
	SitePattern string `json:"site_pattern,omitempty"` // 正则,空=任意
	Severity    string `json:"severity,omitempty"`      // info/low/medium/high/blocker,空=任意
}

// AnomalyTemplateStats 命中与执行结果的累积计数。Disabled 由阈值自动翻。
type AnomalyTemplateStats struct {
	MatchCount   int       `json:"match_count"`
	SuccessCount int       `json:"success_count"`
	FailureCount int       `json:"failure_count"`
	UpdatedAt    time.Time `json:"updated_at"`
	Disabled     bool      `json:"disabled,omitempty"`
}

// SuccessRate 返回成功率;样本太少(<3)返回 -1,让调用方走冷启动路径。
func (s *AnomalyTemplateStats) SuccessRate() float64 {
	n := s.SuccessCount + s.FailureCount
	if n < 3 {
		return -1
	}
	return float64(s.SuccessCount) / float64(n)
}

// AnomalyTemplate 是库里的一条记录。ID 为 0 表示未入库(内存新建),
// 入库后由 persistence 回填自增 ID。
type AnomalyTemplate struct {
	ID        int64                           `json:"id"`
	Signature AnomalyTemplateSignature        `json:"signature"`
	Recovery  []AnomalyTemplateRecoveryAction `json:"recovery"`
	Stats     AnomalyTemplateStats            `json:"stats"`
	Source    string                          `json:"source,omitempty"` // "seed" | "mined" | "llm"
	CreatedAt time.Time                       `json:"created_at"`
	UpdatedAt time.Time                       `json:"updated_at"`

	sitePatternRE *regexp.Regexp // 懒编译缓存
}

// compileSite 懒编译 SitePattern。非法正则返回 nil(调用方降级为仅按 type/subtype 匹配)。
func (t *AnomalyTemplate) compileSite() *regexp.Regexp {
	if t.Signature.SitePattern == "" {
		return nil
	}
	if t.sitePatternRE != nil {
		return t.sitePatternRE
	}
	re, err := regexp.Compile(t.Signature.SitePattern)
	if err != nil {
		return nil
	}
	t.sitePatternRE = re
	return re
}

// matches 判断模板能否处理给定 anomaly + site。匹配规则:
//   - Type 必须精确相等(大小写不敏感)
//   - Subtype 非空时必须精确匹配;模板 Subtype 空 = 通配
//   - SitePattern 非空时必须正则 match;空 = 通配
//   - Severity 非空时必须精确匹配;空 = 通配
//   - Disabled 模板不参与匹配
func (t *AnomalyTemplate) matches(anomalyType, subtype, siteOrigin, severity string) bool {
	if t.Stats.Disabled {
		return false
	}
	if !strings.EqualFold(t.Signature.Type, anomalyType) {
		return false
	}
	if t.Signature.Subtype != "" && !strings.EqualFold(t.Signature.Subtype, subtype) {
		return false
	}
	if t.Signature.Severity != "" && !strings.EqualFold(t.Signature.Severity, severity) {
		return false
	}
	if t.Signature.SitePattern != "" {
		re := t.compileSite()
		if re == nil {
			return false
		}
		if !re.MatchString(siteOrigin) {
			return false
		}
	}
	return true
}

// Specificity 返回模板的具体度得分。匹配时具体度高的优先(Site + Subtype + Severity
// 都匹配 > 只 Type 匹配)。用于 Match 排序。
func (t *AnomalyTemplate) Specificity() int {
	s := 0
	if t.Signature.Subtype != "" {
		s += 4
	}
	if t.Signature.SitePattern != "" {
		s += 2
	}
	if t.Signature.Severity != "" {
		s += 1
	}
	return s
}

// ---------------------------------------------------------------------------
// AnomalyTemplateLibrary — 内存态模板库(类比 ui_pattern_library)
// ---------------------------------------------------------------------------

// AnomalyTemplateLibrary 是线程安全的内存模板库。Load/Save 由 kernel 层
// 的 LearningEngine 驱动 —— 本包不直接依赖 persistence,避免循环。
type AnomalyTemplateLibrary struct {
	mu        sync.RWMutex
	templates map[int64]*AnomalyTemplate // key: persistence ID(内存态临时记录用负数)
	nextTemp  int64                      // 内存临时 ID 递减计数器
	// autoDisableThreshold: FailureCount 达到时才开始判断停用;
	// autoDisableRate: SuccessRate 低于此值即停用。
	// 复用 M3 模式自动停用的阈值哲学。
	autoDisableThreshold int
	autoDisableRate      float64
}

// NewAnomalyTemplateLibrary 创建空库。阈值和 M3 对齐:5 次失败 + 成功率 <0.3 自动停用。
func NewAnomalyTemplateLibrary() *AnomalyTemplateLibrary {
	return &AnomalyTemplateLibrary{
		templates:            map[int64]*AnomalyTemplate{},
		nextTemp:             -1,
		autoDisableThreshold: 5,
		autoDisableRate:      0.3,
	}
}

// Upsert 加入 / 覆盖一条模板。ID=0 分配临时负 ID(持久化时回填)。
func (lib *AnomalyTemplateLibrary) Upsert(tpl *AnomalyTemplate) *AnomalyTemplate {
	if tpl == nil {
		return nil
	}
	lib.mu.Lock()
	defer lib.mu.Unlock()
	now := time.Now()
	if tpl.CreatedAt.IsZero() {
		tpl.CreatedAt = now
	}
	tpl.UpdatedAt = now
	if tpl.ID == 0 {
		tpl.ID = lib.nextTemp
		lib.nextTemp--
	}
	lib.templates[tpl.ID] = tpl
	return tpl
}

// Delete 按 ID 删除模板。
func (lib *AnomalyTemplateLibrary) Delete(id int64) {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	delete(lib.templates, id)
}

// List 返回所有模板(拷贝切片头,模板本身仍为指针 —— 只读场景用)。
func (lib *AnomalyTemplateLibrary) List() []*AnomalyTemplate {
	lib.mu.RLock()
	defer lib.mu.RUnlock()
	out := make([]*AnomalyTemplate, 0, len(lib.templates))
	for _, t := range lib.templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Match 返回最具体的匹配模板(Specificity 最大,平手选 SuccessRate 高的)。
// 没有匹配返回 nil。输入 site/severity 可以空字符串。
func (lib *AnomalyTemplateLibrary) Match(anomalyType, subtype, siteOrigin, severity string) *AnomalyTemplate {
	lib.mu.RLock()
	defer lib.mu.RUnlock()
	var candidates []*AnomalyTemplate
	for _, t := range lib.templates {
		if t.matches(anomalyType, subtype, siteOrigin, severity) {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		si, sj := candidates[i].Specificity(), candidates[j].Specificity()
		if si != sj {
			return si > sj
		}
		ri, rj := candidates[i].Stats.SuccessRate(), candidates[j].Stats.SuccessRate()
		if ri != rj {
			return ri > rj
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0]
}

// RecordOutcome 累计执行结果并按阈值自动停用。threshold 样本 + <rate 成功率 → Disabled。
// 返回更新后的模板(供调用方持久化)。
func (lib *AnomalyTemplateLibrary) RecordOutcome(id int64, success bool) *AnomalyTemplate {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	t := lib.templates[id]
	if t == nil {
		return nil
	}
	t.Stats.MatchCount++
	if success {
		t.Stats.SuccessCount++
	} else {
		t.Stats.FailureCount++
	}
	t.Stats.UpdatedAt = time.Now()
	t.UpdatedAt = t.Stats.UpdatedAt
	// 自动停用:累积失败达阈值且成功率低于下限
	if t.Stats.FailureCount >= lib.autoDisableThreshold {
		if r := t.Stats.SuccessRate(); r >= 0 && r < lib.autoDisableRate {
			t.Stats.Disabled = true
		}
	}
	return t
}

// Enable 手动启用(ops 可以覆盖自动停用判断)。
func (lib *AnomalyTemplateLibrary) Enable(id int64) *AnomalyTemplate {
	lib.mu.Lock()
	defer lib.mu.Unlock()
	t := lib.templates[id]
	if t == nil {
		return nil
	}
	t.Stats.Disabled = false
	t.UpdatedAt = time.Now()
	return t
}

// PromoteCandidate 用于 C 子项:LLM 首次修复成功后,若累积 ≥minSamples
// 且 SuccessRate ≥minRate,将候选方案固化为 AnomalyTemplate 入库。
// 返回新入库模板;不满足条件返回 nil。
func (lib *AnomalyTemplateLibrary) PromoteCandidate(
	sig AnomalyTemplateSignature,
	recovery []AnomalyTemplateRecoveryAction,
	stats AnomalyTemplateStats,
	minSamples int,
	minRate float64,
) *AnomalyTemplate {
	if minSamples <= 0 {
		minSamples = 3
	}
	total := stats.SuccessCount + stats.FailureCount
	if total < minSamples {
		return nil
	}
	if total > 0 {
		r := float64(stats.SuccessCount) / float64(total)
		if r < minRate {
			return nil
		}
	}
	tpl := &AnomalyTemplate{
		Signature: sig,
		Recovery:  recovery,
		Stats:     stats,
		Source:    "llm",
	}
	return lib.Upsert(tpl)
}

// ---------------------------------------------------------------------------
// JSON roundtrip helpers
// ---------------------------------------------------------------------------

// EncodeRecoveryActions 序列化 Recovery 列表为 json.RawMessage,给
// persistence 层复用。空切片返回 `[]`。
func EncodeRecoveryActions(actions []AnomalyTemplateRecoveryAction) json.RawMessage {
	if len(actions) == 0 {
		return json.RawMessage(`[]`)
	}
	raw, _ := json.Marshal(actions)
	return raw
}

// DecodeRecoveryActions 反序列化。空 / nil 返回空切片(不报错,方便 store 回填)。
func DecodeRecoveryActions(raw json.RawMessage) ([]AnomalyTemplateRecoveryAction, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var out []AnomalyTemplateRecoveryAction
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode recovery: %w", err)
	}
	return out, nil
}
