package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type modelConfigInput struct {
	Provider    string            `json:"provider,omitempty"`
	BaseURL     string            `json:"base_url,omitempty"`
	APIKey      string            `json:"api_key,omitempty"`
	Model       string            `json:"model,omitempty"`
	BrainModels map[string]string `json:"brain_models,omitempty"`
}

func parseModelConfigJSON(raw string) (*modelConfigInput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	cfg := &modelConfigInput{}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return nil, fmt.Errorf("parse model_config_json: %w", err)
	}
	return cfg, nil
}

func wantsMockProvider(flagProvider string, cfg *modelConfigInput) bool {
	if strings.EqualFold(strings.TrimSpace(flagProvider), "mock") {
		return true
	}
	return cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Provider), "mock")
}

func hasModelConfigOverrides(cfg *modelConfigInput) bool {
	if cfg == nil {
		return false
	}
	if strings.TrimSpace(cfg.APIKey) != "" || strings.TrimSpace(cfg.BaseURL) != "" || strings.TrimSpace(cfg.Model) != "" {
		return true
	}
	return len(cfg.BrainModels) > 0
}
