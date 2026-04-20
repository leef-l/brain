package cdp

import "testing"

func TestChooseInitialPageTarget_PrefersAboutBlank(t *testing.T) {
	targetID, ok := chooseInitialPageTarget([]pageTargetInfo{
		{TargetID: "page-1", Type: "page", URL: "https://www.baidu.com"},
		{TargetID: "page-2", Type: "page", URL: "about:blank"},
	})
	if !ok {
		t.Fatal("chooseInitialPageTarget() ok = false, want true")
	}
	if targetID != "page-2" {
		t.Fatalf("chooseInitialPageTarget() = %q, want page-2", targetID)
	}
}

func TestChooseInitialPageTarget_IgnoresNonBlankPages(t *testing.T) {
	targetID, ok := chooseInitialPageTarget([]pageTargetInfo{
		{TargetID: "page-1", Type: "page", URL: "https://www.google.com"},
		{TargetID: "worker-1", Type: "service_worker", URL: "https://www.google.com/sw.js"},
	})
	if ok {
		t.Fatalf("chooseInitialPageTarget() = %q, want no match", targetID)
	}
}
