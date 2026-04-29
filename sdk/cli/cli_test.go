package cli

import (
	"encoding/json"
	"testing"
)

func TestExitCodes(t *testing.T) {
	cases := map[string]int{
		"ExitOK":              ExitOK,
		"ExitFailed":          ExitFailed,
		"ExitCanceled":        ExitCanceled,
		"ExitBudgetExhausted": ExitBudgetExhausted,
		"ExitNotFound":        ExitNotFound,
		"ExitInvalidState":    ExitInvalidState,
		"ExitUsage":           ExitUsage,
		"ExitDataErr":         ExitDataErr,
		"ExitNoInput":         ExitNoInput,
		"ExitNoPerm":          ExitNoPerm,
		"ExitSoftware":        ExitSoftware,
		"ExitOSErr":           ExitOSErr,
		"ExitCredMissing":     ExitCredMissing,
		"ExitSignalInt":       ExitSignalInt,
		"ExitSignalTerm":      ExitSignalTerm,
	}

	expected := map[string]int{
		"ExitOK":              0,
		"ExitFailed":          1,
		"ExitCanceled":        2,
		"ExitBudgetExhausted": 3,
		"ExitNotFound":        4,
		"ExitInvalidState":    5,
		"ExitUsage":           64,
		"ExitDataErr":         65,
		"ExitNoInput":         66,
		"ExitNoPerm":          67,
		"ExitSoftware":        70,
		"ExitOSErr":           71,
		"ExitCredMissing":     77,
		"ExitSignalInt":       130,
		"ExitSignalTerm":      143,
	}

	for name, got := range cases {
		want, ok := expected[name]
		if !ok {
			t.Fatalf("missing expected value for %s", name)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", name, got, want)
		}
	}
}

func TestOutputFormat(t *testing.T) {
	if FormatHuman != "human" {
		t.Fatalf("expected FormatHuman=human, got %s", FormatHuman)
	}
	if FormatJSON != "json" {
		t.Fatalf("expected FormatJSON=json, got %s", FormatJSON)
	}
}

func TestVersionInfoJSON(t *testing.T) {
	v := VersionInfo{
		CLIVersion:      "1.0.0",
		ProtocolVersion: "2.0",
		KernelVersion:   "1.0.0",
		SDKLanguage:     "go",
		SDKVersion:      "1.0.0",
		Commit:          "abc123",
		BuiltAt:         "2026-04-29T00:00:00Z",
		OS:              "linux",
		Arch:            "amd64",
	}

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal VersionInfo failed: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal VersionInfo failed: %v", err)
	}

	expectedFields := []string{
		"cli_version", "protocol_version", "kernel_version",
		"sdk_language", "sdk_version", "commit", "built_at", "os", "arch",
	}
	for _, field := range expectedFields {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("missing JSON field %s", field)
		}
	}

	if decoded["cli_version"] != "1.0.0" {
		t.Fatalf("expected cli_version=1.0.0, got %v", decoded["cli_version"])
	}
}
