package provider

import (
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
}

func OpenConfigured(cfg *config.Config, brainKind string, input *config.ModelConfigInput, flagProvider, flagKey, flagURL, flagModel string) (Session, error) {
	resolved, err := ResolveWithInput(cfg, brainKind, input, flagProvider, flagKey, flagURL, flagModel)
	if err != nil {
		return Session{}, err
	}
	if resolved.APIKey == "" {
		return Session{}, fmt.Errorf("no API key configured")
	}

	var p llm.Provider
	switch strings.ToLower(resolved.Protocol) {
	case "openai":
		p = llm.NewOpenAIProvider(resolved.BaseURL, resolved.APIKey, resolved.Model)
	default:
		p = llm.NewAnthropicProvider(resolved.BaseURL, resolved.APIKey, resolved.Model)
	}

	return Session{
		Provider: p,
		Name:     resolved.Name,
		Model:    resolved.Model,
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
