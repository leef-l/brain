package provider

import (
	"testing"

	"github.com/leef-l/brain/cmd/brain/config"
)

func TestOpenMock(t *testing.T) {
	s := OpenMock("hello")
	if s.Name != "mock" {
		t.Fatalf("expected Name=mock, got %s", s.Name)
	}
	if s.Model != "mock" {
		t.Fatalf("expected Model=mock, got %s", s.Model)
	}
	if s.Provider == nil {
		t.Fatal("expected non-nil Provider")
	}
}

func TestResolveWithInputDefaults(t *testing.T) {
	r, err := ResolveWithInput(nil, "", nil, "", "", "", "")
	if err == nil {
		t.Fatal("expected error when no config or input available")
	}
	_ = r
}

func TestResolveWithInputFlagOverrides(t *testing.T) {
	cfg := &config.Config{
		BaseURL: "https://default.example.com",
		APIKey:  "default-key",
		Model:   "default-model",
	}
	r, err := ResolveWithInput(cfg, "", nil, "anthropic", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Name != "anthropic" {
		t.Fatalf("expected Name=anthropic, got %s", r.Name)
	}
	if r.APIKey != "default-key" {
		t.Fatalf("expected APIKey=default-key, got %s", r.APIKey)
	}

	// Flag overrides.
	r, err = ResolveWithInput(cfg, "", nil, "", "flag-key", "https://flag.example.com", "flag-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.APIKey != "flag-key" {
		t.Fatalf("expected APIKey=flag-key, got %s", r.APIKey)
	}
	if r.BaseURL != "https://flag.example.com" {
		t.Fatalf("expected BaseURL=https://flag.example.com, got %s", r.BaseURL)
	}
	if r.Model != "flag-model" {
		t.Fatalf("expected Model=flag-model, got %s", r.Model)
	}
}

func TestResolveWithInputProviderConfig(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]*config.ProviderConfig{
			"deepseek": {
				BaseURL:  "https://deepseek.example.com",
				APIKey:   "deepseek-key",
				Model:    "deepseek-model",
				Protocol: "openai",
				Models:   map[string]string{"code": "deepseek-coder"},
			},
		},
	}

	r, err := ResolveWithInput(cfg, "code", nil, "deepseek", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Name != "deepseek" {
		t.Fatalf("expected Name=deepseek, got %s", r.Name)
	}
	if r.BaseURL != "https://deepseek.example.com" {
		t.Fatalf("expected BaseURL from provider config, got %s", r.BaseURL)
	}
	if r.APIKey != "deepseek-key" {
		t.Fatalf("expected APIKey from provider config, got %s", r.APIKey)
	}
	if r.Model != "deepseek-coder" {
		t.Fatalf("expected Model=deepseek-coder (brain-specific), got %s", r.Model)
	}
	if r.Protocol != "openai" {
		t.Fatalf("expected Protocol=openai, got %s", r.Protocol)
	}
}

func TestResolveWithInputEnvFallback(t *testing.T) {
	// When cfg has no API key but env does.
	cfg := &config.Config{BaseURL: "https://api.anthropic.com"}
	r, err := ResolveWithInput(cfg, "", nil, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// APIKey may come from ANTHROPIC_API_KEY env.
	_ = r.APIKey
}

func TestResolveWithInputInputOverrides(t *testing.T) {
	cfg := &config.Config{
		APIKey: "cfg-key",
		Model:  "cfg-model",
	}
	input := &config.ModelConfigInput{
		APIKey:      "input-key",
		Model:       "input-model",
		BrainModels: map[string]string{"code": "input-code-model"},
	}

	r, err := ResolveWithInput(cfg, "code", input, "", "", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.APIKey != "input-key" {
		t.Fatalf("expected APIKey=input-key, got %s", r.APIKey)
	}
	if r.Model != "input-code-model" {
		t.Fatalf("expected Model=input-code-model, got %s", r.Model)
	}
}
