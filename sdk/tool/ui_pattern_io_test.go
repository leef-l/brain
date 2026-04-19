package tool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func newTestLibIO(t *testing.T) *PatternLibrary {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "io.db")
	lib, err := NewPatternLibrary(dsn)
	if err != nil {
		t.Fatalf("NewPatternLibrary: %v", err)
	}
	t.Cleanup(func() { lib.Close() })
	return lib
}

// Export → Import roundtrip on a fresh library should end up with the same
// pattern IDs and categories as before.
func TestExportImportRoundtrip(t *testing.T) {
	src := newTestLibIO(t)

	ctx := context.Background()
	// Add a user pattern so we can verify non-seed passes through.
	userP := &UIPattern{
		ID: "user-login-x", Category: "auth", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: "/login"},
		ElementRoles: map[string]ElementDescriptor{"f": {CSS: "input"}},
		ActionSequence: []ActionStep{{Tool: "browser.click", TargetRole: "f"}},
	}
	if err := src.Upsert(ctx, userP); err != nil {
		t.Fatalf("seed user pattern: %v", err)
	}

	blob, err := src.Export(ctx, ExportFilter{Origin: "test"})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var env PatternExport
	if err := json.Unmarshal(blob, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.SchemaVersion != PatternExportSchemaVersion {
		t.Errorf("schema_version = %q, want %q", env.SchemaVersion, PatternExportSchemaVersion)
	}
	if env.Origin != "test" {
		t.Errorf("origin = %q, want test", env.Origin)
	}
	if env.Count == 0 || env.Count != len(env.Patterns) {
		t.Errorf("count/patterns mismatch: count=%d len=%d", env.Count, len(env.Patterns))
	}
	// Exported patterns must have zeroed stats to avoid leaking telemetry.
	for _, p := range env.Patterns {
		if p.Stats.HitCount != 0 || p.Stats.SuccessCount != 0 || p.Stats.FailureCount != 0 {
			t.Errorf("exported pattern %s carries stats: %+v", p.ID, p.Stats)
		}
	}

	// Fresh library, import into it.
	dst := newTestLibIO(t)
	report, err := dst.Import(ctx, blob, ImportOptions{Mode: ImportModeMerge, AllowOverwriteBuiltin: true})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Rejected != 0 {
		t.Errorf("unexpected rejections: %+v", report)
	}
	// dst starts seeded too, so seed IDs in env will be skipped in merge mode.
	// The user-login-x id must have landed.
	if got := dst.GetAny("user-login-x"); got == nil {
		t.Fatalf("user-login-x not imported")
	}
}

// ExportByCategory must only include rows of the requested category and must
// skip learned + disabled rows.
func TestExportByCategoryFilters(t *testing.T) {
	lib := newTestLibIO(t)
	ctx := context.Background()

	// learned pattern: must not appear in exportByCategory output
	learned := &UIPattern{
		ID: "learned-auth-1", Category: "auth", Source: "learned",
		AppliesWhen: MatchCondition{URLPattern: "/x"},
		ActionSequence: []ActionStep{{Tool: "browser.click"}},
	}
	if err := lib.Upsert(ctx, learned); err != nil {
		t.Fatalf("upsert learned: %v", err)
	}
	// disabled pattern: also excluded.
	disabled := &UIPattern{
		ID: "disabled-auth-1", Category: "auth", Source: "user",
		AppliesWhen: MatchCondition{URLPattern: "/y"},
		ActionSequence: []ActionStep{{Tool: "browser.click"}},
	}
	if err := lib.Upsert(ctx, disabled); err != nil {
		t.Fatalf("upsert disabled: %v", err)
	}
	if err := lib.SetEnabled(ctx, "disabled-auth-1", false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	blob, err := lib.ExportByCategory(ctx, "auth")
	if err != nil {
		t.Fatalf("ExportByCategory: %v", err)
	}
	var env PatternExport
	if err := json.Unmarshal(blob, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, p := range env.Patterns {
		if p.Category != "auth" {
			t.Errorf("pattern %s category=%s, want auth", p.ID, p.Category)
		}
		if p.Source == "learned" {
			t.Errorf("learned pattern %s leaked into export", p.ID)
		}
		if p.ID == "disabled-auth-1" {
			t.Errorf("disabled pattern leaked into export")
		}
	}
}

// Dry-run must never write; Added/Updated counts must reflect what *would*
// happen, Written must be 0.
func TestImportDryRun(t *testing.T) {
	src := newTestLibIO(t)
	ctx := context.Background()

	p := &UIPattern{
		ID: "dry-run-new", Category: "form", Source: "user",
		AppliesWhen: MatchCondition{Has: []string{"form"}},
		ActionSequence: []ActionStep{{Tool: "browser.click"}},
	}
	if err := src.Upsert(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	blob, err := src.Export(ctx, ExportFilter{IDs: []string{"dry-run-new"}})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newTestLibIO(t)
	report, err := dst.Import(ctx, blob, ImportOptions{Mode: ImportModeDryRun})
	if err != nil {
		t.Fatalf("Import dry-run: %v", err)
	}
	if report.Written != 0 {
		t.Errorf("dry-run Written=%d, want 0", report.Written)
	}
	if report.Added != 1 {
		t.Errorf("dry-run Added=%d, want 1", report.Added)
	}
	if got := dst.GetAny("dry-run-new"); got != nil {
		t.Errorf("dry-run wrote pattern: %+v", got)
	}
}

// Merge mode keeps existing rows on conflict; their stats are preserved.
func TestImportMergeKeepsExisting(t *testing.T) {
	src := newTestLibIO(t)
	ctx := context.Background()

	p := &UIPattern{
		ID: "conflict-merge", Category: "form", Source: "user",
		Description: "from src",
		AppliesWhen: MatchCondition{Has: []string{"form"}},
		ActionSequence: []ActionStep{{Tool: "browser.click"}},
	}
	if err := src.Upsert(ctx, p); err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	blob, err := src.Export(ctx, ExportFilter{IDs: []string{"conflict-merge"}})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newTestLibIO(t)
	existing := &UIPattern{
		ID: "conflict-merge", Category: "form", Source: "user",
		Description: "existing in dst",
		AppliesWhen: MatchCondition{Has: []string{"form"}},
		ActionSequence: []ActionStep{{Tool: "browser.type"}},
	}
	if err := dst.Upsert(ctx, existing); err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	if err := dst.RecordExecution(ctx, "conflict-merge", true, 50); err != nil {
		t.Fatalf("record: %v", err)
	}

	report, err := dst.Import(ctx, blob, ImportOptions{Mode: ImportModeMerge})
	if err != nil {
		t.Fatalf("Import merge: %v", err)
	}
	if report.Skipped != 1 || report.Added != 0 || report.Updated != 0 {
		t.Errorf("merge report unexpected: %+v", report)
	}
	got := dst.GetAny("conflict-merge")
	if got == nil {
		t.Fatalf("pattern vanished after merge import")
	}
	if got.Description != "existing in dst" {
		t.Errorf("merge clobbered existing description: %q", got.Description)
	}
	if got.Stats.SuccessCount != 1 {
		t.Errorf("merge reset stats: %+v", got.Stats)
	}
}

// Overwrite mode replaces user rows but preserves their stats; seed rows are
// protected unless AllowOverwriteBuiltin=true.
func TestImportOverwriteAndBuiltinProtection(t *testing.T) {
	src := newTestLibIO(t)
	ctx := context.Background()

	// user row to overwrite
	usr := &UIPattern{
		ID: "overwrite-user", Category: "form", Source: "user",
		Description: "new-description",
		AppliesWhen: MatchCondition{Has: []string{"form"}},
		ActionSequence: []ActionStep{{Tool: "browser.click"}},
	}
	if err := src.Upsert(ctx, usr); err != nil {
		t.Fatalf("upsert src usr: %v", err)
	}
	// fake seed row in the export envelope — IDs overlap with dst's seed
	fakeSeed := &UIPattern{
		ID: "login_username_password", Category: "auth", Source: "seed",
		Description: "MALICIOUS OVERRIDE",
		Enabled:     true,
		AppliesWhen: MatchCondition{URLPattern: "/evil"},
		ElementRoles: map[string]ElementDescriptor{"x": {CSS: "input"}},
		ActionSequence: []ActionStep{{Tool: "browser.type", TargetRole: "x"}},
	}
	if err := src.Upsert(ctx, fakeSeed); err != nil {
		t.Fatalf("upsert src seed: %v", err)
	}

	blob, err := src.Export(ctx, ExportFilter{IDs: []string{"overwrite-user", "login_username_password"}})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newTestLibIO(t)
	existing := &UIPattern{
		ID: "overwrite-user", Category: "form", Source: "user",
		Description: "old-description",
		AppliesWhen: MatchCondition{Has: []string{"form"}},
		ActionSequence: []ActionStep{{Tool: "browser.click"}},
	}
	if err := dst.Upsert(ctx, existing); err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	if err := dst.RecordExecution(ctx, "overwrite-user", true, 100); err != nil {
		t.Fatalf("record: %v", err)
	}

	report, err := dst.Import(ctx, blob, ImportOptions{Mode: ImportModeOverwrite})
	if err != nil {
		t.Fatalf("Import overwrite: %v", err)
	}
	// seed conflict rejected; user row updated.
	if report.Updated != 1 {
		t.Errorf("Updated=%d, want 1: %+v", report.Updated, report)
	}
	if report.Rejected != 1 {
		t.Errorf("Rejected=%d, want 1 (builtin protection): %+v", report.Rejected, report)
	}
	seen := false
	for _, id := range report.RejectedIDs {
		if id == "login_username_password" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("builtin protection did not reject login_username_password: %+v", report.RejectedIDs)
	}

	// dst's seed row for login_username_password must still describe auth.
	seed := dst.GetAny("login_username_password")
	if seed == nil {
		t.Fatalf("seed row missing")
	}
	if strings.Contains(seed.Description, "MALICIOUS") {
		t.Errorf("builtin overwritten despite protection: %q", seed.Description)
	}

	// Overwritten user row: description replaced, stats preserved.
	got := dst.GetAny("overwrite-user")
	if got.Description != "new-description" {
		t.Errorf("overwrite did not replace description: %q", got.Description)
	}
	if got.Stats.SuccessCount != 1 {
		t.Errorf("overwrite reset stats: %+v", got.Stats)
	}
}

// AllowOverwriteBuiltin=true must let seed rows be replaced.
func TestImportAllowOverwriteBuiltin(t *testing.T) {
	src := newTestLibIO(t)
	ctx := context.Background()

	override := &UIPattern{
		ID: "login_username_password", Category: "auth", Source: "seed",
		Description: "team-patched",
		Enabled:     true,
		AppliesWhen: MatchCondition{URLPattern: "/login"},
		ElementRoles: map[string]ElementDescriptor{"x": {CSS: "input"}},
		ActionSequence: []ActionStep{{Tool: "browser.type", TargetRole: "x"}},
	}
	if err := src.Upsert(ctx, override); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	blob, err := src.Export(ctx, ExportFilter{IDs: []string{"login_username_password"}})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst := newTestLibIO(t)
	report, err := dst.Import(ctx, blob, ImportOptions{Mode: ImportModeOverwrite, AllowOverwriteBuiltin: true})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Updated != 1 || report.Rejected != 0 {
		t.Errorf("unexpected report: %+v", report)
	}
	got := dst.GetAny("login_username_password")
	if got == nil || got.Description != "team-patched" {
		t.Errorf("builtin not overwritten with allow flag: %+v", got)
	}
}

// Envelope without schema_version or with unsupported major must be rejected
// with an error from Import itself.
func TestImportSchemaValidation(t *testing.T) {
	dst := newTestLibIO(t)
	ctx := context.Background()

	cases := []struct {
		name string
		body string
	}{
		{"missing schema_version", `{"count":0,"patterns":[]}`},
		{"unsupported major", `{"schema_version":"9.0.0","count":0,"patterns":[]}`},
		{"not json", `not-json`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := dst.Import(ctx, []byte(c.body), ImportOptions{Mode: ImportModeMerge}); err == nil {
				t.Errorf("expected error for %s", c.name)
			}
		})
	}
}

// Invalid pattern (empty id / all-empty body) must be rejected without
// aborting the whole batch.
func TestImportRejectsInvalidPattern(t *testing.T) {
	ctx := context.Background()
	blob, err := json.Marshal(PatternExport{
		SchemaVersion: PatternExportSchemaVersion,
		Count:         2,
		Patterns: []UIPattern{
			{ID: "", Category: "x"},
			{ID: "ok-1", Category: "form",
				AppliesWhen: MatchCondition{Has: []string{"form"}},
				ActionSequence: []ActionStep{{Tool: "browser.click"}}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dst := newTestLibIO(t)
	report, err := dst.Import(ctx, blob, ImportOptions{Mode: ImportModeMerge})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Added != 1 {
		t.Errorf("Added=%d, want 1", report.Added)
	}
	if report.Rejected != 1 {
		t.Errorf("Rejected=%d, want 1", report.Rejected)
	}
}
