package config

import (
	"os"
	"testing"
)

func TestApplyDiagnosticEnv(t *testing.T) {
	t.Setenv("BRAIN_DIAG", "")
	t.Setenv("BRAIN_DIAG_CATEGORIES", "")
	t.Setenv("BRAIN_DIAG_FILE", "")
	t.Setenv("BRAIN_DIAG_STDERR", "")
	t.Setenv("BRAIN_DIAG_LEVEL", "")
	t.Setenv("BRAIN_DIAG_FORMAT", "")

	ApplyDiagnosticEnv(&Config{
		Diagnostics: &DiagnosticsConfig{
			Enabled:    true,
			Categories: []string{"process", "llm"},
			File:       "/tmp/brain-diag.log",
			Stderr:     true,
			Level:      "debug",
			Format:     "json",
		},
	})

	if got := os.Getenv("BRAIN_DIAG"); got != "1" {
		t.Fatalf("BRAIN_DIAG=%q, want 1", got)
	}
	if got := os.Getenv("BRAIN_DIAG_CATEGORIES"); got != "process,llm" {
		t.Fatalf("BRAIN_DIAG_CATEGORIES=%q, want process,llm", got)
	}
	if got := os.Getenv("BRAIN_DIAG_FILE"); got != "/tmp/brain-diag.log" {
		t.Fatalf("BRAIN_DIAG_FILE=%q, want /tmp/brain-diag.log", got)
	}
	if got := os.Getenv("BRAIN_DIAG_STDERR"); got != "1" {
		t.Fatalf("BRAIN_DIAG_STDERR=%q, want 1", got)
	}
	if got := os.Getenv("BRAIN_DIAG_LEVEL"); got != "debug" {
		t.Fatalf("BRAIN_DIAG_LEVEL=%q, want debug", got)
	}
	if got := os.Getenv("BRAIN_DIAG_FORMAT"); got != "json" {
		t.Fatalf("BRAIN_DIAG_FORMAT=%q, want json", got)
	}
}
