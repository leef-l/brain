package easymvp

import (
	"encoding/json"
	"testing"
)

func TestExtractContractKind(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"[contract:plan_review] review this plan", "plan_review"},
		{"[contract:plan_compile] compile", "plan_compile"},
		{"[contract:architect_chat] chat", "architect_chat"},
		{"no prefix", ""},
		{"[contract:missing_bracket", ""},
		{"", ""},
		{"[contract:  plan_review  ] extra", "plan_review"},
	}
	for _, c := range cases {
		got := extractContractKind(c.input)
		if got != c.want {
			t.Fatalf("extractContractKind(%q)=%q, want %q", c.input, got, c.want)
		}
	}
}

func TestIsValidJSON(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{`{"a":1}`, true},
		{`[1,2,3]`, true},
		{`"string"`, true},
		{`123`, true},
		{`true`, true},
		{`null`, true},
		{`{invalid}`, false},
		{``, false},
		{`{`, false},
	}
	for _, c := range cases {
		got := isValidJSON(c.input)
		if got != c.want {
			t.Fatalf("isValidJSON(%q)=%v, want %v", c.input, got, c.want)
		}
	}
}

func TestExtractBracketContent(t *testing.T) {
	cases := []struct {
		input string
		start int
		want  string
	}{
		{`{"a":1}`, 0, `{"a":1}`},
		{`[1,2,3]`, 0, `[1,2,3]`},
		{`text{"a":1}more`, 4, `{"a":1}`},
		{`text[1,2]more`, 4, `[1,2]`},
		{`{"a":"{\"nested\":1}"}`, 0, `{"a":"{\"nested\":1}"}`},
		{`{`, 0, ""},
		{`abc`, 0, ""},
		{``, 0, ""},
		{`{"a":1`, 0, ""},
	}
	for _, c := range cases {
		got := extractBracketContent(c.input, c.start)
		if got != c.want {
			t.Fatalf("extractBracketContent(%q, %d)=%q, want %q", c.input, c.start, got, c.want)
		}
	}
}

func TestExtractJSONFromText(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"Here is the result: {\"a\":1}", `{"a":1}`},
		{"Here is the result: [1,2]", `[1,2]`},
		{"no json here", "no json here"},
		{`{"a":"b"} extra`, `{"a":"b"}`},
	}
	for _, c := range cases {
		got := extractJSONFromText(c.input)
		if got != c.want {
			t.Fatalf("extractJSONFromText(%q)=%q, want %q", c.input, got, c.want)
		}
	}
}

func TestIsValidArchitectChatJSON(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{`{"reply":"hello","draft_tasks":[]}`, true},
		{`{"reply":"   "}`, false},
		{`{"reply":""}`, false},
		{`{"no_reply":"x"}`, false},
		{`invalid`, false},
		{`[]`, false},
	}
	for _, c := range cases {
		got := isValidArchitectChatJSON(c.input)
		if got != c.want {
			t.Fatalf("isValidArchitectChatJSON(%q)=%v, want %v", c.input, got, c.want)
		}
	}
}

func TestNormalizePlanReviewDecision(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{`{"decision":"approved"}`, `{"decision":"approved"}`},
		{`{"decision":"needs_rewrite"}`, `{"decision":"rejected"}`},
		{`{"decision":"rejected"}`, `{"decision":"rejected"}`},
		{`invalid json`, `invalid json`},
	}
	for _, c := range cases {
		got := normalizePlanReviewDecision(c.input)
		var gotObj, wantObj map[string]interface{}
		if err := json.Unmarshal([]byte(got), &gotObj); err != nil {
			if got != c.want {
				t.Fatalf("normalizePlanReviewDecision(%q)=%q, want %q", c.input, got, c.want)
			}
			continue
		}
		_ = json.Unmarshal([]byte(c.want), &wantObj)
		if gotObj["decision"] != wantObj["decision"] {
			t.Fatalf("normalizePlanReviewDecision(%q) decision=%v, want %v", c.input, gotObj["decision"], wantObj["decision"])
		}
	}
}

func TestBuildEnvelope(t *testing.T) {
	env := buildEnvelope("test_kind",
		[]map[string]interface{}{{"kind": "test", "id": "1", "version": 1}},
		"test summary",
		"success",
		map[string]interface{}{"data": "value"},
	)
	status, ok := env["status"].(string)
	if !ok || status != "ok" {
		t.Fatalf("expected status=ok, got %v", env["status"])
	}
	summary, ok := env["summary"].(string)
	if !ok || summary == "" {
		t.Fatalf("expected non-empty summary, got %v", env["summary"])
	}
	// Verify summary is valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(summary), &parsed); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	if parsed["result_kind"] != "test_kind" {
		t.Fatalf("expected result_kind=test_kind, got %v", parsed["result_kind"])
	}
}

func TestBuildEnvelopeWithRawMessage(t *testing.T) {
	raw := json.RawMessage(`{"key":"val"}`)
	env := buildEnvelope("raw_test", nil, "summary", "success", raw)
	summary := env["summary"].(string)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(summary), &parsed); err != nil {
		t.Fatalf("summary is not valid JSON: %v", err)
	}
	resultJSON, ok := parsed["result_json"]
	if !ok {
		t.Fatal("expected result_json in envelope")
	}
	_ = resultJSON
}

func TestBuildEnvelopeWithInvalidRawMessage(t *testing.T) {
	raw := json.RawMessage(`{invalid}`)
	env := buildEnvelope("invalid_raw", nil, "summary", "success", raw)
	summary := env["summary"].(string)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(summary), &parsed); err != nil {
		t.Fatalf("summary should still be valid JSON even with invalid raw: %v", err)
	}
	// Invalid raw message should be stored as JSON-encoded string
	_, ok := parsed["result_json"]
	if !ok {
		t.Fatal("expected result_json even for invalid raw")
	}
}
