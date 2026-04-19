package llm

import (
	"errors"
	"testing"

	brainerrors "github.com/leef-l/brain/sdk/errors"
)

func TestClassifyHTTPError_MapsTransientAndAuthCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{
			name: "rate limited",
			err:  classifyHTTPError("anthropic", 429, "rate_limit_error", "too many requests"),
			code: brainerrors.CodeLLMRateLimitedShortterm,
		},
		{
			name: "upstream 5xx",
			err:  classifyHTTPError("anthropic", 500, "runtime_error", "model engine error"),
			code: brainerrors.CodeLLMUpstream5xx,
		},
		{
			name: "auth failed",
			err:  classifyHTTPError("openai", 401, "authentication_error", "bad key"),
			code: brainerrors.CodeLLMAuthFailed,
		},
		{
			name: "context overflow",
			err:  classifyHTTPError("openai", 400, "invalid_request_error", "maximum context length exceeded"),
			code: brainerrors.CodeLLMContextOverflow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var be *brainerrors.BrainError
			if !errors.As(tc.err, &be) {
				t.Fatalf("error=%T, want *BrainError", tc.err)
			}
			if be.ErrorCode != tc.code {
				t.Fatalf("error_code=%q, want %q", be.ErrorCode, tc.code)
			}
		})
	}
}
