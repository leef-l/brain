// Package llm provides a vendor-neutral LLM chat client using the
// OpenAI-compatible API format. This works with:
//   - DeepSeek V3.2 (recommended for cost/quality)
//   - Claude via proxy (OpenAI-compatible endpoints)
//   - Tencent HunYuan (混元)
//   - Any OpenAI-compatible API
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Config configures the LLM client.
type Config struct {
	// BaseURL is the API endpoint (e.g. "https://api.deepseek.com/v1").
	BaseURL string

	// APIKey is the authentication key.
	APIKey string

	// Model is the model name (e.g. "deepseek-chat", "claude-haiku-4-5-20251001").
	Model string

	// MaxTokens limits the response length. Default: 500.
	MaxTokens int

	// Temperature controls randomness. Default: 0.3 (conservative for trade review).
	Temperature float64

	// Timeout is the HTTP request timeout. Default: 15s.
	Timeout time.Duration
}

// DefaultConfig returns a config suitable for DeepSeek V3.2.
func DefaultConfig() Config {
	return Config{
		BaseURL:     "https://api.deepseek.com/v1",
		Model:       "deepseek-chat",
		MaxTokens:   500,
		Temperature: 0.3,
		Timeout:     15 * time.Second,
	}
}

// Message is a chat message.
type Message struct {
	Role    string `json:"role"`    // "system", "user", "assistant"
	Content string `json:"content"`
}

// Client is a vendor-neutral LLM client using OpenAI-compatible chat API.
type Client struct {
	config Config
	client *http.Client
}

// New creates an LLM client.
func New(cfg Config) *Client {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 500
	}
	if cfg.Temperature <= 0 {
		cfg.Temperature = 0.3
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &Client{
		config: cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// chatRequest is the OpenAI-compatible request body.
type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
}

// chatResponse is the OpenAI-compatible response body.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Chat sends a chat completion request and returns the assistant's response.
func (c *Client) Chat(ctx context.Context, messages []Message) (string, error) {
	reqBody := chatRequest{
		Model:       c.config.Model,
		Messages:    messages,
		MaxTokens:   c.config.MaxTokens,
		Temperature: c.config.Temperature,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := c.config.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if chatResp.Error != nil {
		return "", fmt.Errorf("api error: %s (%s)", chatResp.Error.Message, chatResp.Error.Type)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return chatResp.Choices[0].Message.Content, nil
}

// ChatJSON sends a chat request and parses the JSON response into the target.
// It handles markdown code fences (```json ... ```) that some models wrap output in.
func (c *Client) ChatJSON(ctx context.Context, messages []Message, target interface{}) error {
	content, err := c.Chat(ctx, messages)
	if err != nil {
		return err
	}

	// Strip markdown code fences if present
	cleaned := stripCodeFences(content)

	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		return fmt.Errorf("parse JSON from LLM: %w (raw: %s)", err, content)
	}
	return nil
}

// stripCodeFences removes ```json ... ``` wrapping that LLMs often add.
func stripCodeFences(s string) string {
	// Find opening fence
	start := 0
	for i := 0; i < len(s)-3; i++ {
		if s[i] == '`' && s[i+1] == '`' && s[i+2] == '`' {
			// Skip past the fence line
			for j := i + 3; j < len(s); j++ {
				if s[j] == '\n' {
					start = j + 1
					break
				}
			}
			break
		}
	}

	// Find closing fence
	end := len(s)
	for i := len(s) - 1; i >= start+3; i-- {
		if i >= 2 && s[i] == '`' && s[i-1] == '`' && s[i-2] == '`' {
			// Trim back to before the closing fence
			end = i - 2
			for end > start && s[end-1] == '\n' {
				end--
			}
			break
		}
	}

	if start > 0 || end < len(s) {
		return s[start:end]
	}
	return s
}
