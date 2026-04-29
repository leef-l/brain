package env

import (
	"testing"
)

func TestParsePermissionMode(t *testing.T) {
	cases := []struct {
		input   string
		want    PermissionMode
		wantErr bool
	}{
		{"plan", ModePlan, false},
		{"default", ModeDefault, false},
		{"accept-edits", ModeAcceptEdits, false},
		{"acceptedits", ModeAcceptEdits, false},
		{"auto", ModeAuto, false},
		{"restricted", ModeRestricted, false},
		{"bypass-permissions", ModeBypassPermissions, false},
		{"bypasspermissions", ModeBypassPermissions, false},
		{"bypass", ModeBypassPermissions, false},
		{"acceptedits+sandbox", ModeAcceptEdits, false},
		{"bypasspermissions+sandbox", ModeBypassPermissions, false},
		{"unknown", "", true},
	}
	for _, c := range cases {
		got, err := ParsePermissionMode(c.input)
		if c.wantErr {
			if err == nil {
				t.Fatalf("ParsePermissionMode(%q) expected error, got nil", c.input)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParsePermissionMode(%q) unexpected error: %v", c.input, err)
		}
		if got != c.want {
			t.Fatalf("ParsePermissionMode(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestPermissionModeLabel(t *testing.T) {
	cases := []struct {
		mode PermissionMode
		want string
	}{
		{ModePlan, "plan (read-only)"},
		{ModeDefault, "default (always confirm)"},
		{ModeAcceptEdits, "accept-edits (auto-approve edits)"},
		{ModeAuto, "auto (sandboxed auto-approve)"},
		{ModeRestricted, "restricted (file-policy enforced)"},
		{ModeBypassPermissions, "bypass-permissions (no confirmation, sandbox still enforced)"},
		{"unknown", "unknown"},
	}
	for _, c := range cases {
		got := c.mode.Label()
		if got != c.want {
			t.Fatalf("Label() for %q = %q, want %q", c.mode, got, c.want)
		}
	}
}

func TestCycleMode(t *testing.T) {
	cases := []struct {
		start PermissionMode
		want  PermissionMode
	}{
		{ModePlan, ModeDefault},
		{ModeDefault, ModeAcceptEdits},
		{ModeAcceptEdits, ModeAuto},
		{ModeAuto, ModeRestricted},
		{ModeRestricted, ModeBypassPermissions},
		{ModeBypassPermissions, ModePlan},
		{"unknown", ModeDefault},
	}
	for _, c := range cases {
		got := CycleMode(c.start)
		if got != c.want {
			t.Fatalf("CycleMode(%q) = %q, want %q", c.start, got, c.want)
		}
	}
}

func TestEnvironmentAutoApprove(t *testing.T) {
	e := New("/tmp", ModeDefault, nil, nil, false)
	if !e.AutoApprove(ToolClassRead) {
		t.Fatal("expected ModeDefault to auto-approve read")
	}
	if e.AutoApprove(ToolClassEdit) {
		t.Fatal("expected ModeDefault to NOT auto-approve edit")
	}

	e.Mode = ModeBypassPermissions
	if !e.AutoApprove(ToolClassCommand) {
		t.Fatal("expected ModeBypassPermissions to auto-approve command")
	}

	e.Mode = ModePlan
	if !e.AutoApprove(ToolClassRead) {
		t.Fatal("expected ModePlan to auto-approve read")
	}
	if e.AutoApprove(ToolClassEdit) {
		t.Fatal("expected ModePlan to NOT auto-approve edit")
	}

	e.Mode = ModeAuto
	if !e.AutoApprove(ToolClassCommand) {
		t.Fatal("expected ModeAuto to auto-approve command")
	}
}

func TestFmtJSON(t *testing.T) {
	got := FmtJSON(map[string]interface{}{"key": "value"})
	if got != `{"key":"value"}` {
		t.Fatalf("expected JSON object, got %s", got)
	}

	got = FmtJSON(func() {})
	if got != "{}" {
		t.Fatalf("expected {} on marshal error, got %s", got)
	}
}

func TestAllModes(t *testing.T) {
	if len(AllModes) != 6 {
		t.Fatalf("expected 6 modes, got %d", len(AllModes))
	}
	seen := make(map[PermissionMode]bool)
	for _, m := range AllModes {
		if seen[m] {
			t.Fatalf("duplicate mode %q in AllModes", m)
		}
		seen[m] = true
	}
}

func TestEnvironmentAllowsDelegation(t *testing.T) {
	e := New("/tmp", ModeDefault, nil, nil, false)
	if !e.AllowsDelegation() {
		t.Fatal("expected AllowsDelegation=true when FilePolicy is nil")
	}
}
