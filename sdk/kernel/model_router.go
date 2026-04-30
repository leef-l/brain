package kernel

import (
	"sync"

	"github.com/leef-l/brain/sdk/agent"
)

// ModelConfig 单个 brain 的模型配置。
type ModelConfig struct {
	Kind          agent.Kind `json:"kind"`
	PrimaryModel  string     `json:"primary_model"`  // 主模型
	FallbackModel string     `json:"fallback_model"` // 备用模型
	MaxTokens     int        `json:"max_tokens"`     // 最大输出 token
	Temperature   float64    `json:"temperature"`    // 温度
	Reason        string     `json:"reason"`         // 选择理由
}

// ModelRoutingStrategy 模型路由策略。
type ModelRoutingStrategy string

const (
	StrategyStatic   ModelRoutingStrategy = "static"   // 固定配置
	StrategyAdaptive ModelRoutingStrategy = "adaptive" // 基于学习数据自适应
	StrategyCost     ModelRoutingStrategy = "cost"     // 成本优先
	StrategyQuality  ModelRoutingStrategy = "quality"  // 质量优先
)

// ModelRouter 多模型路由器。
// 管理 brain → model 的映射，支持按策略动态选择。
type ModelRouter struct {
	mu       sync.RWMutex
	configs  map[agent.Kind]*ModelConfig
	strategy ModelRoutingStrategy
	learner  *LearningEngine
	defaults map[string]string // brain_kind_hint → default model
}

// NewModelRouter 创建多模型路由器。
func NewModelRouter(strategy ModelRoutingStrategy) *ModelRouter {
	return &ModelRouter{
		configs:  make(map[agent.Kind]*ModelConfig),
		strategy: strategy,
		defaults: defaultModelHints(),
	}
}

// SetLearner 注入学习引擎用于自适应路由。
func (r *ModelRouter) SetLearner(learner *LearningEngine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.learner = learner
}

// Configure 设置指定 brain 的模型配置。
func (r *ModelRouter) Configure(config ModelConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configs[config.Kind] = &config
}

// Resolve 解析指定 brain 应使用的模型。
// 优先级：显式配置 > 自适应选择 > 默认映射
func (r *ModelRouter) Resolve(kind agent.Kind, taskType string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. 显式配置
	if cfg, ok := r.configs[kind]; ok && cfg.PrimaryModel != "" {
		return cfg.PrimaryModel
	}

	// 2. 自适应（如果策略是 adaptive 且有 learner）
	if r.strategy == StrategyAdaptive && r.learner != nil {
		// 查 learner profiles，选择该 kind 历史性能最好的 model
		// 当前 learner 不跟踪 model 维度，所以这里 fallback
		_ = taskType
	}

	// 3. 默认映射
	if model, ok := r.defaults[string(kind)]; ok {
		return model
	}

	return "" // 使用系统默认
}

// ResolveConfig 解析完整的模型配置。
func (r *ModelRouter) ResolveConfig(kind agent.Kind) *ModelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if cfg, ok := r.configs[kind]; ok {
		cp := *cfg
		return &cp
	}
	return &ModelConfig{
		Kind:         kind,
		PrimaryModel: r.defaults[string(kind)],
	}
}

// SyncToLLMProxy 将当前路由配置同步到 LLMProxy。
func (r *ModelRouter) SyncToLLMProxy(proxy *LLMProxy) {
	if proxy == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if proxy.ModelForKind == nil {
		proxy.ModelForKind = make(map[agent.Kind]string)
	}
	for kind, cfg := range r.configs {
		if cfg.PrimaryModel != "" {
			proxy.ModelForKind[kind] = cfg.PrimaryModel
		}
	}
}

// AllConfigs 返回所有模型配置的快照。
func (r *ModelRouter) AllConfigs() []ModelConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ModelConfig, 0, len(r.configs))
	for _, cfg := range r.configs {
		out = append(out, *cfg)
	}
	return out
}

// defaultModelHints 返回每种 brain 的建议默认模型。
func defaultModelHints() map[string]string {
	return map[string]string{
		"central":  "", // 使用系统默认（通常是超长上下文模型）
		"code":     "", // 代码专用模型
		"verifier": "", // 推理专用模型
		"browser":  "", // 多模态模型
		"data":     "", // 轻量快速模型
		"quant":    "", // 数学专用模型
	}
}
