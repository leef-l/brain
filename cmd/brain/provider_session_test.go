package main

import "testing"

func TestOpenConfiguredProvider_UsesExplicitModelConfig(t *testing.T) {
	cfg := &brainConfig{
		ActiveProvider: "anthropic",
		Providers: map[string]*providerConfig{
			"anthropic": {
				BaseURL: "https://api.anthropic.com",
				APIKey:  "cfg-key",
				Model:   "cfg-model",
				Models: map[string]string{
					"code": "cfg-code-model",
				},
			},
		},
	}

	session, err := openConfiguredProvider(cfg, "code", &modelConfigInput{
		Provider: "anthropic",
		APIKey:   "override-key",
		BaseURL:  "https://example.invalid",
		BrainModels: map[string]string{
			"code": "override-code-model",
		},
	}, "", "", "", "")
	if err != nil {
		t.Fatalf("openConfiguredProvider: %v", err)
	}
	if session.Name != "anthropic" {
		t.Fatalf("Name=%q, want anthropic", session.Name)
	}
	if session.Model != "override-code-model" {
		t.Fatalf("Model=%q, want override-code-model", session.Model)
	}
}

func TestWantsMockProvider(t *testing.T) {
	if !wantsMockProvider("mock", nil) {
		t.Fatal("expected mock provider from flag")
	}
	if !wantsMockProvider("", &modelConfigInput{Provider: "mock"}) {
		t.Fatal("expected mock provider from model config")
	}
}

func TestHasModelConfigOverrides(t *testing.T) {
	if hasModelConfigOverrides(nil) {
		t.Fatal("nil config should not count as explicit override")
	}
	if hasModelConfigOverrides(&modelConfigInput{Provider: "anthropic"}) {
		t.Fatal("provider-only config should not count as explicit override")
	}
	if !hasModelConfigOverrides(&modelConfigInput{Model: "claude-sonnet-4-20250514"}) {
		t.Fatal("model override should count as explicit override")
	}
	if !hasModelConfigOverrides(&modelConfigInput{BrainModels: map[string]string{"code": "claude"}}) {
		t.Fatal("brain_models override should count as explicit override")
	}
}
