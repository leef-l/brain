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
	// 各类模型的最小可用 token 数差异很大:
	//   - Anthropic Sonnet/Haiku: 1 即可
	//   - DeepSeek-reasoner / R1: 至少 16(推理预算)
	//   - 国内 OpenAI 兼容(mimo / qwen / hunyuan / 文心 等):普遍要求 ≥ 16
	//     否则 stop_reason=length 且 content 为空,触发"empty response"误报
	// 折衷:统一上调到 16。预检本身只是 8s 超时的轻量探测,16 token 不影响延迟。
	req.MaxTokens = 16

	resp, err := session.Provider.Complete(ctx, req)
	if err != nil {
		return fmt.Sprintf("Provider connectivity check failed: %v", err)
	}
	if resp == nil || len(resp.Content) == 0 {
		// 预检空响应不阻塞实际使用,只提示常见原因。
		return "Provider returned empty response on warm-up probe — 实际对话仍可使用。" +
			"若每次对话都空响应,请检查 base_url / model / api_key 是否正确;" +
			"国内 OpenAI 兼容服务(mimo/qwen/hunyuan 等)请确认 model 名拼写"
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
		MaxTokens: 16,
	}
}
