package chat

import "testing"

func TestShouldPrintAssistantReply(t *testing.T) {
	tests := []struct {
		name            string
		streamedContent string
		replyText       string
		want            bool
	}{
		{
			name:      "prints final reply when nothing was streamed",
			replyText: "最终回复",
			want:      true,
		},
		{
			name:            "skips duplicate final reply after streaming",
			streamedContent: "我将为您打开浏览器并执行登录操作。",
			replyText:       "我将为您打开浏览器并执行登录操作。",
			want:            false,
		},
		{
			name:            "skips final print even when streamed content has extra whitespace",
			streamedContent: "\n已流式输出\n",
			replyText:       "已流式输出",
			want:            false,
		},
		{
			name:      "ignores empty final reply",
			replyText: "   ",
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPrintAssistantReply(tc.streamedContent, tc.replyText); got != tc.want {
				t.Fatalf("shouldPrintAssistantReply(%q, %q) = %v, want %v", tc.streamedContent, tc.replyText, got, tc.want)
			}
		})
	}
}
