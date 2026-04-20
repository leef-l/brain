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

func TestLooksLikeSliderDrag(t *testing.T) {
	from := &elementGeometry{Width: 24, Height: 24, CenterY: 50}
	to := &elementGeometry{Left: 10, Top: 35, Right: 210, Bottom: 65, Width: 200, Height: 30, CenterY: 50}
	if !looksLikeSliderDrag(from, to) {
		t.Fatal("looksLikeSliderDrag() = false, want true")
	}
}

func TestComputeDragDestinationAutoUsesSliderStrategy(t *testing.T) {
	from := &elementGeometry{Width: 24, Height: 24, CenterY: 50}
	to := &elementGeometry{Left: 10, Top: 35, Right: 210, Bottom: 65, Width: 200, Height: 30, CenterY: 50}
	x, y, err := computeDragDestination("auto", from, to)
	if err != nil {
		t.Fatalf("computeDragDestination() err = %v", err)
	}
	if x <= to.CenterX {
		t.Fatalf("x = %v, want value near right edge", x)
	}
	if y != from.CenterY {
		t.Fatalf("y = %v, want %v", y, from.CenterY)
	}
}

func TestComputeDragDestinationCenter(t *testing.T) {
	to := &elementGeometry{CenterX: 90, CenterY: 45}
	x, y, err := computeDragDestination("center", nil, to)
	if err != nil {
		t.Fatalf("computeDragDestination() err = %v", err)
	}
	if x != 90 || y != 45 {
		t.Fatalf("got (%v,%v), want (90,45)", x, y)
	}
}

func TestLooksLikeSliderDragAllowsTallerContainerTarget(t *testing.T) {
	from := &elementGeometry{Width: 24, Height: 24, CenterY: 50}
	to := &elementGeometry{Left: 10, Top: 20, Right: 210, Bottom: 80, Width: 200, Height: 60, CenterY: 50}
	if !looksLikeSliderDrag(from, to) {
		t.Fatal("looksLikeSliderDrag() = false, want true for container-like slider target")
	}
}

func TestComputeDragDestinationSliderUsesRightEdgeForContainer(t *testing.T) {
	from := &elementGeometry{Width: 24, Height: 24, CenterY: 50}
	to := &elementGeometry{Left: 10, Top: 20, Right: 210, Bottom: 80, Width: 200, Height: 60, CenterY: 50}
	x, y, err := computeDragDestination("slider", from, to)
	if err != nil {
		t.Fatalf("computeDragDestination() err = %v", err)
	}
	if x <= 180 {
		t.Fatalf("x = %v, want near right edge of container target", x)
	}
	if y != from.CenterY {
		t.Fatalf("y = %v, want %v", y, from.CenterY)
	}
}

func TestPostDragVerificationRuleLikeSuccess(t *testing.T) {
	from := &elementGeometry{Width: 24, Height: 24, CenterX: 22, CenterY: 50}
	current := &elementGeometry{Width: 24, Height: 24, CenterX: 192, CenterY: 50}
	movement := current.CenterX - from.CenterX
	if movement <= 0 {
		t.Fatalf("movement = %v, want positive", movement)
	}
	distanceToExpected := 6.0
	sourceMoved := movement >= maxFloat(4, from.Width*0.2)
	if !sourceMoved {
		t.Fatal("sourceMoved = false, want true")
	}
	verified := sourceMoved && distanceToExpected <= maxFloat(12, from.Width)
	if !verified {
		t.Fatal("verified = false, want true")
	}
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
