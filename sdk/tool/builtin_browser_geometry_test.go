package tool

import "testing"

func TestNewBrowserToolsIncludesGeometryTool(t *testing.T) {
	var found Tool
	for _, candidate := range NewBrowserTools() {
		if candidate.Name() == "browser.geometry" {
			found = candidate
			break
		}
	}
	if found == nil {
		t.Fatal("browser.geometry missing from NewBrowserTools()")
	}
	if got := found.Schema().OutputSchema; len(got) == 0 {
		t.Fatal("browser.geometry missing OutputSchema")
	}
}

func TestNormalizeElementBox(t *testing.T) {
	got := normalizeElementBox([4]float64{10, 20, 30, 40})
	if got.Left != 10 || got.Top != 20 {
		t.Fatalf("left/top = (%v,%v), want (10,20)", got.Left, got.Top)
	}
	if got.Right != 40 || got.Bottom != 60 {
		t.Fatalf("right/bottom = (%v,%v), want (40,60)", got.Right, got.Bottom)
	}
	if got.CenterX != 25 || got.CenterY != 40 {
		t.Fatalf("center = (%v,%v), want (25,40)", got.CenterX, got.CenterY)
	}
}
