package cdp

import (
	"runtime"
	"testing"
)

func TestBrowserCandidates_LinuxIncludesUngoogledChromium(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only candidate list")
	}

	candidates := browserCandidates()
	want := map[string]bool{
		"ungoogled-chromium":          false,
		"/usr/bin/ungoogled-chromium": false,
	}
	for _, candidate := range candidates {
		if _, ok := want[candidate]; ok {
			want[candidate] = true
		}
	}
	for candidate, found := range want {
		if !found {
			t.Fatalf("candidate %q missing from browserCandidates()", candidate)
		}
	}
}
