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

func TestIsValidRequirementAnalysisJSON(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{`{"requirement_doc":{"title":"Test"},"summary":"ok","suggested_next_action":"confirm_requirement"}`, true},
		{`{"requirement_doc":{"title":"  "},"summary":"ok"}`, false},
		{`{"requirement_doc":{"title":""},"summary":"ok"}`, false},
		{`{"requirement_doc":{},"summary":"ok"}`, false},
		{`{"summary":"ok"}`, false},
		{`invalid`, false},
		{`[]`, false},
	}
	for _, c := range cases {
		got := isValidRequirementAnalysisJSON(c.input)
		if got != c.want {
			t.Fatalf("isValidRequirementAnalysisJSON(%q)=%v, want %v", c.input, got, c.want)
		}
	}
}

func TestIsValidSolutionDesignJSON(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{`{"architecture":"monolith","summary":"basic design"}`, true},
		{`{"architecture":"","summary":"basic design"}`, false},
		{`{"architecture":"monolith","summary":""}`, false},
		{`{"architecture":"monolith"}`, false},
		{`{"summary":"ok"}`, false},
		{`invalid`, false},
	}
	for _, c := range cases {
		got := isValidSolutionDesignJSON(c.input)
		if got != c.want {
			t.Fatalf("isValidSolutionDesignJSON(%q)=%v, want %v", c.input, got, c.want)
		}
	}
}

func TestIsValidDesignReviewJSON(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{`{"review_result_id":"rev_1","passed":true,"score":85,"dimensions":[],"issues":[],"suggestions":[]}`, true},
		{`{"review_result_id":"rev_1","passed":false,"score":0}`, true},
		{`{"review_result_id":"","score":80}`, false},
		{`{"review_result_id":"rev_1"}`, false},
		{`{"score":80}`, false},
		{`invalid`, false},
	}
	for _, c := range cases {
		got := isValidDesignReviewJSON(c.input)
		if got != c.want {
			t.Fatalf("isValidDesignReviewJSON(%q)=%v, want %v", c.input, got, c.want)
		}
	}
}

func TestIsValidDesignFixJSON(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{`{"fix_result_id":"fix_1","architecture":"updated","changes_summary":"fixed","fixed_issues":["ISS-1"]}`, true},
		{`{"fix_result_id":"fix_1"}`, true},
		{`{"fix_result_id":""}`, false},
		{`{"fix_result_id":"   "}`, false},
		{`{}`, false},
		{`invalid`, false},
	}
	for _, c := range cases {
		got := isValidDesignFixJSON(c.input)
		if got != c.want {
			t.Fatalf("isValidDesignFixJSON(%q)=%v, want %v", c.input, got, c.want)
		}
	}
}

func TestExtractContractKindNewContracts(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"[contract:requirement_analysis] analyze requirements", "requirement_analysis"},
		{"[contract:solution_design] design solution", "solution_design"},
		{"[contract:design_review] review design", "design_review"},
		{"[contract:design_fix] fix design", "design_fix"},
	}
	for _, c := range cases {
		got := extractContractKind(c.input)
		if got != c.want {
			t.Fatalf("extractContractKind(%q)=%q, want %q", c.input, got, c.want)
		}
	}
}
