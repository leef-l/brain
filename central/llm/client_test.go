package llm

import (
	"testing"
)

func TestStripCodeFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain json",
			input: `{"approved": true}`,
			want:  `{"approved": true}`,
		},
		{
			name:  "fenced json",
			input: "```json\n{\"approved\": true}\n```",
			want:  `{"approved": true}`,
		},
		{
			name:  "fenced no lang",
			input: "```\n{\"approved\": true}\n```",
			want:  `{"approved": true}`,
		},
		{
			name:  "with trailing newline",
			input: "```json\n{\"approved\": true}\n\n```\n",
			want:  `{"approved": true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripCodeFences(tt.input)
			if got != tt.want {
				t.Errorf("stripCodeFences() = %q, want %q", got, tt.want)
			}
		})
	}
}
