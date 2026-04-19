package cliruntime

import (
	"testing"

	"github.com/leef-l/brain/cmd/brain/env"
	"github.com/leef-l/brain/sdk/tool"
)

func TestBuildManagedRegistry_CentralExcludesVerifierBrowserAction(t *testing.T) {
	e := env.New(t.TempDir(), env.ModeAuto, nil, nil, false)
	reg := BuildManagedRegistry(nil, e, "central", nil)

	if _, ok := reg.Lookup("verifier.browser_action"); ok {
		t.Fatal("central registry should not expose verifier.browser_action")
	}
}

func TestBuildManagedRegistry_VerifierIncludesVerifierBrowserAction(t *testing.T) {
	e := env.New(t.TempDir(), env.ModeAuto, nil, nil, false)
	reg := BuildManagedRegistry(nil, e, "verifier", nil)

	tt, ok := reg.Lookup("verifier.browser_action")
	if !ok {
		t.Fatal("verifier registry should expose verifier.browser_action")
	}
	if tt.Risk() != tool.RiskMedium {
		t.Fatalf("risk=%v, want %v", tt.Risk(), tool.RiskMedium)
	}
}
