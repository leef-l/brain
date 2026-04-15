package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/leef-l/brain/sdk/llm"
)

// runStartupDiagnostics performs a lightweight health check before entering the
// REPL. It returns a formatted diagnostic string (warnings/errors) to print,
// or "" if everything looks good.
func runStartupDiagnostics(session providerSession, cfg *brainConfig) string {
	var warnings []string

	// 1. Config-level checks.
	if cfg != nil {
		warnings = append(warnings, checkBaseURL(cfg)...)
		warnings = append(warnings, checkAPIKey(cfg)...)
	}

	// 2. Provider connectivity: send a tiny request to verify the endpoint.
	if connWarn := checkProviderConnectivity(session); connWarn != "" {
		warnings = append(warnings, connWarn)
	}

	if len(warnings) == 0 {
		return ""
	}

	var b strings.Builder
	for _, w := range warnings {
		b.WriteString(fmt.Sprintf("  \033[1;33m⚠ %s\033[0m\n", w))
	}
	return b.String()
}

// checkBaseURL validates the provider base_url format.
func checkBaseURL(cfg *brainConfig) []string {
	var warnings []string
	if cfg.Providers == nil {
		return nil
	}
	for name, p := range cfg.Providers {
		if p == nil || p.BaseURL == "" {
			continue
		}
		u, err := url.Parse(p.BaseURL)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Provider %q: invalid base_url: %v", name, err))
			continue
		}
		// Anthropic provider appends /v1/messages. Warn if base_url already
		// contains /v1 or /v3 — a common misconfiguration when mixing OpenAI
		// and Anthropic endpoints.
		path := strings.TrimRight(u.Path, "/")
		if strings.HasSuffix(path, "/v1") || strings.HasSuffix(path, "/v3") {
			warnings = append(warnings, fmt.Sprintf(
				"Provider %q: base_url ends with %q — code appends /v1/messages, so the final path will be %s/v1/messages. Did you mean to remove the trailing version segment?",
				name, path[strings.LastIndex(path, "/"):], path))
		}
	}
	return warnings
}

// checkAPIKey warns about empty or placeholder API keys.
func checkAPIKey(cfg *brainConfig) []string {
	var warnings []string
	active := cfg.ActiveProvider
	if active == "" {
		return nil
	}
	if cfg.Providers == nil {
		return nil
	}
	p, ok := cfg.Providers[active]
	if !ok || p == nil {
		warnings = append(warnings, fmt.Sprintf("Active provider %q not found in providers config", active))
		return warnings
	}
	if strings.TrimSpace(p.APIKey) == "" {
		warnings = append(warnings, fmt.Sprintf("Provider %q: api_key is empty", active))
	}
	return warnings
}

// checkProviderConnectivity sends a minimal LLM request to verify the
// endpoint is reachable. Timeout is short (8s) to avoid blocking startup.
func checkProviderConnectivity(session providerSession) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	req := minimalChatRequest()
	// Reasoner models (e.g. deepseek-reasoner) spend tokens on internal
	// reasoning before producing visible content. A max_tokens of 1 may
	// yield an empty content field while the reasoning_content is non-empty.
	// Use a slightly larger budget so the model can emit at least one
	// visible token after reasoning.
	if isReasonerModel(session) {
		req.MaxTokens = 16
	}

	resp, err := session.Provider.Complete(ctx, req)
	if err != nil {
		return fmt.Sprintf("Provider connectivity check failed: %v", err)
	}
	if resp == nil || len(resp.Content) == 0 {
		return "Provider returned empty response — check base_url and model"
	}
	return ""
}

// isReasonerModel returns true if the active provider session uses a model
// known to perform chain-of-thought reasoning (e.g. deepseek-reasoner).
func isReasonerModel(session providerSession) bool {
	m := strings.ToLower(session.Model)
	return strings.Contains(m, "reasoner") || strings.Contains(m, "-r1")
}

// minimalChatRequest creates the smallest possible ChatRequest for a health
// check. It asks the LLM to respond with a single character to minimise
// token usage and latency.
func minimalChatRequest() *llm.ChatRequest {
	return &llm.ChatRequest{
		Messages: []llm.Message{
			{
				Role: "user",
				Content: []llm.ContentBlock{
					{Type: "text", Text: "hi"},
				},
			},
		},
		MaxTokens: 1,
	}
}
