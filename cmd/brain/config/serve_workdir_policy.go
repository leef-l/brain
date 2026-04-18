package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ServeWorkdirPolicy string

const (
	ServeWorkdirPolicyConfined ServeWorkdirPolicy = "confined"
	ServeWorkdirPolicyOpen     ServeWorkdirPolicy = "open"
)

func ParseServeWorkdirPolicy(raw string) (ServeWorkdirPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ServeWorkdirPolicyConfined):
		return ServeWorkdirPolicyConfined, nil
	case string(ServeWorkdirPolicyOpen):
		return ServeWorkdirPolicyOpen, nil
	default:
		return "", fmt.Errorf("invalid serve workdir policy %q (must be confined or open)", raw)
	}
}

func ResolveServeWorkdirPolicy(flagValue string, cfg *Config) (ServeWorkdirPolicy, error) {
	raw := strings.TrimSpace(flagValue)
	if raw == "" && cfg != nil {
		raw = strings.TrimSpace(cfg.ServeWorkdirPolicy)
	}
	if raw == "" {
		return ServeWorkdirPolicyConfined, nil
	}
	return ParseServeWorkdirPolicy(raw)
}

func ResolveServeRunWorkdir(defaultWorkdir, requested string, policy ServeWorkdirPolicy) (string, error) {
	base, err := CanonicalDir(defaultWorkdir)
	if err != nil {
		return "", fmt.Errorf("invalid serve workdir root %q: %w", defaultWorkdir, err)
	}

	requested = strings.TrimSpace(requested)
	if requested == "" {
		return base, nil
	}

	var resolved string
	if filepath.IsAbs(requested) {
		resolved = requested
	} else {
		resolved = filepath.Join(base, requested)
	}

	resolved, err = CanonicalDir(resolved)
	if err != nil {
		return "", fmt.Errorf("invalid workdir %q: %w", requested, err)
	}

	if policy == ServeWorkdirPolicyConfined {
		rel, err := filepath.Rel(base, resolved)
		if err != nil {
			return "", fmt.Errorf("resolve workdir %q: %w", requested, err)
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return "", fmt.Errorf("workdir %q escapes serve root %s", resolved, base)
		}
	}

	return resolved, nil
}

func CanonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs), nil
}
