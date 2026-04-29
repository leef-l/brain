package central

import (
	"testing"
	"time"

	"github.com/leef-l/brain/central/llm"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.LLM.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("expected LLM BaseURL=https://api.deepseek.com/v1, got %s", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "deepseek-chat" {
		t.Fatalf("expected LLM Model=deepseek-chat, got %s", cfg.LLM.Model)
	}
	if cfg.LLM.MaxTokens != 500 {
		t.Fatalf("expected LLM MaxTokens=500, got %d", cfg.LLM.MaxTokens)
	}
	if cfg.LLM.Temperature != 0.3 {
		t.Fatalf("expected LLM Temperature=0.3, got %f", cfg.LLM.Temperature)
	}
	if cfg.LLM.Timeout != "15s" {
		t.Fatalf("expected LLM Timeout=15s, got %s", cfg.LLM.Timeout)
	}
	if !cfg.Review.Enabled {
		t.Fatal("expected Review Enabled=true")
	}
	if cfg.Review.TriggerConcurrent != 3 {
		t.Fatalf("expected Review TriggerConcurrent=3, got %d", cfg.Review.TriggerConcurrent)
	}
	if cfg.Review.TriggerPositionPct != 5 {
		t.Fatalf("expected Review TriggerPositionPct=5, got %f", cfg.Review.TriggerPositionPct)
	}
	if cfg.Review.TriggerDailyLoss != 3 {
		t.Fatalf("expected Review TriggerDailyLoss=3, got %f", cfg.Review.TriggerDailyLoss)
	}
	if cfg.Review.Timeout != "10s" {
		t.Fatalf("expected Review Timeout=10s, got %s", cfg.Review.Timeout)
	}
	if cfg.Review.MaxTokens != 500 {
		t.Fatalf("expected Review MaxTokens=500, got %d", cfg.Review.MaxTokens)
	}
}

func TestBuildLLMConfig(t *testing.T) {
	cfg := LLMConfig{
		APIKey:      "test-key",
		BaseURL:     "https://test.example.com/v1",
		Model:       "test-model",
		MaxTokens:   1000,
		Temperature: 0.5,
		Timeout:     "30s",
	}
	lc := cfg.BuildLLMConfig()
	if lc.APIKey != "test-key" {
		t.Fatalf("expected APIKey=test-key, got %s", lc.APIKey)
	}
	if lc.BaseURL != "https://test.example.com/v1" {
		t.Fatalf("expected BaseURL=https://test.example.com/v1, got %s", lc.BaseURL)
	}
	if lc.Model != "test-model" {
		t.Fatalf("expected Model=test-model, got %s", lc.Model)
	}
	if lc.MaxTokens != 1000 {
		t.Fatalf("expected MaxTokens=1000, got %d", lc.MaxTokens)
	}
	if lc.Temperature != 0.5 {
		t.Fatalf("expected Temperature=0.5, got %f", lc.Temperature)
	}
	if lc.Timeout != 30*time.Second {
		t.Fatalf("expected Timeout=30s, got %v", lc.Timeout)
	}
}

func TestBuildLLMConfigDefaults(t *testing.T) {
	// Empty config should fall back to llm.DefaultConfig().
	cfg := LLMConfig{}
	lc := cfg.BuildLLMConfig()
	defaultCfg := llm.DefaultConfig()
	if lc.BaseURL != defaultCfg.BaseURL {
		t.Fatalf("expected BaseURL fallback, got %s", lc.BaseURL)
	}
	if lc.Model != defaultCfg.Model {
		t.Fatalf("expected Model fallback, got %s", lc.Model)
	}
	if lc.MaxTokens != defaultCfg.MaxTokens {
		t.Fatalf("expected MaxTokens fallback, got %d", lc.MaxTokens)
	}
	if lc.Temperature != defaultCfg.Temperature {
		t.Fatalf("expected Temperature fallback, got %f", lc.Temperature)
	}
	if lc.Timeout != defaultCfg.Timeout {
		t.Fatalf("expected Timeout fallback, got %v", lc.Timeout)
	}
}

func TestBuildLLMConfigInvalidTimeout(t *testing.T) {
	cfg := LLMConfig{Timeout: "invalid"}
	lc := cfg.BuildLLMConfig()
	defaultCfg := llm.DefaultConfig()
	if lc.Timeout != defaultCfg.Timeout {
		t.Fatalf("expected Timeout fallback on invalid duration, got %v", lc.Timeout)
	}
}
