package main

import (
	"testing"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/shared"
	"github.com/leef-l/brain/sdk/tool"
)

func TestRunBrain_DesktopRegistryExists(t *testing.T) {
	// Desktop brain is not yet in RunBrain switch; verify thin brain wiring works.
	reg := shared.RegisterWithPolicy(agent.KindDesktop,
		tool.NewNoteTool("desktop"),
	)
	if _, ok := reg.Lookup("desktop.note"); !ok {
		t.Fatalf("desktop.note should be available")
	}
}
