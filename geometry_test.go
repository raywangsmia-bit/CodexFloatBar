package main

import "testing"

func TestClampGeometrySupportsNegativeMonitorCoordinates(t *testing.T) {
	areas := []workArea{
		{X: -1920, Y: 0, Width: 1920, Height: 1040},
		{X: 0, Y: 0, Width: 1920, Height: 1040},
	}
	geometry := windowGeometry{X: -1800, Y: 20, Width: 437, Height: 58}

	if got := clampGeometry(geometry, areas); got != geometry {
		t.Fatalf("negative-coordinate placement changed: got %+v want %+v", got, geometry)
	}
}

func TestClampGeometryRecentresOffscreenWindow(t *testing.T) {
	areas := []workArea{{X: 0, Y: 0, Width: 1920, Height: 1040}}
	geometry := windowGeometry{X: 8000, Y: 8000, Width: 437, Height: 58}
	got := clampGeometry(geometry, areas)

	if got.X != 741 || got.Y != 491 {
		t.Fatalf("unexpected centred placement: %+v", got)
	}
}

func TestClampGeometryRejectsBarelyVisibleWindow(t *testing.T) {
	areas := []workArea{{X: 0, Y: 0, Width: 1920, Height: 1040}}
	geometry := windowGeometry{X: 1910, Y: 100, Width: 437, Height: 58}
	got := clampGeometry(geometry, areas)

	if got.X != 1483 || got.Y != 100 {
		t.Fatalf("partially visible placement was not fitted: %+v", got)
	}
}

func TestResizeGeometryPreservesRightDock(t *testing.T) {
	area := workArea{X: 0, Y: 0, Width: 1920, Height: 1040}
	vertical := windowGeometry{X: 1650, Y: 200, Width: 270, Height: 368}
	got := resizeGeometryPreservingDock(vertical, 656, 87, area)
	want := windowGeometry{X: 1264, Y: 200, Width: 656, Height: 87}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestClampGeometryFitsLargestMonitorIntersection(t *testing.T) {
	areas := []workArea{
		{X: -1920, Y: 0, Width: 1920, Height: 1040},
		{X: 0, Y: 0, Width: 1920, Height: 1040},
	}
	geometry := windowGeometry{X: -100, Y: 100, Width: 656, Height: 87}
	got := clampGeometry(geometry, areas)
	if got.X != 0 {
		t.Fatalf("geometry was not fitted to the monitor with the largest overlap: %+v", got)
	}
}

func TestCollapsedPositionLeavesEightPixelsVisible(t *testing.T) {
	area := workArea{X: 0, Y: 0, Width: 1920, Height: 1040}
	geometry := windowGeometry{X: 1483, Y: 300, Width: 437, Height: 58}
	want := geometryPoint{X: 1912, Y: 300}

	if got := collapsedPosition(geometry, area); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCollapsedPositionAvoidsInternalMonitorSeam(t *testing.T) {
	left := workArea{X: -1920, Y: 0, Width: 1920, Height: 1040}
	areas := []workArea{
		left,
		{X: 0, Y: 0, Width: 1920, Height: 1040},
	}
	geometry := windowGeometry{X: -437, Y: 100, Width: 437, Height: 58}

	got, ok := collapsedPositionForWorkAreas(geometry, left, areas)
	if !ok {
		t.Fatal("no external collapse edge was selected")
	}
	want := geometryPoint{X: -437, Y: -50}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestInterpolateEndsAtTarget(t *testing.T) {
	start := geometryPoint{X: -300, Y: 10}
	target := geometryPoint{X: 100, Y: 200}
	if got := interpolate(start, target, 10, 10); got != target {
		t.Fatalf("got %+v want %+v", got, target)
	}
}

func TestIsDockedUsesAllWorkAreaEdges(t *testing.T) {
	area := workArea{X: -1920, Y: -200, Width: 1920, Height: 1040}
	tests := []windowGeometry{
		{X: -1920, Y: 100, Width: 437, Height: 58},
		{X: -437, Y: 100, Width: 437, Height: 58},
		{X: -1200, Y: -200, Width: 437, Height: 58},
		{X: -1200, Y: 782, Width: 437, Height: 58},
	}
	for _, geometry := range tests {
		if !isDocked(geometry, area) {
			t.Fatalf("geometry was not detected as docked: %+v", geometry)
		}
	}
}

func TestDockAuxiliaryStackBelowHorizontalWindow(t *testing.T) {
	area := workArea{X: 0, Y: 0, Width: 1920, Height: 1040}
	main := windowGeometry{X: 900, Y: 100, Width: 656, Height: 87}
	sizes := []geometrySize{
		{Width: 520, Height: 300},
		{Width: 360, Height: 96},
	}

	got := dockAuxiliaryStack(main, sizes, area, false, 12)
	want := []windowGeometry{
		{X: 1036, Y: 199, Width: 520, Height: 300},
		{X: 1036, Y: 511, Width: 360, Height: 96},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d geometries want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("geometry %d = %+v want %+v", index, got[index], want[index])
		}
	}
}

func TestDockAuxiliaryStackMovesAboveAtBottomEdge(t *testing.T) {
	area := workArea{X: 0, Y: 0, Width: 1920, Height: 1040}
	main := windowGeometry{X: 1264, Y: 953, Width: 656, Height: 87}
	sizes := []geometrySize{{Width: 520, Height: 300}}

	got := dockAuxiliaryStack(main, sizes, area, false, 12)
	want := windowGeometry{X: 1400, Y: 641, Width: 520, Height: 300}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestDockAuxiliaryStackUsesLeftOfVerticalWindow(t *testing.T) {
	area := workArea{X: -1920, Y: -200, Width: 1920, Height: 1040}
	main := windowGeometry{X: -270, Y: 100, Width: 270, Height: 368}
	sizes := []geometrySize{
		{Width: 520, Height: 300},
		{Width: 360, Height: 96},
	}

	got := dockAuxiliaryStack(main, sizes, area, true, 12)
	want := []windowGeometry{
		{X: -802, Y: 100, Width: 520, Height: 300},
		{X: -802, Y: 412, Width: 360, Height: 96},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d geometries want %d", len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("geometry %d = %+v want %+v", index, got[index], want[index])
		}
	}
}

func TestDockAuxiliaryStackReturnsInitializedEmptySlice(t *testing.T) {
	got := dockAuxiliaryStack(
		windowGeometry{},
		[]geometrySize{},
		workArea{Width: 1920, Height: 1040},
		false,
		8,
	)
	if got == nil || len(got) != 0 {
		t.Fatalf("got %#v want initialized empty slice", got)
	}
}

func TestGeometryInsideWorkAreaSupportsNegativeCoordinates(t *testing.T) {
	area := workArea{X: -1920, Y: -200, Width: 1920, Height: 1040}
	inside := windowGeometry{X: -1900, Y: -180, Width: 520, Height: 254}
	offscreen := windowGeometry{X: -1900, Y: -180, Width: 2000, Height: 254}

	if !geometryInsideWorkArea(inside, area) {
		t.Fatalf("geometry should be inside work area: %+v", inside)
	}
	if geometryInsideWorkArea(offscreen, area) {
		t.Fatalf("geometry should extend beyond work area: %+v", offscreen)
	}
}

func TestGeometriesOverlapRequiresPositiveIntersection(t *testing.T) {
	first := windowGeometry{X: 10, Y: 10, Width: 100, Height: 50}
	touching := windowGeometry{X: 110, Y: 10, Width: 100, Height: 50}
	overlapping := windowGeometry{X: 109, Y: 10, Width: 100, Height: 50}

	if geometriesOverlap(first, touching) {
		t.Fatal("touching window edges should not count as overlap")
	}
	if !geometriesOverlap(first, overlapping) {
		t.Fatal("positive-area intersection should count as overlap")
	}
}
