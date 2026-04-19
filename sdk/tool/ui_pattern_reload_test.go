package tool

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPatternLibraryReloadIfChangedSeesCrossProcessUpsert(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "ui_patterns.db")

	reader, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary(reader): %v", err)
	}
	defer reader.Close()
	reader.reloadInterval = 0

	writer, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary(writer): %v", err)
	}
	defer writer.Close()

	if err := writer.Upsert(context.Background(), &UIPattern{
		ID:       "cross_process_pattern",
		Category: "auth",
		Source:   "user",
		AppliesWhen: MatchCondition{
			SiteHost: "example.com",
		},
		ElementRoles: map[string]ElementDescriptor{
			"submit": {Role: "button", Name: "Login"},
		},
		ActionSequence: []ActionStep{{Tool: "browser.click", TargetRole: "submit"}},
	}); err != nil {
		t.Fatalf("writer.Upsert: %v", err)
	}

	got := reader.GetAny("cross_process_pattern")
	if got == nil {
		t.Fatal("reader.GetAny() = nil, want cross-process upsert after reload")
	}
}
