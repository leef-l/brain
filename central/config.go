package central

import (
	"time"

	"github.com/leef-l/brain/central/llm"
)

// Config is the full Central Brain configuration, loadable from JSON/YAML.
type Config struct {
	LLM    LLMConfig    `json:"llm" yaml:"llm"`
	Review ReviewConfig `json:"review" yaml:"review"`
}

// LLMConfig holds LLM provider settings.
type LLMConfig struct {
	APIKey      string `json:"api_key" yaml:"api_key"`
	BaseURL     string `json:"base_url" yaml:"base_url"`
	Model       string `json:"model" yaml:"model"`
	MaxTokens   int    `json:"max_tokens" yaml:"max_tokens"`
	Temperature float64 `json:"temperature" yaml:"temperature"`
	Timeout     string `json:"timeout" yaml:"timeout"` // duration string, e.g. "15s"
}

// ReviewConfig holds trade review trigger thresholds.
type ReviewConfig struct {
	Enabled            bool    `json:"enabled" yaml:"enabled"`
	TriggerConcurrent  int     `json:"trigger_concurrent" yaml:"trigger_concurrent"`     // open positions >= N triggers review
	TriggerPositionPct float64 `json:"trigger_position_pct" yaml:"trigger_position_pct"` // largest position > N% triggers review
	TriggerDailyLoss   float64 `json:"trigger_daily_loss" yaml:"trigger_daily_loss"`     // daily loss > N% triggers review
	Timeout            string  `json:"timeout" yaml:"timeout"`                           // LLM response timeout
	MaxTokens          int     `json:"max_tokens" yaml:"max_tokens"`                     // LLM output token limit for review
}

// DefaultConfig returns the default Central Brain configuration.
func DefaultConfig() Config {
	return Config{
		LLM: LLMConfig{
			BaseURL:     "https://api.deepseek.com/v1",
			Model:       "deepseek-chat",
			MaxTokens:   500,
			Temperature: 0.3,
			Timeout:     "15s",
		},
		Review: ReviewConfig{
			Enabled:            true,
			TriggerConcurrent:  3,
			TriggerPositionPct: 5,
			TriggerDailyLoss:   3,
			Timeout:            "10s",
			MaxTokens:          500,
		},
	}
}

// BuildLLMConfig converts LLMConfig to the llm.Config used by the LLM client.
// Environment variables override config file values (API key, base URL, model).
func (c LLMConfig) BuildLLMConfig() llm.Config {
	cfg := llm.DefaultConfig()
	if c.BaseURL != "" {
		cfg.BaseURL = c.BaseURL
	}
	if c.APIKey != "" {
		cfg.APIKey = c.APIKey
	}
	if c.Model != "" {
		cfg.Model = c.Model
	}
	if c.MaxTokens > 0 {
		cfg.MaxTokens = c.MaxTokens
	}
	if c.Temperature > 0 {
		cfg.Temperature = c.Temperature
	}
	if c.Timeout != "" {
		if d, err := time.ParseDuration(c.Timeout); err == nil {
			cfg.Timeout = d
		}
	}
	return cfg
}
