package toolpolicy

import (
	"sort"
	"sync"

	"github.com/leef-l/brain/sdk/tool"
)

// AdaptiveToolPolicy 是运行时动态工具策略。
// 在静态配置之上叠加任务级、brain 级的动态调整。
type AdaptiveToolPolicy interface {
	// Evaluate 根据上下文动态筛选工具，返回当前应激活的 Registry。
	Evaluate(req EvalRequest, base tool.Registry) tool.Registry

	// RecordOutcome 记录一次工具执行结果，供后续 Suggest 使用。
	RecordOutcome(toolName string, taskType string, success bool)

	// Suggest 基于历史成功率推荐工具排序（高成功率优先）。
	Suggest(taskType string) []string

	// Override 运行时覆盖某个 scope 的活跃 profile。
	Override(scope string, profiles string)

	// ClearOverride 清除运行时覆盖，恢复静态配置。
	ClearOverride(scope string)
}

// EvalRequest 是 Evaluate 的输入上下文。
type EvalRequest struct {
	BrainKind string
	TaskType  string
	Mode      string
	Scopes    []string

	// BrowserStage 仅在 BrainKind=="browser" 时有意义,按语义理解阶段
	// 切换曝露给 LLM 的工具集合:
	//   - "new_page":     新页面,优先 snapshot + understand
	//   - "known_flow":   疑似已知流程,优先 pattern_match / pattern_exec
	//   - "destructive":  即将动破坏性操作,加 visual_inspect 做视觉复核
	//   - "fallback":     understand/pattern 都不够用,开放 visual_inspect
	//                      + screenshot + eval
	// 空字符串或未知值退回通用 "run.browser" 作用域。
	BrowserStage string
}

// toolRecord 记录单个工具在特定 taskType 下的执行统计。
type toolRecord struct {
	calls    int
	successes int
}

// DefaultAdaptivePolicy 是 AdaptiveToolPolicy 的默认实现。
type DefaultAdaptivePolicy struct {
	base      *Config
	overrides map[string]string // scope → profiles（运行时覆盖）
	stats     map[string]map[string]*toolRecord // taskType → toolName → record
	mu        sync.RWMutex
}

// NewAdaptivePolicy 基于静态配置创建自适应策略。
func NewAdaptivePolicy(base *Config) *DefaultAdaptivePolicy {
	if base == nil {
		base = &Config{}
	}
	return &DefaultAdaptivePolicy{
		base:      base,
		overrides: make(map[string]string),
		stats:     make(map[string]map[string]*toolRecord),
	}
}

func (p *DefaultAdaptivePolicy) Evaluate(req EvalRequest, base tool.Registry) tool.Registry {
	if base == nil {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	// 合并静态配置和运行时覆盖
	effective := p.effectiveConfig()

	// 确定 scopes
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = p.inferScopes(req)
	}

	// 用合并后的配置筛选
	filtered := FilterRegistry(base, effective, scopes...)

	// 如果有历史统计，进一步过滤掉成功率极低的工具
	if taskStats, ok := p.stats[req.TaskType]; ok && req.TaskType != "" {
		final := tool.NewMemRegistry()
		for _, t := range filtered.List() {
			rec, exists := taskStats[t.Name()]
			if exists && rec.calls >= 5 && float64(rec.successes)/float64(rec.calls) <= 0.15 {
				continue // 成功率低于 10% 且样本足够，临时禁用
			}
			final.Register(t)
		}
		return final
	}

	return filtered
}

func (p *DefaultAdaptivePolicy) RecordOutcome(toolName string, taskType string, success bool) {
	if taskType == "" {
		taskType = "_default"
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stats[taskType] == nil {
		p.stats[taskType] = make(map[string]*toolRecord)
	}
	rec, ok := p.stats[taskType][toolName]
	if !ok {
		rec = &toolRecord{}
		p.stats[taskType][toolName] = rec
	}
	rec.calls++
	if success {
		rec.successes++
	}
}

func (p *DefaultAdaptivePolicy) Suggest(taskType string) []string {
	if taskType == "" {
		taskType = "_default"
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	taskStats, ok := p.stats[taskType]
	if !ok {
		return nil
	}

	type scored struct {
		name    string
		rate    float64
		calls   int
	}
	var items []scored
	for name, rec := range taskStats {
		if rec.calls == 0 {
			continue
		}
		items = append(items, scored{
			name:  name,
			rate:  float64(rec.successes) / float64(rec.calls),
			calls: rec.calls,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].rate != items[j].rate {
			return items[i].rate > items[j].rate
		}
		return items[i].calls > items[j].calls
	})

	result := make([]string, len(items))
	for i, it := range items {
		result[i] = it.name
	}
	return result
}

func (p *DefaultAdaptivePolicy) Override(scope string, profiles string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.overrides[scope] = profiles
}

func (p *DefaultAdaptivePolicy) ClearOverride(scope string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.overrides, scope)
}

func (p *DefaultAdaptivePolicy) effectiveConfig() *Config {
	// 始终返回独立副本，防止调用方通过共享引用污染 base 配置
	profiles := make(map[string]*Profile, len(p.base.ToolProfiles))
	for k, v := range p.base.ToolProfiles {
		if v != nil {
			cp := *v
			profiles[k] = &cp
		}
	}
	activeTools := make(map[string]string, len(p.base.ActiveTools)+len(p.overrides))
	for k, v := range p.base.ActiveTools {
		activeTools[k] = v
	}
	for k, v := range p.overrides {
		activeTools[k] = v
	}
	return &Config{
		ToolProfiles: profiles,
		ActiveTools:  activeTools,
	}
}

func (p *DefaultAdaptivePolicy) inferScopes(req EvalRequest) []string {
	base := p.inferBaseScopes(req)
	if req.BrainKind == "browser" && req.BrowserStage != "" {
		base = append(base, ToolScopesForBrowserStage(req.BrowserStage)...)
	}
	return base
}

func (p *DefaultAdaptivePolicy) inferBaseScopes(req EvalRequest) []string {
	if req.Mode == "chat" {
		return ToolScopesForChat(req.BrainKind, "")
	}
	if req.Mode == "run" {
		return ToolScopesForRun(req.BrainKind)
	}
	if req.Mode == "delegate" {
		return ToolScopesForDelegate(req.BrainKind)
	}
	return []string{"run"}
}

// compile-time check
var _ AdaptiveToolPolicy = (*DefaultAdaptivePolicy)(nil)
