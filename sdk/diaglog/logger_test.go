package diaglog

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogf_WritesOnlyEnabledCategory(t *testing.T) {
	ResetForTests()
	t.Setenv(envEnabled, "1")
	t.Setenv(envCategories, "llm,process")
	path := filepath.Join(t.TempDir(), "diag.log")
	t.Setenv(envFile, path)

	Logf("llm", "hello %s", "world")
	Logf("tool", "should not appear")
	Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "category=llm") || !strings.Contains(text, "msg=\"hello world\"") {
		t.Fatalf("missing llm line in %q", text)
	}
	if strings.Contains(text, "should not appear") {
		t.Fatalf("unexpected disabled category line in %q", text)
	}
}

func TestLogger_JSONAndLevel(t *testing.T) {
	ResetForTests()
	t.Setenv(envEnabled, "1")
	t.Setenv(envCategories, "process")
	t.Setenv(envLevel, "warn")
	t.Setenv(envFormat, "json")
	path := filepath.Join(t.TempDir(), "diag.json")
	t.Setenv(envFile, path)

	Info("process", "boot", "kind", "browser")
	Warn("process", "degraded", "kind", "browser", "retry", 1)
	Logger("process", slog.String("kind", "browser")).Error("crashed", "exit_code", 2)
	Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "\"msg\":\"boot\"") {
		t.Fatalf("info line should be filtered by warn level: %q", text)
	}
	if !strings.Contains(text, "\"msg\":\"degraded\"") || !strings.Contains(text, "\"retry\":1") {
		t.Fatalf("missing warn json line: %q", text)
	}
	if !strings.Contains(text, "\"msg\":\"crashed\"") || !strings.Contains(text, "\"exit_code\":2") {
		t.Fatalf("missing error json line: %q", text)
	}
}
