package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type serveWorkdirPolicy string

const (
	serveWorkdirPolicyConfined serveWorkdirPolicy = "confined"
	serveWorkdirPolicyOpen     serveWorkdirPolicy = "open"
)

func parseServeWorkdirPolicy(raw string) (serveWorkdirPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(serveWorkdirPolicyConfined):
		return serveWorkdirPolicyConfined, nil
	case string(serveWorkdirPolicyOpen):
		return serveWorkdirPolicyOpen, nil
	default:
		return "", fmt.Errorf("invalid serve workdir policy %q (must be confined or open)", raw)
	}
}

func resolveServeWorkdirPolicy(flagValue string, cfg *brainConfig) (serveWorkdirPolicy, error) {
	raw := strings.TrimSpace(flagValue)
	if raw == "" && cfg != nil {
		raw = strings.TrimSpace(cfg.ServeWorkdirPolicy)
	}
	if raw == "" {
		return serveWorkdirPolicyConfined, nil
	}
	return parseServeWorkdirPolicy(raw)
}

func resolveServeRunWorkdir(defaultWorkdir, requested string, policy serveWorkdirPolicy) (string, error) {
	base, err := canonicalDir(defaultWorkdir)
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

	resolved, err = canonicalDir(resolved)
	if err != nil {
		return "", fmt.Errorf("invalid workdir %q: %w", requested, err)
	}

	if policy == serveWorkdirPolicyConfined {
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

func canonicalDir(path string) (string, error) {
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
