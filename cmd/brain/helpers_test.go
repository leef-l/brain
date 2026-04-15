package main

import (
	"reflect"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
)

func TestSidecarBinaryNamesForOS(t *testing.T) {
	t.Run("unix", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindCode, "linux")
		want := []string{"brain-code-sidecar", "brain-code"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(code, linux) = %v, want %v", got, want)
		}
	})

	t.Run("unix_data", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindData, "linux")
		want := []string{"brain-data-sidecar", "brain-data"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(data, linux) = %v, want %v", got, want)
		}
	})

	t.Run("windows", func(t *testing.T) {
		got := sidecarBinaryNamesForOS(agent.KindCode, "windows")
		want := []string{"brain-code-sidecar.exe", "brain-code-sidecar", "brain-code.exe", "brain-code"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sidecarBinaryNamesForOS(code, windows) = %v, want %v", got, want)
		}
	})
}
