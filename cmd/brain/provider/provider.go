package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/sdk/llm"
)

type Session struct {
	Provider llm.Provider
	Name     string
	Model    string
	// Capabilities 是装配点解析后传给 Runner 的最终 capability 值。
	// 每个 Provider 内部已通过 WithXxxCapabilities 注入了相同的值,
	// 这里 expose 仅用于 dashboard / 日志 / 测试观察。
	Capabilities llm.Capabilities
}

func OpenConfigured(cfg *config.Config, brainKind string, input *config.ModelConfigInput, flagProvider, flagKey, flagURL, flagModel string) (Session, error) {
	resolved, err := ResolveWithInput(cfg, brainKind, input, flagProvider, flagKey, flagURL, flagModel)
	if err != nil {
		return Session{}, err
	}
	if resolved.APIKey == "" {
		return Session{}, fmt.Errorf("no API key configured")
	}

	// Phase 7 — Capability 优先级链。
	//
	//   default → InferCapabilities → builtin 表 → user override(config.json)
	//
	// 解析用户在 active_provider.capabilities 块的 raw JSON;parse 失败
	// 不静默吞 —— 用户配错(typo)应该立刻报错让用户修,不应该悄悄
	// 退化到默认值导致 mimo / deepseek 等模型表现异常用户找不到原因。
	var userOverride *llm.CapabilitiesOverride
	if len(resolved.Capabilities) > 0 {
		var ov llm.CapabilitiesOverride
		if err := json.Unmarshal(resolved.Capabilities, &ov); err != nil {
			return Session{}, fmt.Errorf("active_provider.capabilities parse: %w", err)
		}
		userOverride = &ov
	}
	caps := llm.ResolveCapabilities(resolved.BaseURL, resolved.Model, userOverride)

	var p llm.Provider
	switch strings.ToLower(resolved.Protocol) {
	case "openai":
		p = llm.NewOpenAIProvider(resolved.BaseURL, resolved.APIKey, resolved.Model,
			llm.WithOpenAICapabilities(caps))
	default:
		p = llm.NewAnthropicProvider(resolved.BaseURL, resolved.APIKey, resolved.Model,
			llm.WithAnthropicCapabilities(caps))
	}

	return Session{
		Provider:     p,
		Name:         resolved.Name,
		Model:        resolved.Model,
		Capabilities: caps,
	}, nil
}

func OpenMock(reply string) Session {
	mock := llm.NewMockProvider("mock")
	mock.QueueText(reply)
	return Session{
		Provider: mock,
		Name:     "mock",
		Model:    "mock",
	}
}

func ResolveWithInput(cfg *config.Config, brainKind string, input *config.ModelConfigInput, flagProvider, flagKey, flagURL, flagModel string) (config.ResolvedProvider, error) {
	var r config.ResolvedProvider

	providerName := strings.TrimSpace(flagProvider)
	if providerName == "" && input != nil {
		providerName = strings.TrimSpace(input.Provider)
	}
	if providerName == "" && cfg != nil {
		providerName = strings.TrimSpace(cfg.ActiveProvider)
	}
	if providerName == "" {
		providerName = "anthropic"
	}
	r.Name = providerName

	if cfg != nil {
		r.BaseURL = cfg.BaseURL
		r.APIKey = cfg.APIKey
		r.Model = cfg.Model

		if cfg.Providers != nil {
			if p, ok := cfg.Providers[providerName]; ok && p != nil {
				if p.BaseURL != "" {
					r.BaseURL = p.BaseURL
				}
				if p.APIKey != "" {
					r.APIKey = p.APIKey
				}
				if p.Model != "" {
					r.Model = p.Model
				}
				if p.Protocol != "" {
					r.Protocol = p.Protocol
				}
				if brainKind != "" && p.Models != nil {
					if model, ok := p.Models[brainKind]; ok && strings.TrimSpace(model) != "" {
						r.Model = model
					}
				}
				// Phase 7 — 透传用户声明的 capability 覆盖 raw JSON,
				// 装配点(OpenConfigured)反序列化为 *llm.CapabilitiesOverride。
				if len(p.Capabilities) > 0 {
					r.Capabilities = p.Capabilities
				}
			}
		}
	}

	if r.APIKey == "" {
		r.APIKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	if input != nil {
		if input.BaseURL != "" {
			r.BaseURL = input.BaseURL
		}
		if input.APIKey != "" {
			r.APIKey = input.APIKey
		}
		if input.Model != "" {
			r.Model = input.Model
		}
		if brainKind != "" && input.BrainModels != nil {
			if model, ok := input.BrainModels[brainKind]; ok && strings.TrimSpace(model) != "" {
				r.Model = model
			}
		}
	}

	if flagKey != "" {
		r.APIKey = flagKey
	}
	if flagURL != "" {
		r.BaseURL = flagURL
	}
	if flagModel != "" {
		r.Model = flagModel
	}

	if strings.TrimSpace(r.APIKey) == "" && cfg == nil && input == nil {
		return config.ResolvedProvider{}, fmt.Errorf("no config available")
	}
	return r, nil
}
