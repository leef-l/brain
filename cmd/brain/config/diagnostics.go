package config

import (
	"os"
	"strings"
)

func ApplyDiagnosticEnv(cfg *Config) {
	if cfg == nil || cfg.Diagnostics == nil {
		return
	}
	applyEnvDefault("BRAIN_DIAG", boolString(cfg.Diagnostics.Enabled))
	if len(cfg.Diagnostics.Categories) > 0 {
		applyEnvDefault("BRAIN_DIAG_CATEGORIES", strings.Join(cfg.Diagnostics.Categories, ","))
	}
	if strings.TrimSpace(cfg.Diagnostics.File) != "" {
		applyEnvDefault("BRAIN_DIAG_FILE", cfg.Diagnostics.File)
	}
	if cfg.Diagnostics.Stderr {
		applyEnvDefault("BRAIN_DIAG_STDERR", "1")
	}
	if strings.TrimSpace(cfg.Diagnostics.Level) != "" {
		applyEnvDefault("BRAIN_DIAG_LEVEL", cfg.Diagnostics.Level)
	}
	if strings.TrimSpace(cfg.Diagnostics.Format) != "" {
		applyEnvDefault("BRAIN_DIAG_FORMAT", cfg.Diagnostics.Format)
	}
}

func applyEnvDefault(key, value string) {
	if strings.TrimSpace(key) == "" || value == "" {
		return
	}
	if strings.TrimSpace(os.Getenv(key)) != "" {
		return
	}
	_ = os.Setenv(key, value)
}

func boolString(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
