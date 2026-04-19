package llm

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

func classifyHTTPError(provider string, statusCode int, errType string, message string) error {
	errType = strings.TrimSpace(errType)
	message = strings.TrimSpace(message)

	text := provider + ": HTTP " + fmt.Sprintf("%d", statusCode)
	if errType != "" {
		text += ": " + errType
	}
	if message != "" {
		text += ": " + message
	}

	switch {
	case statusCode == http.StatusTooManyRequests:
		return brainerrors.New(brainerrors.CodeLLMRateLimitedShortterm,
			brainerrors.WithMessage(text))
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return brainerrors.New(brainerrors.CodeLLMAuthFailed,
			brainerrors.WithMessage(text))
	case statusCode >= 500 && statusCode <= 599:
		return brainerrors.New(brainerrors.CodeLLMUpstream5xx,
			brainerrors.WithMessage(text))
	case statusCode == http.StatusRequestEntityTooLarge || statusCode == http.StatusBadRequest && looksLikeContextOverflow(message):
		return brainerrors.New(brainerrors.CodeLLMContextOverflow,
			brainerrors.WithMessage(text))
	default:
		return errors.New(text)
	}
}

func looksLikeContextOverflow(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(msg, "context") && strings.Contains(msg, "limit") ||
		strings.Contains(msg, "context window") ||
		strings.Contains(msg, "maximum context length") ||
		strings.Contains(msg, "prompt is too long") ||
		strings.Contains(msg, "token limit")
}
