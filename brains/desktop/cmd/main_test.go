package main

import (
	"testing"

	"github.com/leef-l/brain/sdk/executionpolicy"
)

func TestDesktopHandlerBuildRegistry_UsesExecutionSpec(t *testing.T) {
	h := newDesktopHandler()

	reg, err := h.buildRegistry(&executionpolicy.ExecutionSpec{
		Workdir: "/tmp",
	})
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}

	for _, name := range []string{
		"desktop.open_path",
		"desktop.list_windows",
		"desktop.send_hotkey",
		"desktop.note",
	} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("missing tool %q", name)
		}
	}
}
