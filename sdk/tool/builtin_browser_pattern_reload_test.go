package tool

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshSharedPatternLibraryIfChanged_UsesCustomDSNAndReloads(t *testing.T) {
	patternLibMu.Lock()
	prevLib := patternLib
	prevErr := patternLibErr
	prevDSN := patternLibDSN
	prevCheckedAt := patternLibCheckedAt
	patternLib = nil
	patternLibErr = nil
	patternLibDSN = ""
	patternLibCheckedAt = time.Time{}
	patternLibMu.Unlock()
	t.Cleanup(func() {
		patternLibMu.Lock()
		patternLib = prevLib
		patternLibErr = prevErr
		patternLibDSN = prevDSN
		patternLibCheckedAt = prevCheckedAt
		patternLibMu.Unlock()
	})

	dsn := filepath.Join(t.TempDir(), "ui_patterns.db")
	t.Setenv(envUIPatternDBPath, dsn)

	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	defer lib.Close()

	if err := lib.Upsert(context.Background(), &UIPattern{
		ID:       "reload_login",
		Category: "auth",
		Source:   "learned",
		Enabled:  true,
		AppliesWhen: MatchCondition{
			URLPattern: "/login",
		},
		ElementRoles: map[string]ElementDescriptor{
			"submit": {CSS: "button[type=submit]"},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "submit"},
		},
	}); err != nil {
		t.Fatalf("Upsert first pattern: %v", err)
	}

	if err := RefreshSharedPatternLibraryIfChanged(); err != nil {
		t.Fatalf("RefreshSharedPatternLibraryIfChanged first: %v", err)
	}
	shared := SharedPatternLibrary()
	if shared == nil || shared.Get("reload_login") == nil {
		t.Fatal("shared pattern library did not load first pattern from custom DSN")
	}
	shared.reloadInterval = 0

	writer, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary writer: %v", err)
	}
	defer writer.Close()
	if err := writer.Upsert(context.Background(), &UIPattern{
		ID:       "reload_checkout",
		Category: "commerce",
		Source:   "learned",
		Enabled:  true,
		AppliesWhen: MatchCondition{
			URLPattern: "/checkout",
		},
		ElementRoles: map[string]ElementDescriptor{
			"submit": {CSS: "button[type=submit]"},
		},
		ActionSequence: []ActionStep{
			{Tool: "browser.click", TargetRole: "submit"},
		},
	}); err != nil {
		t.Fatalf("Upsert second pattern: %v", err)
	}

	patternLibMu.Lock()
	patternLibCheckedAt = patternLibCheckedAt.Add(-2 * patternLibraryRefreshInterval)
	patternLibMu.Unlock()

	if err := RefreshSharedPatternLibraryIfChanged(); err != nil {
		t.Fatalf("RefreshSharedPatternLibraryIfChanged reload: %v", err)
	}
	if SharedPatternLibrary().Get("reload_checkout") == nil {
		t.Fatal("shared pattern library did not observe newly inserted pattern after reload")
	}
}
