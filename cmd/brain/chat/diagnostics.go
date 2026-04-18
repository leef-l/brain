package chat

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/leef-l/brain/cmd/brain/config"
	"github.com/leef-l/brain/cmd/brain/provider"
	"github.com/leef-l/brain/sdk/llm"
)

func RunStartupDiagnostics(session provider.Session, cfg *config.Config) string {
	var warnings []string

	if cfg != nil {
		warnings = append(warnings, checkBaseURL(cfg)...)
		warnings = append(warnings, checkAPIKey(cfg)...)
	}

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

func checkBaseURL(cfg *config.Config) []string {
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
		path := strings.TrimRight(u.Path, "/")
		if strings.HasSuffix(path, "/v1") || strings.HasSuffix(path, "/v3") {
			warnings = append(warnings, fmt.Sprintf(
				"Provider %q: base_url ends with %q — code appends /v1/messages, so the final path will be %s/v1/messages. Did you mean to remove the trailing version segment?",
				name, path[strings.LastIndex(path, "/"):], path))
		}
	}
	return warnings
}

func checkAPIKey(cfg *config.Config) []string {
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

func checkProviderConnectivity(session provider.Session) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	req := minimalChatRequest()
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

func isReasonerModel(session provider.Session) bool {
	m := strings.ToLower(session.Model)
	return strings.Contains(m, "reasoner") || strings.Contains(m, "-r1")
}

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
