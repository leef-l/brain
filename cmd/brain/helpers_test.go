package main

import (
	"reflect"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
)

func TestSidecarBinaryNamesForOS(t *testing.T) {
	t.Run("unix", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindCode, "linux")
		want := []string{"brain-code"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(linux) = %v, want %v", got, want)
		}
	})

	t.Run("windows", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindCode, "windows")
		want := []string{"brain-code.exe", "brain-code"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(windows) = %v, want %v", got, want)
		}
	})
}
