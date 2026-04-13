package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/leef-l/brain/llm"
)

type providerSession struct {
	Provider llm.Provider
	Name     string
	Model    string
}

func openConfiguredProvider(cfg *brainConfig, brainKind string, input *modelConfigInput, flagProvider, flagKey, flagURL, flagModel string) (providerSession, error) {
	resolved, err := resolveProviderConfigWithInput(cfg, brainKind, input, flagProvider, flagKey, flagURL, flagModel)
	if err != nil {
		return providerSession{}, err
	}
	if resolved.APIKey == "" {
		return providerSession{}, fmt.Errorf("no API key configured")
	}

	return providerSession{
		Provider: llm.NewAnthropicProvider(resolved.BaseURL, resolved.APIKey, resolved.Model),
		Name:     resolved.Name,
		Model:    resolved.Model,
	}, nil
}

func openMockProvider(reply string) providerSession {
	mock := llm.NewMockProvider("mock")
	mock.QueueText(reply)
	return providerSession{
		Provider: mock,
		Name:     "mock",
		Model:    "mock",
	}
}

func resolveProviderConfigWithInput(cfg *brainConfig, brainKind string, input *modelConfigInput, flagProvider, flagKey, flagURL, flagModel string) (resolvedProvider, error) {
	var r resolvedProvider

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
		return resolvedProvider{}, fmt.Errorf("no config available")
	}
	return r, nil
}
