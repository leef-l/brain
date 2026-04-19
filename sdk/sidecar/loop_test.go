package sidecar

import (
	"encoding/json"
	"strings"
	"testing"

	brainerrors "github.com/leef-l/brain/sdk/errors"
	"github.com/leef-l/brain/sdk/llm"
	"github.com/leef-l/brain/sdk/loop"
)

func TestApplyStructuredOutputs_CollectsArtifactsVerificationAndFault(t *testing.T) {
	out := &ExecuteResult{Status: "failed", Error: "human_intervention:aborted"}
	runResult := &loop.RunResult{
		FinalMessages: []llm.Message{
			{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "tool_use", ToolUseID: "tool-1", ToolName: "browser.screenshot", Input: json.RawMessage(`{}`)},
					{Type: "tool_use", ToolUseID: "tool-24", ToolName: "browser.open", Input: json.RawMessage(`{"url":"https://example.com/login"}`)},
					{Type: "tool_use", ToolUseID: "tool-2", ToolName: "browser.pattern_exec", Input: json.RawMessage(`{"pattern_id":"login_step1"}`)},
					{Type: "tool_use", ToolUseID: "tool-3", ToolName: "browser.downloads", Input: json.RawMessage(`{"action":"wait"}`)},
					{Type: "tool_use", ToolUseID: "tool-4", ToolName: "browser.storage", Input: json.RawMessage(`{"action":"export"}`)},
					{Type: "tool_use", ToolUseID: "tool-5", ToolName: "browser.upload_file", Input: json.RawMessage(`{"selector":"input[type=file]"}`)},
					{Type: "tool_use", ToolUseID: "tool-19", ToolName: "browser.check_anomaly_v2", Input: json.RawMessage(`{"mode":"passive"}`)},
					{Type: "tool_use", ToolUseID: "tool-6", ToolName: "verifier.check_output", Input: json.RawMessage(`{"mode":"exact"}`)},
					{Type: "tool_use", ToolUseID: "tool-7", ToolName: "code.write_file", Input: json.RawMessage(`{"path":"/tmp/out.txt"}`)},
					{Type: "tool_use", ToolUseID: "tool-8", ToolName: "code.edit_file", Input: json.RawMessage(`{"path":"/tmp/out.txt"}`)},
					{Type: "tool_use", ToolUseID: "tool-9", ToolName: "browser.frame", Input: json.RawMessage(`{"action":"snapshot","index":0}`)},
					{Type: "tool_use", ToolUseID: "tool-10", ToolName: "browser.network", Input: json.RawMessage(`{"action":"get","id":"req-1","with_body":true}`)},
					{Type: "tool_use", ToolUseID: "tool-11", ToolName: "browser.snapshot", Input: json.RawMessage(`{"mode":"interactive"}`)},
					{Type: "tool_use", ToolUseID: "tool-12", ToolName: "browser.understand", Input: json.RawMessage(`{"max_elements":10}`)},
					{Type: "tool_use", ToolUseID: "tool-13", ToolName: "browser.sitemap", Input: json.RawMessage(`{"start_url":"https://example.com"}`)},
					{Type: "tool_use", ToolUseID: "tool-14", ToolName: "verifier.read_file", Input: json.RawMessage(`{"path":"/tmp/out.txt"}`)},
					{Type: "tool_use", ToolUseID: "tool-15", ToolName: "browser.pattern_match", Input: json.RawMessage(`{"category":"auth"}`)},
					{Type: "tool_use", ToolUseID: "tool-16", ToolName: "browser.changes", Input: json.RawMessage(`{"limit":10}`)},
					{Type: "tool_use", ToolUseID: "tool-17", ToolName: "browser.request_anomaly_fix", Input: json.RawMessage(`{"anomaly":{"type":"captcha"}}`)},
					{Type: "tool_use", ToolUseID: "tool-18", ToolName: "browser.pattern_list", Input: json.RawMessage(`{"category":"auth"}`)},
					{Type: "tool_use", ToolUseID: "tool-20", ToolName: "browser.network", Input: json.RawMessage(`{"action":"list","url_pattern":"orders"}`)},
					{Type: "tool_use", ToolUseID: "tool-21", ToolName: "browser.navigate", Input: json.RawMessage(`{"action":"list_tabs"}`)},
					{Type: "tool_use", ToolUseID: "tool-22", ToolName: "browser.iframe", Input: json.RawMessage(`{"action":"list"}`)},
					{Type: "tool_use", ToolUseID: "tool-23", ToolName: "verifier.browser_action", Input: json.RawMessage(`{"action":"screenshot","params":{"full_page":false}}`)},
					{Type: "tool_use", ToolUseID: "tool-25", ToolName: "browser.select", Input: json.RawMessage(`{"selector":"select[name=status]","value":"paid"}`)},
					{Type: "tool_use", ToolUseID: "tool-26", ToolName: "code.list_files", Input: json.RawMessage(`{"pattern":"**/*.go"}`)},
					{Type: "tool_use", ToolUseID: "tool-27", ToolName: "code.search", Input: json.RawMessage(`{"pattern":"collectArtifacts","path":"sdk/sidecar"}`)},
				},
			},
			{
				Role: "user",
				Content: []llm.ContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: "tool-1",
						Output:    json.RawMessage(`{"status":"ok","format":"png","data":"abc123","encoding":"base64"}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-24",
						Output:    json.RawMessage(`{"status":"ok","url":"https://example.com/login","target_id":"page-1"}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-2",
						IsError:   true,
						Output: json.RawMessage(`{
							"pattern_id":"login_step1",
							"success":false,
							"post_conditions":[{"type":"dom_contains","ok":false,"reason":"#dashboard absent"}],
							"error":"human_intervention:aborted",
							"aborted_by_anomaly":"captcha"
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-3",
						Output: json.RawMessage(`{
							"action":"wait",
							"directory":"/tmp/downloads",
							"count":1,
							"files":[{"name":"report.csv","path":"/tmp/downloads/report.csv","size":321,"mtime_ms":1710000000000}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-4",
						Output:    json.RawMessage(`{"action":"export","written":"/tmp/state.json","state":{"cookies":[]}}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-5",
						Output:    json.RawMessage(`{"status":"ok","files":["/tmp/input.csv"]}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-19",
						Output: json.RawMessage(`{
							"page_health":"degraded",
							"anomalies":[{"type":"captcha","subtype":"hcaptcha","severity":"high"}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-6",
						IsError:   true,
						Output:    json.RawMessage(`{"match":false,"mode":"exact","diff":"expected: \"ok\"\nactual:   \"fail\""}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-7",
						Output:    json.RawMessage(`{"bytes_written":42,"path":"/tmp/out.txt"}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-8",
						Output:    json.RawMessage(`{"path":"/tmp/out.txt","replacements":1,"bytes_written":45}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-9",
						Output: json.RawMessage(`{
							"action":"snapshot",
							"frame":{"index":0,"url":"https://pay.example/frame","name":"pay","id":"stripe","w":400,"h":300,"visible":true},
							"elements":[{"id":1,"role":"button","name":"Pay"}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-10",
						Output: json.RawMessage(`{
							"action":"get",
							"entry":{"id":"req-1","url":"https://api.example/orders","status":200},
							"body":"{\"ok\":true}",
							"body_mime":"application/json"
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-11",
						Output: json.RawMessage(`{
							"count":1,
							"total":1,
							"mode":"interactive",
							"url":"https://example.com/login",
							"title":"Login",
							"elements":[{"id":1,"role":"button","name":"Submit"}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-12",
						Output: json.RawMessage(`{
							"url_pattern":"https://example.com/login",
							"dom_hash":"abc",
							"semantic_quality":"full",
							"elements":[{"id":1,"action_intent":"submit_login","risk_level":"safe_caution"}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-13",
						Output: json.RawMessage(`{
							"start_url":"https://example.com",
							"pages_visited":2,
							"pages":["https://example.com","https://example.com/login"],
							"route_patterns":["/","/login"]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-14",
						Output: json.RawMessage(`{
							"content":"ok",
							"lines":1,
							"total_lines":1,
							"truncated":false,
							"path":"/tmp/out.txt"
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-15",
						Output: json.RawMessage(`{
							"url":"https://example.com/login",
							"count":1,
							"matches":[{"pattern_id":"login_username_password","score":0.91}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-16",
						Output: json.RawMessage(`{
							"initialized":true,
							"count":1,
							"records":[{"type":"childList","target":"div","ts":1710000000000}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-17",
						Output: json.RawMessage(`{
							"source":"llm",
							"rationale":"captcha loop detected",
							"recovery":[{"kind":"human_intervention","reason":"solve captcha"}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-18",
						Output: json.RawMessage(`{
							"count":1,
							"patterns":[{"id":"login_username_password","category":"auth","enabled":true}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-20",
						Output: json.RawMessage(`{
							"action":"list",
							"count":1,
							"inflight":0,
							"entries":[{"id":"req-1","url":"https://api.example/orders","status":200}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-21",
						Output: json.RawMessage(`[
							{"target_id":"tab-1","title":"Home","url":"https://example.com/"},
							{"target_id":"tab-2","title":"Login","url":"https://example.com/login"}
						]`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-22",
						Output: json.RawMessage(`{
							"action":"list",
							"count":1,
							"frames":[{"index":0,"url":"https://pay.example/frame","name":"pay","id":"stripe","w":400,"h":300,"visible":true}]
						}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-23",
						Output:    json.RawMessage(`{"status":"ok","format":"png","data":"proxy-shot","encoding":"base64"}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-25",
						Output:    json.RawMessage(`{"status":"ok","value":"paid"}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-26",
						Output:    json.RawMessage(`{"paths":["sdk/sidecar/loop.go","sdk/sidecar/loop_test.go"],"count":2,"truncated":false}`),
					},
					{
						Type:      "tool_result",
						ToolUseID: "tool-27",
						Output:    json.RawMessage(`{"matches":[{"file":"sdk/sidecar/loop.go","line":415,"text":"func collectArtifacts(results []toolResultSnapshot) []ArtifactRef {"}],"total":1,"truncated":false,"backend":"ripgrep"}`),
					},
				},
			},
		},
	}

	applyStructuredOutputs(out, runResult)

	if len(out.Artifacts) != 27 {
		t.Fatalf("artifacts len=%d, want 27", len(out.Artifacts))
	}
	if out.Artifacts[0].Kind != "screenshot" || out.Artifacts[0].Tool != "browser.screenshot" {
		t.Fatalf("artifact=%+v, want screenshot from browser.screenshot", out.Artifacts[0])
	}
	wantArtifacts := []ArtifactRef{
		{Kind: "download", Tool: "browser.downloads", Locator: "/tmp/downloads/report.csv"},
		{Kind: "page_ref", Tool: "browser.open", Locator: "page-1"},
		{Kind: "storage_state", Tool: "browser.storage", Locator: "/tmp/state.json"},
		{Kind: "upload_file", Tool: "browser.upload_file", Locator: "/tmp/input.csv"},
		{Kind: "diff", Tool: "verifier.check_output", Locator: "diff"},
		{Kind: "file", Tool: "code.write_file", Locator: "/tmp/out.txt"},
		{Kind: "file", Tool: "code.edit_file", Locator: "/tmp/out.txt"},
		{Kind: "frame_snapshot", Tool: "browser.frame", Locator: "elements"},
		{Kind: "response_body", Tool: "browser.network", Locator: "body"},
		{Kind: "snapshot", Tool: "browser.snapshot", Locator: "elements"},
		{Kind: "semantic_annotations", Tool: "browser.understand", Locator: "elements"},
		{Kind: "sitemap", Tool: "browser.sitemap", Locator: "pages"},
		{Kind: "route_patterns", Tool: "browser.sitemap", Locator: "route_patterns"},
		{Kind: "file_content", Tool: "verifier.read_file", Locator: "/tmp/out.txt"},
		{Kind: "pattern_matches", Tool: "browser.pattern_match", Locator: "matches"},
		{Kind: "dom_changes", Tool: "browser.changes", Locator: "records"},
		{Kind: "recovery_plan", Tool: "browser.request_anomaly_fix", Locator: "recovery"},
		{Kind: "pattern_catalog", Tool: "browser.pattern_list", Locator: "patterns"},
		{Kind: "anomaly_report", Tool: "browser.check_anomaly_v2", Locator: "anomalies"},
		{Kind: "network_trace", Tool: "browser.network", Locator: "entries"},
		{Kind: "network_entry", Tool: "browser.network", Locator: "entry"},
		{Kind: "tab_catalog", Tool: "browser.navigate", Locator: "tabs"},
		{Kind: "frame_catalog", Tool: "browser.iframe", Locator: "frames"},
		{Kind: "screenshot", Tool: "verifier.browser_action", Locator: "data"},
		{Kind: "file_list", Tool: "code.list_files", Locator: "paths"},
		{Kind: "search_matches", Tool: "code.search", Locator: "matches"},
	}
	for _, want := range wantArtifacts {
		if !hasArtifact(out.Artifacts, want.Kind, want.Tool, want.Locator) {
			t.Fatalf("artifacts=%+v missing kind=%q tool=%q locator=%q", out.Artifacts, want.Kind, want.Tool, want.Locator)
		}
	}
	if out.Verification == nil {
		t.Fatal("verification=nil, want structured verification")
	}
	if out.Verification.SourceTool != "browser.pattern_exec,browser.upload_file,browser.check_anomaly_v2,verifier.check_output,browser.select" {
		t.Fatalf("source_tool=%q, want browser.pattern_exec,browser.upload_file,browser.check_anomaly_v2,verifier.check_output,browser.select", out.Verification.SourceTool)
	}
	if out.Verification.PatternID != "login_step1" {
		t.Fatalf("pattern_id=%q, want login_step1", out.Verification.PatternID)
	}
	if out.Verification.Passed == nil || *out.Verification.Passed {
		t.Fatalf("verification passed=%v, want false", out.Verification.Passed)
	}
	if len(out.Verification.Checks) != 7 {
		t.Fatalf("checks=%+v, want 7 checks", out.Verification.Checks)
	}
	if !hasVerificationCheck(out.Verification.Checks, "dom_contains", false) {
		t.Fatalf("checks=%+v, want failed dom_contains check", out.Verification.Checks)
	}
	if !hasVerificationCheck(out.Verification.Checks, "browser.upload_file.status", true) {
		t.Fatalf("checks=%+v, want passed upload_file.status", out.Verification.Checks)
	}
	if !hasVerificationCheck(out.Verification.Checks, "browser.upload_file.files_attached", true) {
		t.Fatalf("checks=%+v, want passed upload_file.files_attached", out.Verification.Checks)
	}
	if !hasVerificationCheck(out.Verification.Checks, "verifier.check_output.match", false) {
		t.Fatalf("checks=%+v, want failed verifier.check_output.match", out.Verification.Checks)
	}
	if !hasVerificationCheck(out.Verification.Checks, "browser.check_anomaly_v2.page_health", false) {
		t.Fatalf("checks=%+v, want failed anomaly page_health", out.Verification.Checks)
	}
	if !hasVerificationCheck(out.Verification.Checks, "browser.check_anomaly_v2.anomaly:captcha/hcaptcha", false) {
		t.Fatalf("checks=%+v, want failed anomaly detail", out.Verification.Checks)
	}
	if !hasVerificationCheck(out.Verification.Checks, "browser.select.applied", true) {
		t.Fatalf("checks=%+v, want passed browser.select.applied", out.Verification.Checks)
	}
	if out.FaultSummary == nil {
		t.Fatal("fault_summary=nil, want normalized failure")
	}
	switch out.FaultSummary.Tool {
	case "verifier.check_output":
		if out.FaultSummary.Route != "verification_failed" {
			t.Fatalf("fault route=%q, want verification_failed for verifier.check_output", out.FaultSummary.Route)
		}
		if out.FaultSummary.Code != "verification_failed" {
			t.Fatalf("fault code=%q, want verification_failed for verifier.check_output", out.FaultSummary.Code)
		}
		if out.FaultSummary.PageHealth != "" {
			t.Fatalf("fault page_health=%q, want empty for verifier fault", out.FaultSummary.PageHealth)
		}
	case "browser.check_anomaly_v2":
		if out.FaultSummary.Route != "anomaly_detected" {
			t.Fatalf("fault route=%q, want anomaly_detected for browser.check_anomaly_v2", out.FaultSummary.Route)
		}
		if out.FaultSummary.Code != "anomaly_detected" {
			t.Fatalf("fault code=%q, want anomaly_detected for browser.check_anomaly_v2", out.FaultSummary.Code)
		}
		if out.FaultSummary.PageHealth != "degraded" {
			t.Fatalf("fault page_health=%q, want degraded", out.FaultSummary.PageHealth)
		}
	default:
		t.Fatalf("fault tool=%q, want verifier.check_output or browser.check_anomaly_v2", out.FaultSummary.Tool)
	}
}

func TestCollectToolResults_OrphanAndDuplicateToolUseHandling(t *testing.T) {
	results := collectToolResults([]llm.Message{
		{
			Role: "assistant",
			Content: []llm.ContentBlock{
				{Type: "tool_use", ToolUseID: "dup-1", ToolName: "browser.snapshot", Input: json.RawMessage(`{"mode":"interactive"}`)},
				{Type: "tool_use", ToolUseID: "dup-1", ToolName: "browser.screenshot", Input: json.RawMessage(`{"full_page":true}`)},
				{Type: "tool_use", ToolUseID: "empty-1", ToolName: "", Input: json.RawMessage(`{"ignored":true}`)},
			},
		},
		{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: "tool_result", ToolUseID: "dup-1", Output: json.RawMessage(`{"elements":[]}`)},
				{Type: "tool_result", ToolUseID: "missing-1", Output: json.RawMessage(`{"status":"ok"}`), IsError: true},
				{Type: "tool_result", Output: json.RawMessage(`{"status":"ok"}`), IsError: true},
				{Type: "tool_result", ToolUseID: "empty-1", Output: json.RawMessage(`{"status":"ok"}`), IsError: true},
			},
		},
	})

	if len(results) != 6 {
		t.Fatalf("results len=%d, want 6", len(results))
	}

	if results[0].CollectCode != "duplicate_tool_use_id" {
		t.Fatalf("results[0].CollectCode=%q, want duplicate_tool_use_id", results[0].CollectCode)
	}
	if results[1].CollectCode != "empty_tool_name" {
		t.Fatalf("results[1].CollectCode=%q, want empty_tool_name", results[1].CollectCode)
	}

	if results[2].ToolName != "browser.snapshot" {
		t.Fatalf("results[2].ToolName=%q, want first tool_use binding preserved", results[2].ToolName)
	}
	if string(results[2].ToolInput) != `{"mode":"interactive"}` {
		t.Fatalf("results[2].ToolInput=%s, want first tool_use input", results[2].ToolInput)
	}

	if results[3].CollectCode != "orphan_tool_result" || results[3].CollectError != "tool_result references unknown tool_use_id: missing-1" {
		t.Fatalf("results[3]=%+v, want unknown tool_use_id orphan", results[3])
	}
	if results[4].CollectCode != "orphan_tool_result" || results[4].CollectError != "tool_result missing tool_use_id" {
		t.Fatalf("results[4]=%+v, want missing tool_use_id orphan", results[4])
	}
	if results[5].CollectCode != "orphan_tool_result" || results[5].CollectError != "tool_result references unknown tool_use_id: empty-1" {
		t.Fatalf("results[5]=%+v, want empty-name binding to remain unresolved", results[5])
	}
}

func TestApplyStructuredOutputs_ProtocolMismatchFaultWinsWhenPresent(t *testing.T) {
	out := &ExecuteResult{Status: "failed"}
	runResult := &loop.RunResult{
		FinalMessages: []llm.Message{
			{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "tool_use", ToolUseID: "tool-1", ToolName: "browser.snapshot", Input: json.RawMessage(`{"mode":"interactive"}`)},
					{Type: "tool_use", ToolUseID: "tool-1", ToolName: "browser.screenshot", Input: json.RawMessage(`{"full_page":true}`)},
				},
			},
			{
				Role: "user",
				Content: []llm.ContentBlock{
					{Type: "tool_result", ToolUseID: "tool-1", Output: json.RawMessage(`{"elements":[]}`)},
				},
			},
		},
	}

	applyStructuredOutputs(out, runResult)

	if out.FaultSummary == nil {
		t.Fatal("fault_summary=nil, want sidecar protocol mismatch")
	}
	if out.FaultSummary.Source != "sidecar" {
		t.Fatalf("fault_summary.source=%q, want sidecar", out.FaultSummary.Source)
	}
	if out.FaultSummary.Code != "duplicate_tool_use_id" {
		t.Fatalf("fault_summary.code=%q, want duplicate_tool_use_id", out.FaultSummary.Code)
	}
	if out.FaultSummary.Route != "protocol_mismatch" {
		t.Fatalf("fault_summary.route=%q, want protocol_mismatch", out.FaultSummary.Route)
	}
}

func TestCollectArtifacts_IgnoresNonArtifactLikeToolResults(t *testing.T) {
	artifacts := collectArtifacts([]toolResultSnapshot{
		{
			ToolName: "browser.storage",
			Output:   json.RawMessage(`{"action":"import","state":{"cookies":[]}}`),
		},
		{
			ToolName: "browser.downloads",
			Output:   json.RawMessage(`{"action":"list","files":[{"name":"partial.csv"}]}`),
		},
		{
			ToolName: "verifier.check_output",
			Output:   json.RawMessage(`{"match":true,"mode":"exact"}`),
		},
		{
			ToolName: "code.delete_file",
			Output:   json.RawMessage(`{"deleted":true,"path":"/tmp/gone.txt"}`),
		},
		{
			ToolName: "browser.frame",
			Output:   json.RawMessage(`{"action":"list","count":1,"frames":[{"index":0,"url":"https://pay.example"}]}`),
		},
		{
			ToolName: "browser.storage",
			Output:   json.RawMessage(`{"action":"import"}`),
		},
		{
			ToolName: "browser.storage",
			Output:   json.RawMessage(`{"action":"clear"}`),
		},
		{
			ToolName: "browser.snapshot",
			Output:   json.RawMessage(`{"count":0,"total":0,"mode":"interactive"}`),
		},
		{
			ToolName: "browser.sitemap",
			Output:   json.RawMessage(`{"start_url":"https://example.com","pages_visited":0}`),
		},
		{
			ToolName: "verifier.read_file",
			Output:   json.RawMessage(`{"content":"ok","lines":1,"total_lines":1,"truncated":false}`),
		},
		{
			ToolName: "browser.pattern_match",
			Output:   json.RawMessage(`{"url":"https://example.com/login","count":0}`),
		},
		{
			ToolName: "browser.changes",
			Output:   json.RawMessage(`{"initialized":true,"count":0}`),
		},
		{
			ToolName: "browser.request_anomaly_fix",
			Output:   json.RawMessage(`{"source":"fallback","rationale":"none"}`),
		},
		{
			ToolName: "verifier.run_tests",
			Output:   json.RawMessage(`{"stdout":"ok","stderr":"","exit_code":0,"passed":true,"timed_out":false}`),
		},
	})
	if len(artifacts) != 0 {
		t.Fatalf("artifacts=%+v, want none", artifacts)
	}
}

func TestCollectArtifacts_CollectsFrameSnapshotAndNetworkBody(t *testing.T) {
	artifacts := collectArtifacts([]toolResultSnapshot{
		{
			ToolName: "browser.snapshot",
			Output: json.RawMessage(`{
				"count":1,
				"total":1,
				"mode":"interactive",
				"url":"https://example.com",
				"elements":[{"id":1,"role":"link","name":"Home"}]
			}`),
		},
		{
			ToolName: "browser.understand",
			Output: json.RawMessage(`{
				"url_pattern":"https://example.com",
				"dom_hash":"abc",
				"semantic_quality":"full",
				"elements":[{"id":1,"action_intent":"navigate_home","risk_level":"safe"}]
			}`),
		},
		{
			ToolName: "browser.sitemap",
			Output: json.RawMessage(`{
				"start_url":"https://example.com",
				"pages_visited":2,
				"pages":["https://example.com","https://example.com/login"]
			}`),
		},
		{
			ToolName: "code.read_file",
			Output: json.RawMessage(`{
				"content":"hello",
				"lines":1,
				"total_lines":1,
				"truncated":false,
				"path":"/tmp/demo.txt"
			}`),
		},
		{
			ToolName: "browser.pattern_match",
			Output: json.RawMessage(`{
				"url":"https://example.com/login",
				"count":1,
				"matches":[{"pattern_id":"login_username_password","score":0.95}]
			}`),
		},
		{
			ToolName: "browser.changes",
			Output: json.RawMessage(`{
				"initialized":true,
				"count":2,
				"records":[{"type":"childList"},{"type":"attributes"}]
			}`),
		},
		{
			ToolName: "browser.request_anomaly_fix",
			Output: json.RawMessage(`{
				"source":"llm",
				"recovery":[{"kind":"retry"},{"kind":"human_intervention"}]
			}`),
		},
		{
			ToolName: "browser.frame",
			Output: json.RawMessage(`{
				"action":"snapshot",
				"frame":{"index":0,"url":"https://pay.example/frame"},
				"elements":[{"id":1,"role":"button","name":"Pay"}]
			}`),
		},
		{
			ToolName: "browser.network",
			Output: json.RawMessage(`{
				"action":"wait_for",
				"entry":{"id":"req-9","url":"https://api.example/orders","status":200},
				"body":"eyJvayI6dHJ1ZX0=",
				"body_mime":"application/octet-stream;base64"
			}`),
		},
	})

	if !hasArtifact(artifacts, "snapshot", "browser.snapshot", "elements") {
		t.Fatalf("artifacts=%+v, want browser snapshot artifact", artifacts)
	}
	if !hasArtifact(artifacts, "semantic_annotations", "browser.understand", "elements") {
		t.Fatalf("artifacts=%+v, want browser understand semantic artifact", artifacts)
	}
	if !hasArtifact(artifacts, "sitemap", "browser.sitemap", "pages") {
		t.Fatalf("artifacts=%+v, want sitemap artifact", artifacts)
	}
	if !hasArtifact(artifacts, "file_content", "code.read_file", "/tmp/demo.txt") {
		t.Fatalf("artifacts=%+v, want file_content artifact", artifacts)
	}
	if !hasArtifact(artifacts, "pattern_matches", "browser.pattern_match", "matches") {
		t.Fatalf("artifacts=%+v, want pattern_matches artifact", artifacts)
	}
	if !hasArtifact(artifacts, "dom_changes", "browser.changes", "records") {
		t.Fatalf("artifacts=%+v, want dom_changes artifact", artifacts)
	}
	if !hasArtifact(artifacts, "recovery_plan", "browser.request_anomaly_fix", "recovery") {
		t.Fatalf("artifacts=%+v, want recovery_plan artifact", artifacts)
	}
	if !hasArtifact(artifacts, "frame_snapshot", "browser.frame", "elements") {
		t.Fatalf("artifacts=%+v, want frame snapshot artifact", artifacts)
	}
	if !hasArtifact(artifacts, "response_body", "browser.network", "body") {
		t.Fatalf("artifacts=%+v, want network response body artifact", artifacts)
	}
}

func TestCollectArtifacts_CollectsStorageFormAndIframeArtifacts(t *testing.T) {
	artifacts := collectArtifacts([]toolResultSnapshot{
		{
			ToolName: "browser.storage",
			Output: json.RawMessage(`{
				"action":"export",
				"state":{"cookies":[],"localStorage":{"token":"abc"}}
			}`),
		},
		{
			ToolName: "browser.fill_form",
			Output: json.RawMessage(`{
				"filled":1,
				"missing":["otp"],
				"results":[
					{"key":"email","status":"ok","tag":"input","name":"email"},
					{"key":"otp","status":"not_found"}
				],
				"submitted":false
			}`),
		},
		{
			ToolName: "browser.iframe",
			Output: json.RawMessage(`{
				"action":"list",
				"count":1,
				"frames":[{"index":0,"url":"https://pay.example/frame","name":"pay","id":"stripe","w":400,"h":300,"visible":true}]
			}`),
		},
		{
			ToolName: "browser.iframe",
			Output: json.RawMessage(`{
				"action":"snapshot",
				"frame":{"index":0,"url":"https://pay.example/frame"},
				"elements":[{"id":1,"role":"button","name":"Pay"}]
			}`),
		},
	})

	if !hasArtifact(artifacts, "storage_state", "browser.storage", "state") {
		t.Fatalf("artifacts=%+v, want inline storage_state artifact", artifacts)
	}
	if !hasArtifact(artifacts, "form_fill_results", "browser.fill_form", "results") {
		t.Fatalf("artifacts=%+v, want form fill results artifact", artifacts)
	}
	if !hasArtifact(artifacts, "frame_catalog", "browser.iframe", "frames") {
		t.Fatalf("artifacts=%+v, want iframe catalog artifact", artifacts)
	}
	if !hasArtifact(artifacts, "frame_snapshot", "browser.iframe", "elements") {
		t.Fatalf("artifacts=%+v, want iframe snapshot artifact", artifacts)
	}
}

func TestCollectArtifacts_CollectsVisualInspectArtifacts(t *testing.T) {
	artifacts := collectArtifacts([]toolResultSnapshot{
		{
			ToolName: "browser.visual_inspect",
			Output: json.RawMessage(`{
				"screenshot":{"data":"abc123","format":"png","encoding":"base64"},
				"snapshot":{"count":1,"elements":[{"id":1,"role":"button","name":"Pay"}]},
				"semantics":[{"id":1,"action_intent":"submit_payment","risk_level":"high"}]
			}`),
		},
	})

	if !hasArtifact(artifacts, "screenshot", "browser.visual_inspect", "screenshot.data") {
		t.Fatalf("artifacts=%+v, want visual inspect screenshot artifact", artifacts)
	}
	if !hasArtifact(artifacts, "snapshot", "browser.visual_inspect", "snapshot") {
		t.Fatalf("artifacts=%+v, want visual inspect snapshot artifact", artifacts)
	}
	if !hasArtifact(artifacts, "semantic_annotations", "browser.visual_inspect", "semantics") {
		t.Fatalf("artifacts=%+v, want visual inspect semantics artifact", artifacts)
	}
}

func TestCollectArtifacts_UsesToolInputForProxyAndCatalogArtifacts(t *testing.T) {
	artifacts := collectArtifacts([]toolResultSnapshot{
		{
			ToolName: "browser.open",
			Output:   json.RawMessage(`{"status":"ok","url":"https://example.com/","target_id":"page-1"}`),
		},
		{
			ToolName:  "browser.navigate",
			ToolInput: json.RawMessage(`{"action":"list_tabs"}`),
			Output: json.RawMessage(`[
				{"target_id":"tab-1","title":"Home","url":"https://example.com/"}
			]`),
		},
		{
			ToolName:  "browser.iframe",
			ToolInput: json.RawMessage(`{"action":"list"}`),
			Output: json.RawMessage(`{
				"action":"list",
				"count":1,
				"frames":[{"index":0,"url":"https://pay.example/frame","name":"pay","id":"stripe","w":400,"h":300,"visible":true}]
			}`),
		},
		{
			ToolName:  "verifier.browser_action",
			ToolInput: json.RawMessage(`{"action":"screenshot","params":{"full_page":true}}`),
			Output:    json.RawMessage(`{"status":"ok","format":"png","data":"abc123","encoding":"base64"}`),
		},
		{
			ToolName:  "verifier.browser_action",
			ToolInput: json.RawMessage(`{"action":"open","params":{"url":"https://example.com/orders"}}`),
			Output:    json.RawMessage(`{"status":"ok","url":"https://example.com/orders","target_id":"page-2"}`),
		},
		{
			ToolName:  "verifier.browser_action",
			ToolInput: json.RawMessage(`{"action":"navigate","params":{"action":"list_tabs"}}`),
			Output:    json.RawMessage(`[{"target_id":"page-1","url":"https://example.com/orders"}]`),
		},
		{
			ToolName:  "verifier.browser_action",
			ToolInput: json.RawMessage(`{"action":"upload_file","params":{"selector":"input[type=file]"}}`),
			Output:    json.RawMessage(`{"status":"ok","files":["/tmp/proxy.csv"]}`),
		},
		{
			ToolName: "code.list_files",
			Output:   json.RawMessage(`{"paths":["a.go","b.go"],"count":2,"truncated":false}`),
		},
		{
			ToolName: "code.search",
			Output:   json.RawMessage(`{"matches":[{"file":"a.go","line":1,"text":"package main"}],"total":1,"truncated":false}`),
		},
	})

	if !hasArtifact(artifacts, "page_ref", "browser.open", "page-1") {
		t.Fatalf("artifacts=%+v, want page_ref artifact", artifacts)
	}
	if !hasArtifact(artifacts, "tab_catalog", "browser.navigate", "tabs") {
		t.Fatalf("artifacts=%+v, want tab_catalog artifact", artifacts)
	}
	if !hasArtifact(artifacts, "frame_catalog", "browser.iframe", "frames") {
		t.Fatalf("artifacts=%+v, want frame_catalog artifact", artifacts)
	}
	if !hasArtifact(artifacts, "screenshot", "verifier.browser_action", "data") {
		t.Fatalf("artifacts=%+v, want proxied screenshot artifact", artifacts)
	}
	if !hasArtifact(artifacts, "page_ref", "verifier.browser_action", "page-2") {
		t.Fatalf("artifacts=%+v, want proxied page_ref artifact", artifacts)
	}
	if !hasArtifact(artifacts, "tab_catalog", "verifier.browser_action", "tabs") {
		t.Fatalf("artifacts=%+v, want proxied tab_catalog artifact", artifacts)
	}
	if !hasArtifact(artifacts, "upload_file", "verifier.browser_action", "/tmp/proxy.csv") {
		t.Fatalf("artifacts=%+v, want proxied upload_file artifact", artifacts)
	}
	if !hasArtifact(artifacts, "file_list", "code.list_files", "paths") {
		t.Fatalf("artifacts=%+v, want file_list artifact", artifacts)
	}
	if !hasArtifact(artifacts, "search_matches", "code.search", "matches") {
		t.Fatalf("artifacts=%+v, want search_matches artifact", artifacts)
	}
}

func TestApplyStructuredOutputs_FallsBackToTurnError(t *testing.T) {
	out := &ExecuteResult{Status: "failed", Error: "budget exhausted"}
	runResult := &loop.RunResult{
		Turns: []*loop.TurnResult{
			{
				Error: brainerrors.New(brainerrors.CodeToolExecutionFailed,
					brainerrors.WithMessage("tool failed hard")),
			},
		},
	}

	applyStructuredOutputs(out, runResult)

	if out.FaultSummary == nil {
		t.Fatal("fault_summary=nil, want turn fallback")
	}
	if out.FaultSummary.Source != "turn" {
		t.Fatalf("fault source=%q, want turn", out.FaultSummary.Source)
	}
	if out.FaultSummary.Code != brainerrors.CodeToolExecutionFailed {
		t.Fatalf("fault code=%q, want %q", out.FaultSummary.Code, brainerrors.CodeToolExecutionFailed)
	}
	if out.FaultSummary.Message != "tool failed hard" {
		t.Fatalf("fault message=%q, want tool failed hard", out.FaultSummary.Message)
	}
}

func TestCollectFaultSummary_PatternExecAbortIsStructured(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"pattern_id":"login_step1",
				"success":false,
				"error":"aborted_by_anomaly: captcha detected",
				"aborted_by_anomaly":"captcha"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want pattern_exec fault")
	}
	if fault.Tool != "browser.pattern_exec" {
		t.Fatalf("tool=%q, want browser.pattern_exec", fault.Tool)
	}
	if fault.Route != "abort" {
		t.Fatalf("route=%q, want abort", fault.Route)
	}
	if fault.Code != "aborted_by_anomaly" {
		t.Fatalf("code=%q, want aborted_by_anomaly", fault.Code)
	}
	if len(fault.Anomalies) != 1 || fault.Anomalies[0].Type != "captcha" {
		t.Fatalf("anomalies=%+v, want captcha anomaly", fault.Anomalies)
	}
}

func TestCollectFaultSummary_PatternExecHumanInterventionWithAnomalyPrefersAbort(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"pattern_id":"login_step1",
				"success":false,
				"error":"human_intervention:aborted",
				"aborted_by_anomaly":"captcha"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want pattern_exec abort fault")
	}
	if fault.Tool != "browser.pattern_exec" {
		t.Fatalf("tool=%q, want browser.pattern_exec", fault.Tool)
	}
	if fault.Route != "abort" {
		t.Fatalf("route=%q, want abort", fault.Route)
	}
	if fault.Code != "aborted_by_anomaly" {
		t.Fatalf("code=%q, want aborted_by_anomaly", fault.Code)
	}
	if fault.Message != "human_intervention:aborted" {
		t.Fatalf("message=%q, want preserved human_intervention message", fault.Message)
	}
	if len(fault.Anomalies) != 1 || fault.Anomalies[0].Type != "captcha" {
		t.Fatalf("anomalies=%+v, want captcha anomaly", fault.Anomalies)
	}
}

func TestCollectFaultSummary_PatternExecHumanInterventionWithoutAnomalyGetsStableCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"pattern_id":"login_step1",
				"success":false,
				"error":"human_intervention:no_coordinator"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want pattern_exec human_intervention fault")
	}
	if fault.Route != "human_intervention" {
		t.Fatalf("route=%q, want human_intervention", fault.Route)
	}
	if fault.Code != "human_intervention_no_coordinator" {
		t.Fatalf("code=%q, want human_intervention_no_coordinator", fault.Code)
	}
	if fault.Message != "human_intervention:no_coordinator" {
		t.Fatalf("message=%q, want preserved human_intervention message", fault.Message)
	}
}

func TestCollectFaultSummary_AnomaliesDriveFault(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.check_anomaly_v2",
			Output: json.RawMessage(`{
				"page_health":"degraded",
				"anomalies":[
					{"type":"captcha","subtype":"hcaptcha","severity":"blocker"}
				]
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want anomaly-driven fault")
	}
	if fault.Route != "anomaly_detected" {
		t.Fatalf("route=%q, want anomaly_detected", fault.Route)
	}
	if fault.Code != "anomaly_detected" {
		t.Fatalf("code=%q, want anomaly_detected", fault.Code)
	}
	if fault.PageHealth != "degraded" {
		t.Fatalf("page_health=%q, want degraded", fault.PageHealth)
	}
	if len(fault.Anomalies) != 1 || fault.Anomalies[0].Subtype != "hcaptcha" {
		t.Fatalf("anomalies=%+v, want hcaptcha anomaly", fault.Anomalies)
	}
}

func TestCollectFaultSummary_CheckOutputVerificationFailure(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.check_output",
			IsError:  true,
			Output: json.RawMessage(`{
				"match":false,
				"mode":"exact",
				"diff":"expected: \"ok\"\nactual:   \"fail\""
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want verifier fault")
	}
	if fault.Tool != "verifier.check_output" {
		t.Fatalf("tool=%q, want verifier.check_output", fault.Tool)
	}
	if fault.Route != "verification_failed" {
		t.Fatalf("route=%q, want verification_failed", fault.Route)
	}
	if fault.Code != "verification_failed" {
		t.Fatalf("code=%q, want verification_failed", fault.Code)
	}
	if !strings.Contains(fault.Message, `expected: "ok"`) {
		t.Fatalf("message=%q, want diff text", fault.Message)
	}
}

func TestCollectFaultSummary_LicenseFeatureErrorUsesStableRouteAndCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_match",
			IsError:  true,
			Output: json.RawMessage(`{
				"error":"license feature \"browser-pro.intelligence\" is required for browser.pattern_match",
				"error_code":"license_feature_not_allowed",
				"feature":"browser-pro.intelligence",
				"tool":"browser.pattern_match"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want license fault")
	}
	if fault.Tool != "browser.pattern_match" {
		t.Fatalf("tool=%q, want browser.pattern_match", fault.Tool)
	}
	if fault.Route != "license_denied" {
		t.Fatalf("route=%q, want license_denied", fault.Route)
	}
	if fault.Code != brainerrors.CodeLicenseFeatureNotAllowed {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeLicenseFeatureNotAllowed)
	}
	if !strings.Contains(fault.Message, "browser-pro.intelligence") {
		t.Fatalf("message=%q, want feature detail", fault.Message)
	}
}

func TestCollectFaultSummary_GenericToolTimeoutUsesStructuredErrorCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.click",
			IsError:  true,
			Output: json.RawMessage(`{
				"error":"click: selector did not respond",
				"error_code":"tool_timeout",
				"error_class":"transient",
				"retryable":true
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want timeout fault")
	}
	if fault.Tool != "browser.click" {
		t.Fatalf("tool=%q, want browser.click", fault.Tool)
	}
	if fault.Code != brainerrors.CodeToolTimeout {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeToolTimeout)
	}
	if fault.Route != "timeout" {
		t.Fatalf("route=%q, want timeout", fault.Route)
	}
	if fault.Message != "click: selector did not respond" {
		t.Fatalf("message=%q, want structured tool error message", fault.Message)
	}
}

func TestCollectFaultSummary_VerifierBrowserActionTimeoutUsesStructuredErrorCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.browser_action",
			IsError:  true,
			Output: json.RawMessage(`{
				"error":"wait visible: context deadline exceeded",
				"error_code":"deadline_exceeded",
				"error_class":"transient",
				"retryable":true
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want timeout fault")
	}
	if fault.Tool != "verifier.browser_action" {
		t.Fatalf("tool=%q, want verifier.browser_action", fault.Tool)
	}
	if fault.Code != brainerrors.CodeDeadlineExceeded {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeDeadlineExceeded)
	}
	if fault.Route != "timeout" {
		t.Fatalf("route=%q, want timeout", fault.Route)
	}
}

func TestCollectFaultSummary_InputInvalidMessageGetsStableRouteAndCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.browser_action",
			IsError:  true,
			Output:   json.RawMessage(`"unknown browser action: submit_form"`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want input invalid fault")
	}
	if fault.Tool != "verifier.browser_action" {
		t.Fatalf("tool=%q, want verifier.browser_action", fault.Tool)
	}
	if fault.Route != "input_invalid" {
		t.Fatalf("route=%q, want input_invalid", fault.Route)
	}
	if fault.Code != brainerrors.CodeToolInputInvalid {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeToolInputInvalid)
	}
	if fault.Message != "unknown browser action: submit_form" {
		t.Fatalf("message=%q, want preserved invalid-action message", fault.Message)
	}
}

func TestCollectFaultSummary_NoBrowserSessionGetsStableRouteAndCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.click",
			IsError:  true,
			Output:   json.RawMessage(`"no browser session: not initialized"`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want precondition fault")
	}
	if fault.Tool != "browser.click" {
		t.Fatalf("tool=%q, want browser.click", fault.Tool)
	}
	if fault.Route != "precondition_failed" {
		t.Fatalf("route=%q, want precondition_failed", fault.Route)
	}
	if fault.Code != brainerrors.CodeWorkflowPrecondition {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeWorkflowPrecondition)
	}
	if fault.Message != "no browser session: not initialized" {
		t.Fatalf("message=%q, want preserved precondition message", fault.Message)
	}
}

func TestCollectFaultSummary_CommandDeniedMessageGetsStableRouteAndCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "code.shell_exec",
			IsError:  true,
			Output:   json.RawMessage(`"command execution denied: OS-level command sandbox is unavailable"`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want policy fault")
	}
	if fault.Tool != "code.shell_exec" {
		t.Fatalf("tool=%q, want code.shell_exec", fault.Tool)
	}
	if fault.Route != "policy_denied" {
		t.Fatalf("route=%q, want policy_denied", fault.Route)
	}
	if fault.Code != brainerrors.CodeToolSandboxDenied {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeToolSandboxDenied)
	}
	if fault.Message != "command execution denied: OS-level command sandbox is unavailable" {
		t.Fatalf("message=%q, want preserved policy message", fault.Message)
	}
}

func TestCollectFaultSummary_RunTestsFailureUsesStructuredFields(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.run_tests",
			IsError:  true,
			Output: json.RawMessage(`{
				"stdout":"FAIL\tgithub.com/leef-l/brain/sdk/sidecar\t0.012s",
				"stderr":"--- FAIL: TestSidecar",
				"exit_code":1,
				"passed":false,
				"timed_out":false
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want run_tests failure")
	}
	if fault.Tool != "verifier.run_tests" {
		t.Fatalf("tool=%q, want verifier.run_tests", fault.Tool)
	}
	if fault.Route != "verification_failed" {
		t.Fatalf("route=%q, want verification_failed", fault.Route)
	}
	if fault.Code != "test_failed" {
		t.Fatalf("code=%q, want test_failed", fault.Code)
	}
	if !strings.Contains(fault.Message, "test command failed with exit_code=1") {
		t.Fatalf("message=%q, want structured exit summary", fault.Message)
	}
	if !strings.Contains(fault.Message, "--- FAIL: TestSidecar") {
		t.Fatalf("message=%q, want stderr detail", fault.Message)
	}
}

func TestCollectVerification_FillFormAllFieldsResolved(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.fill_form",
			Output: json.RawMessage(`{
				"filled":3,
				"missing":[],
				"results":[
					{"key":"email","status":"ok"},
					{"key":"password","status":"ok"},
					{"key":"otp","status":"ok"}
				],
				"submitted":false
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want structured fill_form verification")
	}
	if verification.SourceTool != "browser.fill_form" {
		t.Fatalf("source_tool=%q, want browser.fill_form", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 4 {
		t.Fatalf("checks=%+v, want 4 checks", verification.Checks)
	}
	if verification.Checks[0].Name != "browser.fill_form.fields_resolved" {
		t.Fatalf("check=%+v, want fields_resolved", verification.Checks[0])
	}
	if verification.Checks[1].Name != "browser.fill_form.field:email" || !verification.Checks[1].Passed {
		t.Fatalf("checks[1]=%+v, want passing email field check", verification.Checks[1])
	}
}

func TestCollectVerification_FillFormMissingFieldsFailsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.fill_form",
			Output: json.RawMessage(`{
				"filled":1,
				"missing":["email","password"],
				"results":[{"key":"otp","status":"ok"}],
				"submitted":false
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want fill_form verification")
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 2 {
		t.Fatalf("checks=%+v, want 2 checks", verification.Checks)
	}
	if !strings.Contains(verification.Checks[0].Reason, "missing=email,password") {
		t.Fatalf("reason=%q, want missing field list", verification.Checks[0].Reason)
	}
	if verification.Checks[1].Name != "browser.fill_form.field:otp" || !verification.Checks[1].Passed {
		t.Fatalf("checks[1]=%+v, want passing otp field check", verification.Checks[1])
	}
}

func TestCollectVerification_FillFormSubmittedAddsCheck(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.fill_form",
			Output: json.RawMessage(`{
				"filled":2,
				"missing":[],
				"results":[
					{"key":"email","status":"ok"},
					{"key":"password","status":"ok"}
				],
				"submitted":true
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want fill_form verification")
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 4 {
		t.Fatalf("checks=%+v, want 4 checks", verification.Checks)
	}
	if verification.Checks[3].Name != "browser.fill_form.submitted" {
		t.Fatalf("checks[3]=%+v, want submitted check", verification.Checks[3])
	}
}

func TestCollectVerification_FillFormFieldErrorFailsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.fill_form",
			Output: json.RawMessage(`{
				"filled":1,
				"missing":[],
				"results":[
					{"key":"email","status":"ok"},
					{"key":"password","status":"error","error":"element detached"}
				],
				"submitted":false
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want fill_form verification")
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 3 {
		t.Fatalf("checks=%+v, want 3 checks", verification.Checks)
	}
	if verification.Checks[2].Name != "browser.fill_form.field:password" {
		t.Fatalf("checks[2]=%+v, want password field check", verification.Checks[2])
	}
	if verification.Checks[2].Passed {
		t.Fatalf("checks[2]=%+v, want failed password field check", verification.Checks[2])
	}
	if !strings.Contains(verification.Checks[2].Reason, "element detached") {
		t.Fatalf("reason=%q, want error detail", verification.Checks[2].Reason)
	}
}

func TestCollectVerification_UsesWaitNetworkIdleAsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "wait.network_idle",
			Output:   json.RawMessage(`{"status":"idle","idle_ms":500,"forced":false}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want wait.network_idle verification")
	}
	if verification.SourceTool != "wait.network_idle" {
		t.Fatalf("source_tool=%q, want wait.network_idle", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "wait.network_idle.status" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want passed wait.network_idle.status", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=idle; idle_ms=500; forced=false" {
		t.Fatalf("reason=%q, want idle verification detail", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_WaitNetworkIdleTimeoutFailsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "wait.network_idle",
			Output:   json.RawMessage(`{"status":"timeout","forced":true}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want wait.network_idle verification")
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "wait.network_idle.status" || verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want failed wait.network_idle.status", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=timeout; forced=true" {
		t.Fatalf("reason=%q, want timeout verification detail", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_UsesBrowserWaitAsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.wait",
			Output:   json.RawMessage(`{"status":"ok","condition":"visible"}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want browser.wait verification")
	}
	if verification.SourceTool != "browser.wait" {
		t.Fatalf("source_tool=%q, want browser.wait", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "browser.wait.condition" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want passed browser.wait.condition", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=ok; condition=visible" {
		t.Fatalf("reason=%q, want browser.wait verification detail", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_BrowserWaitNonOKFailsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.wait",
			Output:   json.RawMessage(`{"status":"timeout","condition":"load"}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want browser.wait verification")
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "browser.wait.condition" || verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want failed browser.wait.condition", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=timeout; condition=load" {
		t.Fatalf("reason=%q, want browser.wait failure detail", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_VerifierBrowserActionWaitIsStructured(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName:  "verifier.browser_action",
			ToolInput: json.RawMessage(`{"action":"wait","params":{"condition":"load"}}`),
			Output:    json.RawMessage(`{"status":"ok","condition":"load"}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want verifier.browser_action wait verification")
	}
	if verification.SourceTool != "verifier.browser_action" {
		t.Fatalf("source_tool=%q, want verifier.browser_action", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "verifier.browser_action.wait.condition" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want passed verifier.browser_action.wait.condition", verification.Checks[0])
	}
}

func TestCollectVerification_UsesBrowserUploadFileAsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.upload_file",
			Output:   json.RawMessage(`{"status":"ok","files":["/tmp/a.csv","/tmp/b.csv"]}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want browser.upload_file verification")
	}
	if verification.SourceTool != "browser.upload_file" {
		t.Fatalf("source_tool=%q, want browser.upload_file", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 2 {
		t.Fatalf("checks len=%d, want 2", len(verification.Checks))
	}
	if verification.Checks[0].Name != "browser.upload_file.status" || !verification.Checks[0].Passed {
		t.Fatalf("checks[0]=%+v, want passed browser.upload_file.status", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=ok; files=2" {
		t.Fatalf("checks[0].reason=%q, want upload status detail", verification.Checks[0].Reason)
	}
	if verification.Checks[1].Name != "browser.upload_file.files_attached" || !verification.Checks[1].Passed {
		t.Fatalf("checks[1]=%+v, want files_attached check", verification.Checks[1])
	}
	if verification.Checks[1].Reason != "/tmp/a.csv,/tmp/b.csv" {
		t.Fatalf("checks[1].reason=%q, want uploaded file list", verification.Checks[1].Reason)
	}
}

func TestCollectVerification_BrowserUploadFileNonOKFailsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.upload_file",
			Output:   json.RawMessage(`{"status":"error","files":[]}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want browser.upload_file verification")
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "browser.upload_file.status" || verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want failed browser.upload_file.status", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=error; files=0" {
		t.Fatalf("reason=%q, want upload failure detail", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_VerifierBrowserActionUploadFileIsStructured(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName:  "verifier.browser_action",
			ToolInput: json.RawMessage(`{"action":"upload_file","params":{"selector":"input[type=file]"}}`),
			Output:    json.RawMessage(`{"status":"ok","files":["/tmp/input.csv"]}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want verifier.browser_action upload_file verification")
	}
	if verification.SourceTool != "verifier.browser_action" {
		t.Fatalf("source_tool=%q, want verifier.browser_action", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 2 {
		t.Fatalf("checks len=%d, want 2", len(verification.Checks))
	}
	if verification.Checks[0].Name != "verifier.browser_action.upload_file.status" || !verification.Checks[0].Passed {
		t.Fatalf("checks[0]=%+v, want passed verifier.browser_action.upload_file.status", verification.Checks[0])
	}
	if verification.Checks[1].Name != "verifier.browser_action.upload_file.files_attached" || !verification.Checks[1].Passed {
		t.Fatalf("checks[1]=%+v, want passed verifier.browser_action.upload_file.files_attached", verification.Checks[1])
	}
}

func TestCollectVerification_BrowserSelectIsStructured(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.select",
			Output:   json.RawMessage(`{"status":"ok","value":"paid"}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want browser.select verification")
	}
	if verification.SourceTool != "browser.select" {
		t.Fatalf("source_tool=%q, want browser.select", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "browser.select.applied" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want passed browser.select.applied", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=ok; value=paid" {
		t.Fatalf("reason=%q, want select detail", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_VerifierBrowserActionSelectIsStructured(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName:  "verifier.browser_action",
			ToolInput: json.RawMessage(`{"action":"select","params":{"selector":"select[name=status]","value":"paid"}}`),
			Output:    json.RawMessage(`{"status":"ok","value":"paid","text":"Paid"}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want verifier.browser_action select verification")
	}
	if verification.SourceTool != "verifier.browser_action" {
		t.Fatalf("source_tool=%q, want verifier.browser_action", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "verifier.browser_action.select.applied" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want passed verifier.browser_action.select.applied", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "status=ok; value=paid; text=Paid" {
		t.Fatalf("reason=%q, want proxied select detail", verification.Checks[0].Reason)
	}
}

func TestCollectFaultSummary_RunTestsTimeoutUsesStructuredFields(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.run_tests",
			IsError:  true,
			Output: json.RawMessage(`{
				"stdout":"",
				"stderr":"",
				"exit_code":-1,
				"passed":false,
				"timed_out":true
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want run_tests timeout")
	}
	if fault.Route != "timeout" {
		t.Fatalf("route=%q, want timeout", fault.Route)
	}
	if fault.Code != "test_timeout" {
		t.Fatalf("code=%q, want test_timeout", fault.Code)
	}
	if fault.Message != "test command timed out (exit_code=-1)" {
		t.Fatalf("message=%q, want timeout summary", fault.Message)
	}
}

func TestCollectFaultSummary_ShellExecNonZeroExitUsesStructuredFields(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "code.shell_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"stdout":"",
				"stderr":"git diff failed",
				"exit_code":128,
				"timed_out":false,
				"sandboxed":true
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want shell_exec failure")
	}
	if fault.Tool != "code.shell_exec" {
		t.Fatalf("tool=%q, want code.shell_exec", fault.Tool)
	}
	if fault.Route != "command_failed" {
		t.Fatalf("route=%q, want command_failed", fault.Route)
	}
	if fault.Code != "command_exit_nonzero" {
		t.Fatalf("code=%q, want command_exit_nonzero", fault.Code)
	}
	if !strings.Contains(fault.Message, "shell command failed with exit_code=128") {
		t.Fatalf("message=%q, want structured exit summary", fault.Message)
	}
	if !strings.Contains(fault.Message, "git diff failed") {
		t.Fatalf("message=%q, want stderr detail", fault.Message)
	}
}

func TestCollectFaultSummary_ShellExecTimeoutUsesStructuredFields(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "code.shell_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"stdout":"partial output",
				"stderr":"command timed out",
				"exit_code":124,
				"timed_out":true,
				"sandboxed":true
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want shell_exec timeout")
	}
	if fault.Tool != "code.shell_exec" {
		t.Fatalf("tool=%q, want code.shell_exec", fault.Tool)
	}
	if fault.Route != "timeout" {
		t.Fatalf("route=%q, want timeout", fault.Route)
	}
	if fault.Code != "command_timeout" {
		t.Fatalf("code=%q, want command_timeout", fault.Code)
	}
	if !strings.Contains(fault.Message, "timed out") {
		t.Fatalf("message=%q, want timeout summary", fault.Message)
	}
}

func TestCollectFaultSummary_UsesNestedAnomaliesContainer(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.check_anomaly_v2",
			IsError:  true,
			Output: json.RawMessage(`{
				"_anomalies":{
					"page_health":"broken",
					"anomalies":[
						{"type":"captcha","subtype":"hcaptcha","severity":"high"}
					]
				}
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want anomaly-driven fault")
	}
	if fault.Route != "anomaly_detected" {
		t.Fatalf("route=%q, want anomaly_detected", fault.Route)
	}
	if fault.PageHealth != "broken" {
		t.Fatalf("page_health=%q, want broken", fault.PageHealth)
	}
	if len(fault.Anomalies) != 1 || fault.Anomalies[0].Subtype != "hcaptcha" {
		t.Fatalf("anomalies=%+v, want nested anomaly subtype hcaptcha", fault.Anomalies)
	}
}

func TestCollectFaultSummary_LicenseCodeMapsToLicenseRoute(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.understand",
			IsError:  true,
			Output: json.RawMessage(`{
				"error_code":"license_feature_not_allowed",
				"message":"browser-pro.intelligence is not enabled"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want license fault")
	}
	if fault.Route != "license_denied" {
		t.Fatalf("route=%q, want license_denied", fault.Route)
	}
	if fault.Code != brainerrors.CodeLicenseFeatureNotAllowed {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeLicenseFeatureNotAllowed)
	}
}

func TestCollectFaultSummary_BudgetCodeMapsToBudgetExhaustedRoute(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"error_code":"budget_tool_calls_exhausted",
				"message":"tool budget exhausted"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want budget fault")
	}
	if fault.Route != "budget_exhausted" {
		t.Fatalf("route=%q, want budget_exhausted", fault.Route)
	}
	if fault.Code != brainerrors.CodeBudgetToolCallsExhausted {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeBudgetToolCallsExhausted)
	}
}

func TestCollectFaultSummary_PolicyCodeMapsToPolicyDeniedRoute(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "code.shell_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"error_code":"tool_sandbox_denied",
				"message":"sandbox denied"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want policy fault")
	}
	if fault.Route != "policy_denied" {
		t.Fatalf("route=%q, want policy_denied", fault.Route)
	}
	if fault.Code != brainerrors.CodeToolSandboxDenied {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeToolSandboxDenied)
	}
}

func TestCollectFaultSummary_CodeOnlyErrorStillNormalizes(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.click",
			IsError:  true,
			Output:   json.RawMessage(`{"code":"tool_execution_failed"}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want normalized code-only fault")
	}
	if fault.Tool != "browser.click" {
		t.Fatalf("tool=%q, want browser.click", fault.Tool)
	}
	if fault.Code != "tool_execution_failed" {
		t.Fatalf("code=%q, want tool_execution_failed", fault.Code)
	}
	if fault.Route != "tool_execution_failed" {
		t.Fatalf("route=%q, want tool_execution_failed", fault.Route)
	}
}

func TestCollectVerification_MergesPatternExecAndAnomalyChecks(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			Output: json.RawMessage(`{
				"pattern_id":"checkout_submit",
				"success":true,
				"post_conditions":[
					{"type":"dom_contains","ok":true,"reason":"#receipt present"},
					{"type":"url_contains","ok":true,"reason":"/checkout/complete"}
				]
			}`),
		},
		{
			ToolName: "browser.check_anomaly_v2",
			Output: json.RawMessage(`{
				"page_health":"degraded",
				"anomalies":[
					{"type":"error_message","subtype":"keyword","severity":"medium","description":"Payment temporarily unavailable"}
				]
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want merged verification")
	}
	if verification.SourceTool != "browser.pattern_exec,browser.check_anomaly_v2" {
		t.Fatalf("source_tool=%q, want merged source order", verification.SourceTool)
	}
	if verification.PatternID != "checkout_submit" {
		t.Fatalf("pattern_id=%q, want checkout_submit", verification.PatternID)
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false because anomaly health failed", verification.Passed)
	}
	if len(verification.Checks) != 4 {
		t.Fatalf("checks len=%d, want 4", len(verification.Checks))
	}
	if verification.Checks[2].Name != "browser.check_anomaly_v2.page_health" || verification.Checks[2].Passed {
		t.Fatalf("page health check=%+v, want failed anomaly health check", verification.Checks[2])
	}
	if verification.Checks[3].Name != "browser.check_anomaly_v2.anomaly:error_message/keyword" || verification.Checks[3].Passed {
		t.Fatalf("anomaly check=%+v, want failed anomaly detail", verification.Checks[3])
	}
}

func TestCollectVerification_UsesHealthyAnomalyCheckAsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.check_anomaly",
			Output:   json.RawMessage(`{"page_health":"healthy","anomalies":[],"checked_at":1710000000}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want anomaly-based verification")
	}
	if verification.SourceTool != "browser.check_anomaly" {
		t.Fatalf("source_tool=%q, want browser.check_anomaly", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "browser.check_anomaly.page_health" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want healthy page_health check", verification.Checks[0])
	}
}

func TestCollectVerification_UsesNestedAnomaliesContainer(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.check_anomaly_v2",
			Output: json.RawMessage(`{
				"_anomalies":{
					"page_health":"broken",
					"anomalies":[
						{"type":"captcha","subtype":"hcaptcha","severity":"blocker","description":"challenge shown"}
					]
				}
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want nested anomaly verification")
	}
	if verification.SourceTool != "browser.check_anomaly_v2" {
		t.Fatalf("source_tool=%q, want browser.check_anomaly_v2", verification.SourceTool)
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 2 {
		t.Fatalf("checks len=%d, want 2", len(verification.Checks))
	}
	if verification.Checks[0].Name != "browser.check_anomaly_v2.page_health" || verification.Checks[0].Passed {
		t.Fatalf("checks[0]=%+v, want failed page_health check", verification.Checks[0])
	}
	if verification.Checks[1].Name != "browser.check_anomaly_v2.anomaly:captcha/hcaptcha" || verification.Checks[1].Passed {
		t.Fatalf("checks[1]=%+v, want failed nested anomaly check", verification.Checks[1])
	}
	if !strings.Contains(verification.Checks[1].Reason, "severity=blocker") {
		t.Fatalf("checks[1].reason=%q, want severity detail", verification.Checks[1].Reason)
	}
}

func TestCollectVerification_UsesVerifierCheckOutputAsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.check_output",
			Output: json.RawMessage(`{
				"match":true,
				"mode":"contains"
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want verifier.check_output verification")
	}
	if verification.SourceTool != "verifier.check_output" {
		t.Fatalf("source_tool=%q, want verifier.check_output", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "verifier.check_output.match" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want passed verifier.check_output.match", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "mode=contains" {
		t.Fatalf("reason=%q, want mode=contains", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_UsesVerifierRunTestsAsVerification(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.run_tests",
			Output: json.RawMessage(`{
				"stdout":"ok\\tgithub.com/leef-l/brain/sdk/sidecar\\t0.012s",
				"stderr":"",
				"exit_code":0,
				"passed":true,
				"timed_out":false
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want verifier.run_tests verification")
	}
	if verification.SourceTool != "verifier.run_tests" {
		t.Fatalf("source_tool=%q, want verifier.run_tests", verification.SourceTool)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "verifier.run_tests.passed" || !verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want passed verifier.run_tests.passed", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "exit_code=0" {
		t.Fatalf("reason=%q, want exit_code=0", verification.Checks[0].Reason)
	}
}

func TestCollectVerification_PatternExecFallsBackToSuccessWithoutPostConditions(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			Output: json.RawMessage(`{
				"pattern_id":"checkout_submit",
				"success":true,
				"actions_run":3
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want pattern_exec success fallback")
	}
	if verification.SourceTool != "browser.pattern_exec" {
		t.Fatalf("source_tool=%q, want browser.pattern_exec", verification.SourceTool)
	}
	if verification.PatternID != "checkout_submit" {
		t.Fatalf("pattern_id=%q, want checkout_submit", verification.PatternID)
	}
	if verification.Passed == nil || !*verification.Passed {
		t.Fatalf("passed=%v, want true", verification.Passed)
	}
	if len(verification.Checks) != 0 {
		t.Fatalf("checks=%+v, want no post-condition checks", verification.Checks)
	}
}

func TestCollectVerification_MergesVerifierAndBrowserSourcesByObservedOrder(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.run_tests",
			Output: json.RawMessage(`{
				"stdout":"",
				"stderr":"FAIL",
				"exit_code":1,
				"passed":false,
				"timed_out":false
			}`),
		},
		{
			ToolName: "browser.check_anomaly_v2",
			Output: json.RawMessage(`{
				"page_health":"healthy",
				"anomalies":[]
			}`),
		},
		{
			ToolName: "wait.network_idle",
			Output:   json.RawMessage(`{"status":"idle","idle_ms":750,"forced":false}`),
		},
		{
			ToolName: "verifier.check_output",
			Output: json.RawMessage(`{
				"match":true,
				"mode":"exact"
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want merged verification")
	}
	if verification.SourceTool != "verifier.run_tests,browser.check_anomaly_v2,wait.network_idle,verifier.check_output" {
		t.Fatalf("source_tool=%q, want merged observed order", verification.SourceTool)
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false because verifier.run_tests failed", verification.Passed)
	}
	if len(verification.Checks) != 4 {
		t.Fatalf("checks len=%d, want 4", len(verification.Checks))
	}
	if verification.Checks[0].Name != "verifier.run_tests.passed" || verification.Checks[0].Passed {
		t.Fatalf("checks[0]=%+v, want failed verifier.run_tests.passed", verification.Checks[0])
	}
	if verification.Checks[1].Name != "browser.check_anomaly_v2.page_health" || !verification.Checks[1].Passed {
		t.Fatalf("checks[1]=%+v, want healthy anomaly page_health", verification.Checks[1])
	}
	if verification.Checks[2].Name != "wait.network_idle.status" || !verification.Checks[2].Passed {
		t.Fatalf("checks[2]=%+v, want passed wait.network_idle.status", verification.Checks[2])
	}
	if verification.Checks[3].Name != "verifier.check_output.match" || !verification.Checks[3].Passed {
		t.Fatalf("checks[3]=%+v, want passed verifier.check_output.match", verification.Checks[3])
	}
}

func TestCollectVerification_IgnoresShellExecWithoutExplicitPassedField(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "code.shell_exec",
			Output:   json.RawMessage(`{"stdout":"ok","stderr":"","exit_code":0,"timed_out":false,"sandboxed":true}`),
		},
	}

	verification := collectVerification(results)
	if verification != nil {
		t.Fatalf("verification=%+v, want nil because shell_exec has no explicit pass/fail field", verification)
	}
}

func TestCollectVerification_RequiresExplicitVerifierPassFailFields(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.run_tests",
			Output:   json.RawMessage(`{"stdout":"ok","stderr":"","exit_code":0,"timed_out":false}`),
		},
		{
			ToolName: "verifier.check_output",
			Output:   json.RawMessage(`{"mode":"exact","diff":"expected: \"ok\"\nactual:   \"fail\""}`),
		},
	}

	verification := collectVerification(results)
	if verification != nil {
		t.Fatalf("verification=%+v, want nil because explicit passed/match fields are missing", verification)
	}
}

func TestCollectVerification_PatternExecFailureWithoutPostConditionsFallsBackToSuccessField(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			Output: json.RawMessage(`{
				"pattern_id":"checkout_submit",
				"success":false,
				"error":"retry exhausted (max=2) on anomaly captcha"
			}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want pattern_exec fallback verification")
	}
	if verification.SourceTool != "browser.pattern_exec" {
		t.Fatalf("source_tool=%q, want browser.pattern_exec", verification.SourceTool)
	}
	if verification.PatternID != "checkout_submit" {
		t.Fatalf("pattern_id=%q, want checkout_submit", verification.PatternID)
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 0 {
		t.Fatalf("checks=%+v, want no explicit post-condition checks", verification.Checks)
	}
}

func TestCollectVerification_RunTestsTimeoutCarriesTimedOutReason(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "verifier.run_tests",
			Output:   json.RawMessage(`{"exit_code":-1,"passed":false,"timed_out":true}`),
		},
	}

	verification := collectVerification(results)
	if verification == nil {
		t.Fatal("verification=nil, want verifier.run_tests verification")
	}
	if verification.SourceTool != "verifier.run_tests" {
		t.Fatalf("source_tool=%q, want verifier.run_tests", verification.SourceTool)
	}
	if verification.Passed == nil || *verification.Passed {
		t.Fatalf("passed=%v, want false", verification.Passed)
	}
	if len(verification.Checks) != 1 {
		t.Fatalf("checks len=%d, want 1", len(verification.Checks))
	}
	if verification.Checks[0].Name != "verifier.run_tests.passed" || verification.Checks[0].Passed {
		t.Fatalf("check=%+v, want failed verifier.run_tests.passed", verification.Checks[0])
	}
	if verification.Checks[0].Reason != "exit_code=-1; timed_out=true" {
		t.Fatalf("reason=%q, want timeout detail", verification.Checks[0].Reason)
	}
}

func TestCollectFaultSummary_PatternExecRetryExhaustedInfersRetryRoute(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"pattern_id":"checkout_submit",
				"success":false,
				"error":"retry exhausted (max=2) on anomaly captcha"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want pattern_exec retry fault")
	}
	if fault.Tool != "browser.pattern_exec" {
		t.Fatalf("tool=%q, want browser.pattern_exec", fault.Tool)
	}
	if fault.Route != "retry" {
		t.Fatalf("route=%q, want retry", fault.Route)
	}
	if fault.Message != "retry exhausted (max=2) on anomaly captcha" {
		t.Fatalf("message=%q, want preserved retry exhaustion text", fault.Message)
	}
}

func TestCollectFaultSummary_AnomalyOnlyWithoutPageHealthStillNormalizes(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.check_anomaly_v2",
			Output: json.RawMessage(`{
				"_anomalies":{
					"anomalies":[
						{"type":"captcha","subtype":"turnstile","severity":"high"}
					]
				}
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want anomaly-only fault")
	}
	if fault.Tool != "browser.check_anomaly_v2" {
		t.Fatalf("tool=%q, want browser.check_anomaly_v2", fault.Tool)
	}
	if fault.Route != "anomaly_detected" {
		t.Fatalf("route=%q, want anomaly_detected", fault.Route)
	}
	if fault.Code != "anomaly_detected" {
		t.Fatalf("code=%q, want anomaly_detected", fault.Code)
	}
	if fault.PageHealth != "" {
		t.Fatalf("page_health=%q, want empty when absent", fault.PageHealth)
	}
	if len(fault.Anomalies) != 1 || fault.Anomalies[0].Subtype != "turnstile" || fault.Anomalies[0].Severity != "high" {
		t.Fatalf("anomalies=%+v, want normalized captcha/turnstile/high", fault.Anomalies)
	}
}

func TestCollectFaultSummary_TurnDeadlineExceededInfersTimeoutRoute(t *testing.T) {
	out := &ExecuteResult{Status: "failed", Error: "deadline hit"}
	runResult := &loop.RunResult{
		Turns: []*loop.TurnResult{
			{
				Error: brainerrors.New(brainerrors.CodeDeadlineExceeded,
					brainerrors.WithMessage("context deadline exceeded")),
			},
		},
	}

	applyStructuredOutputs(out, runResult)

	if out.FaultSummary == nil {
		t.Fatal("fault_summary=nil, want turn fallback")
	}
	if out.FaultSummary.Source != "turn" {
		t.Fatalf("source=%q, want turn", out.FaultSummary.Source)
	}
	if out.FaultSummary.Route != "timeout" {
		t.Fatalf("route=%q, want timeout", out.FaultSummary.Route)
	}
	if out.FaultSummary.Code != brainerrors.CodeDeadlineExceeded {
		t.Fatalf("code=%q, want %q", out.FaultSummary.Code, brainerrors.CodeDeadlineExceeded)
	}
}

func TestCollectFaultSummary_TurnErrorOverridesEarlierToolFaultButKeepsContext(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.check_anomaly_v2",
			IsError:  true,
			Output: json.RawMessage(`{
				"page_health":"broken",
				"anomalies":[{"type":"captcha","subtype":"turnstile","severity":"high"}]
			}`),
		},
		{
			ToolName: "verifier.check_output",
			IsError:  true,
			Output:   json.RawMessage(`{"match":false,"mode":"exact","diff":"expected: ok"}`),
		},
	}
	turns := []*loop.TurnResult{
		{
			Error: brainerrors.New(brainerrors.CodeToolSanitizeFailed,
				brainerrors.WithMessage("secret detected in tool output")),
		},
	}

	fault := collectFaultSummary(results, turns)
	if fault == nil {
		t.Fatal("fault=nil, want turn fault")
	}
	if fault.Source != "turn" {
		t.Fatalf("source=%q, want turn", fault.Source)
	}
	if fault.Code != brainerrors.CodeToolSanitizeFailed {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeToolSanitizeFailed)
	}
	if fault.Route != "sanitize_failed" {
		t.Fatalf("route=%q, want sanitize_failed", fault.Route)
	}
	if fault.Tool != "verifier.check_output" {
		t.Fatalf("tool=%q, want verifier.check_output", fault.Tool)
	}
	if fault.PageHealth != "broken" {
		t.Fatalf("page_health=%q, want broken", fault.PageHealth)
	}
	if len(fault.Anomalies) != 1 || fault.Anomalies[0].Subtype != "turnstile" {
		t.Fatalf("anomalies=%+v, want preserved anomaly context", fault.Anomalies)
	}
}

func TestCollectFaultSummary_BudgetTimeoutUsesBudgetExhaustedRoute(t *testing.T) {
	fault := collectFaultSummary(nil, []*loop.TurnResult{
		{
			Error: brainerrors.New(brainerrors.CodeBudgetTimeoutExhausted,
				brainerrors.WithMessage("max duration exhausted")),
		},
	})

	if fault == nil {
		t.Fatal("fault=nil, want budget timeout fault")
	}
	if fault.Route != "budget_exhausted" {
		t.Fatalf("route=%q, want budget_exhausted", fault.Route)
	}
	if fault.Code != brainerrors.CodeBudgetTimeoutExhausted {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeBudgetTimeoutExhausted)
	}
}

func TestCollectFaultSummary_ShuttingDownInfersStableRoute(t *testing.T) {
	fault := collectFaultSummary(nil, []*loop.TurnResult{
		{
			Error: brainerrors.New(brainerrors.CodeShuttingDown,
				brainerrors.WithMessage("runtime is shutting down")),
		},
	})

	if fault == nil {
		t.Fatal("fault=nil, want shutting_down fault")
	}
	if fault.Route != "shutting_down" {
		t.Fatalf("route=%q, want shutting_down", fault.Route)
	}
	if fault.Code != brainerrors.CodeShuttingDown {
		t.Fatalf("code=%q, want %q", fault.Code, brainerrors.CodeShuttingDown)
	}
}

func TestCollectFaultSummary_PatternExecFallbackPatternMissingIDGetsStableCode(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolName: "browser.pattern_exec",
			IsError:  true,
			Output: json.RawMessage(`{
				"pattern_id":"checkout_submit",
				"success":false,
				"error":"fallback_pattern action without fallback_id"
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want fallback_pattern fault")
	}
	if fault.Route != "fallback_pattern" {
		t.Fatalf("route=%q, want fallback_pattern", fault.Route)
	}
	if fault.Code != "fallback_pattern_missing_id" {
		t.Fatalf("code=%q, want fallback_pattern_missing_id", fault.Code)
	}
	if fault.Message != "fallback_pattern action without fallback_id" {
		t.Fatalf("message=%q, want preserved fallback_pattern message", fault.Message)
	}
}

func TestExecuteResultJSON_OmitsEmptyStructuredFields(t *testing.T) {
	raw, err := json.Marshal(&ExecuteResult{
		Status: "completed",
		Turns:  1,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	for _, key := range []string{`"artifacts"`, `"verification"`, `"fault_summary"`} {
		if strings.Contains(text, key) {
			t.Fatalf("json=%s contains %s, want omitempty", text, key)
		}
	}
}

func TestFailedExecuteResult_BackfillsErrorFromFaultSummary(t *testing.T) {
	out := &ExecuteResult{
		Status: "failed",
	}
	runResult := &loop.RunResult{
		Run: &loop.Run{
			State: loop.StateFailed,
			Budget: loop.Budget{
				UsedTurns: 15,
			},
		},
		FinalMessages: []llm.Message{
			{
				Role: "assistant",
				Content: []llm.ContentBlock{
					{Type: "tool_use", ToolUseID: "tool-1", ToolName: "browser.pattern_exec"},
				},
			},
			{
				Role: "user",
				Content: []llm.ContentBlock{
					{
						Type:      "tool_result",
						ToolUseID: "tool-1",
						IsError:   true,
						Output:    json.RawMessage(`{"error":"retry exhausted (max=2) on anomaly captcha"}`),
					},
				},
			},
		},
	}

	applyStructuredOutputs(out, runResult)
	if out.FaultSummary == nil {
		t.Fatal("FaultSummary=nil, want populated")
	}
	if out.Error != "" {
		t.Fatalf("precondition failed: Error=%q, want empty before fallback", out.Error)
	}
	if out.Error == "" && out.Status != "completed" {
		switch {
		case out.FaultSummary != nil && strings.TrimSpace(out.FaultSummary.Message) != "":
			out.Error = strings.TrimSpace(out.FaultSummary.Message)
		case strings.TrimSpace(out.Summary) != "":
			out.Error = strings.TrimSpace(out.Summary)
		default:
			out.Error = "sidecar execution failed"
		}
	}
	if out.Error != "retry exhausted (max=2) on anomaly captcha" {
		t.Fatalf("Error=%q, want fault summary fallback", out.Error)
	}
}

func TestFailedExecuteResult_BackfillsGenericErrorWhenNoSignalExists(t *testing.T) {
	out := &ExecuteResult{Status: "failed"}
	if out.Error == "" && out.Status != "completed" {
		switch {
		case out.FaultSummary != nil && strings.TrimSpace(out.FaultSummary.Message) != "":
			out.Error = strings.TrimSpace(out.FaultSummary.Message)
		case strings.TrimSpace(out.Summary) != "":
			out.Error = strings.TrimSpace(out.Summary)
		default:
			out.Error = "sidecar execution failed"
		}
	}
	if out.Error != "sidecar execution failed" {
		t.Fatalf("Error=%q, want generic fallback", out.Error)
	}
}

func TestCollectToolResults_PreservesEmptyToolResultAsCollectionError(t *testing.T) {
	results := collectToolResults([]llm.Message{
		{
			Role: "assistant",
			Content: []llm.ContentBlock{
				{Type: "tool_use", ToolUseID: "tool-1", ToolName: "verifier.browser_action"},
			},
		},
		{
			Role: "user",
			Content: []llm.ContentBlock{
				{Type: "tool_result", ToolUseID: "tool-1", IsError: true},
			},
		},
	})

	if len(results) != 1 {
		t.Fatalf("results=%d, want 1", len(results))
	}
	if results[0].CollectCode != "empty_tool_result" {
		t.Fatalf("collect_code=%q, want empty_tool_result", results[0].CollectCode)
	}
}

func TestCollectFaultSummary_ExtractsNestedEnvelopeError(t *testing.T) {
	results := []toolResultSnapshot{
		{
			ToolUseID: "tool-1",
			ToolName:  "verifier.browser_action",
			IsError:   true,
			Output: json.RawMessage(`{
				"tool":"verifier.browser_action",
				"isError":true,
				"error":{"code":"browser_budget_exhausted","message":"browser specialist hit turn budget"}
			}`),
		},
	}

	fault := collectFaultSummary(results, nil)
	if fault == nil {
		t.Fatal("fault=nil, want nested error extracted")
	}
	if fault.Code != "browser_budget_exhausted" {
		t.Fatalf("code=%q, want browser_budget_exhausted", fault.Code)
	}
	if fault.Message != "browser specialist hit turn budget" {
		t.Fatalf("message=%q, want nested error message", fault.Message)
	}
}

func hasArtifact(artifacts []ArtifactRef, kind, toolName, locator string) bool {
	for _, artifact := range artifacts {
		if artifact.Kind == kind && artifact.Tool == toolName && artifact.Locator == locator {
			return true
		}
	}
	return false
}

func hasVerificationCheck(checks []VerificationCheck, name string, passed bool) bool {
	for _, check := range checks {
		if check.Name == name && check.Passed == passed {
			return true
		}
	}
	return false
}
