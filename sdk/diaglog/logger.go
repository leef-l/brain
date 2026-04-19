package diaglog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	envEnabled    = "BRAIN_DIAG"
	envCategories = "BRAIN_DIAG_CATEGORIES"
	envFile       = "BRAIN_DIAG_FILE"
	envStderr     = "BRAIN_DIAG_STDERR"
	envLevel      = "BRAIN_DIAG_LEVEL"
	envFormat     = "BRAIN_DIAG_FORMAT"
)

type config struct {
	enabled    bool
	categories map[string]bool
	toStderr   bool
	filePath   string
	level      slog.Level
	format     string
}

var (
	mu     sync.Mutex
	loaded bool
	cfg    config
	writer io.Writer
	closer io.Closer
	logger *slog.Logger
)

func Enabled(category string) bool {
	c := load()
	if !c.enabled {
		return false
	}
	if len(c.categories) == 0 || c.categories["all"] {
		return true
	}
	return c.categories[normalizeCategory(category)]
}

func Logger(category string, attrs ...slog.Attr) *slog.Logger {
	c := load()
	if !c.enabled || !Enabled(category) {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	mu.Lock()
	defer mu.Unlock()
	l := ensureLoggerLocked(c)
	if len(attrs) == 0 {
		return l.With("category", normalizeCategory(category))
	}

	args := make([]any, 0, len(attrs)*2+2)
	args = append(args, "category", normalizeCategory(category))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	return l.With(args...)
}

func Logf(category, format string, args ...interface{}) {
	if !Enabled(category) {
		return
	}
	Logger(category).Log(context.Background(), slog.LevelInfo, fmt.Sprintf(format, args...))
}

func Debug(category, msg string, args ...any) {
	log(category, slog.LevelDebug, msg, args...)
}

func Info(category, msg string, args ...any) {
	log(category, slog.LevelInfo, msg, args...)
}

func Warn(category, msg string, args ...any) {
	log(category, slog.LevelWarn, msg, args...)
}

func Error(category, msg string, args ...any) {
	log(category, slog.LevelError, msg, args...)
}

func Close() {
	mu.Lock()
	defer mu.Unlock()
	if closer != nil {
		_ = closer.Close()
	}
	closer = nil
	writer = nil
	logger = nil
}

func ResetForTests() {
	mu.Lock()
	defer mu.Unlock()
	if closer != nil {
		_ = closer.Close()
	}
	loaded = false
	cfg = config{}
	writer = nil
	closer = nil
	logger = nil
}

func load() config {
	mu.Lock()
	defer mu.Unlock()
	if loaded {
		return cfg
	}
	cfg = config{
		enabled:    parseBoolEnv(envEnabled),
		categories: parseCategories(os.Getenv(envCategories)),
		toStderr:   parseBoolEnv(envStderr),
		filePath:   strings.TrimSpace(os.Getenv(envFile)),
		level:      parseLevel(os.Getenv(envLevel)),
		format:     parseFormat(os.Getenv(envFormat)),
	}
	loaded = true
	return cfg
}

func ensureLoggerLocked(c config) *slog.Logger {
	if logger != nil {
		return logger
	}

	w := ensureWriterLocked(c)
	opts := &slog.HandlerOptions{Level: c.level}
	if c.format == "json" {
		logger = slog.New(slog.NewJSONHandler(w, opts))
		return logger
	}
	logger = slog.New(slog.NewTextHandler(w, opts))
	return logger
}

func ensureWriterLocked(c config) io.Writer {
	if writer != nil {
		return writer
	}

	var writers []io.Writer
	if c.toStderr {
		writers = append(writers, os.Stderr)
	}

	path := c.filePath
	if path == "" {
		path = defaultLogPath()
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				closer = f
				writers = append(writers, f)
			}
		}
	}

	switch len(writers) {
	case 0:
		writer = io.Discard
	case 1:
		writer = writers[0]
	default:
		writer = io.MultiWriter(writers...)
	}
	return writer
}

func defaultLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".brain", "logs", "diagnostics.log")
}

func log(category string, level slog.Level, msg string, args ...any) {
	if !Enabled(category) {
		return
	}
	Logger(category).Log(context.Background(), level, msg, args...)
}

func parseBoolEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseCategories(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		part = normalizeCategory(part)
		if part == "" {
			continue
		}
		out[part] = true
	}
	return out
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func parseFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "json":
		return "json"
	default:
		return "text"
	}
}

func normalizeCategory(category string) string {
	return strings.ToLower(strings.TrimSpace(category))
}
