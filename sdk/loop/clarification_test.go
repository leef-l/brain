package loop

import (
	"strings"
	"testing"

	"github.com/leef-l/brain/sdk/llm"
)

func text(s string) []llm.ContentBlock {
	return []llm.ContentBlock{{Type: "text", Text: s}}
}

func thinking(s string) []llm.ContentBlock {
	return []llm.ContentBlock{{Type: "thinking", Text: s}}
}

func TestClarifier_Classify(t *testing.T) {
	c := &Clarifier{}

	cases := []struct {
		name string
		in   []llm.ContentBlock
		want ClarifyKind
	}{
		{"thinking only", thinking("let me think about this carefully ..."), KindThinkingOnly},
		{"english announcement", text("I'll write the game.html file now."), KindAnnouncementText},
		{"chinese announcement", text("我要现在创建一个 HTML 文件。"), KindAnnouncementText},
		{"english question", text("Which file would you like me to edit?"), KindQuestion},
		{"chinese question", text("请问你想要哪种风格?"), KindQuestion},
		{"generic refusal", text("This task seems out of scope for me."), KindGenericText},
		{"empty content", nil, KindGenericText},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.classify(tc.in)
			if got != tc.want {
				t.Fatalf("classify(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestClarifier_NextMessage_AttemptCap(t *testing.T) {
	c := &Clarifier{MaxAttempts: 2}
	state := &ClarifierState{}

	// First call — within budget.
	if _, ok := c.NextMessage(state, ClarifyContext{Content: text("I'll write it"), TurnIndex: 1}); !ok {
		t.Fatalf("expected first attempt to succeed")
	}
	if state.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", state.Attempts)
	}

	// Second call — within budget.
	if _, ok := c.NextMessage(state, ClarifyContext{Content: text("I'll write it"), TurnIndex: 2}); !ok {
		t.Fatalf("expected second attempt to succeed")
	}
	if state.Attempts != 2 {
		t.Fatalf("expected attempts=2, got %d", state.Attempts)
	}

	// Third call — over budget; should return ok=false and not bump attempts.
	if _, ok := c.NextMessage(state, ClarifyContext{Content: text("I'll write it"), TurnIndex: 3}); ok {
		t.Fatalf("expected third attempt to be denied")
	}
	if state.Attempts != 2 {
		t.Fatalf("expected attempts to stay at 2, got %d", state.Attempts)
	}
}

func TestClarifier_NextMessage_DefaultMaxAttempts(t *testing.T) {
	// Zero-value MaxAttempts should default to 2.
	c := &Clarifier{}
	state := &ClarifierState{}
	for i := 0; i < 2; i++ {
		if _, ok := c.NextMessage(state, ClarifyContext{Content: text("I'll write it"), TurnIndex: i + 1}); !ok {
			t.Fatalf("attempt %d should succeed under default MaxAttempts", i+1)
		}
	}
	if _, ok := c.NextMessage(state, ClarifyContext{Content: text("I'll write it"), TurnIndex: 3}); ok {
		t.Fatalf("attempt 3 should be denied under default MaxAttempts")
	}
}

func TestClarifier_ReasonerGrace_DoesNotConsumeBudget(t *testing.T) {
	c := &Clarifier{MaxAttempts: 1, Reasoner: true}
	state := &ClarifierState{}

	// Turn 1 + thinking-only → soft prompt; should NOT increment attempts.
	msg, ok := c.NextMessage(state, ClarifyContext{Content: thinking("planning..."), TurnIndex: 1})
	if !ok {
		t.Fatalf("reasoner grace turn should return a soft message")
	}
	if state.Attempts != 0 {
		t.Fatalf("grace turn must not consume attempts, got %d", state.Attempts)
	}
	if !strings.Contains(extractText(msg), "Plan looks ready") &&
		!strings.Contains(extractText(msg), "go ahead and act") {
		t.Fatalf("soft prompt body unexpected: %s", extractText(msg))
	}

	// Subsequent turn 2 + thinking-only → real attempt; consumes budget.
	if _, ok := c.NextMessage(state, ClarifyContext{Content: thinking("more planning..."), TurnIndex: 2}); !ok {
		t.Fatalf("turn-2 nudge should be allowed")
	}
	if state.Attempts != 1 {
		t.Fatalf("expected attempts=1 after real nudge, got %d", state.Attempts)
	}
}

func TestClarifier_ReasonerGrace_OnlyAppliesToThinkingOnly(t *testing.T) {
	c := &Clarifier{MaxAttempts: 1, Reasoner: true}
	state := &ClarifierState{}

	// Turn 1 + announcement text → does NOT get grace; counts as a real attempt.
	if _, ok := c.NextMessage(state, ClarifyContext{Content: text("I'll write it now"), TurnIndex: 1}); !ok {
		t.Fatalf("expected announcement-text to consume the only attempt")
	}
	if state.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", state.Attempts)
	}
}

func TestClarifier_ReasonerMessageIsShorter(t *testing.T) {
	long := &Clarifier{}
	short := &Clarifier{Reasoner: true}

	cc := ClarifyContext{Content: text("I'll write game.html"), TurnIndex: 2}
	stateA := &ClarifierState{}
	stateB := &ClarifierState{}

	mLong, _ := long.NextMessage(stateA, cc)
	mShort, _ := short.NextMessage(stateB, cc)

	if len(extractText(mShort)) >= len(extractText(mLong)) {
		t.Fatalf("reasoner message should be shorter; reasoner=%d non-reasoner=%d",
			len(extractText(mShort)), len(extractText(mLong)))
	}
}

func TestClarifier_NilSafe(t *testing.T) {
	var c *Clarifier
	if _, ok := c.NextMessage(&ClarifierState{}, ClarifyContext{}); ok {
		t.Fatalf("nil Clarifier must return ok=false")
	}
}

func TestLooksLikeQuestion(t *testing.T) {
	yes := []string{
		"do you want me to use bun?",
		"could you provide more detail",
		"请问你想要哪种风格",
	}
	no := []string{
		"I'll write the file now.",
		"Done with the implementation.",
	}
	for _, s := range yes {
		if !looksLikeQuestion(strings.ToLower(s)) {
			t.Errorf("expected question-like: %q", s)
		}
	}
	for _, s := range no {
		if looksLikeQuestion(strings.ToLower(s)) {
			t.Errorf("expected not question-like: %q", s)
		}
	}
}

func extractText(m llm.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}
