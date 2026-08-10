package main

import "math"

const collapsedVisibleThickness = 8

type geometryPoint struct {
	X int
	Y int
}

type windowGeometry struct {
	X      int
	Y      int
	Width  int
	Height int
}

type geometrySize struct {
	Width  int
	Height int
}

type workArea struct {
	X      int
	Y      int
	Width  int
	Height int
}

func clampGeometry(geometry windowGeometry, areas []workArea) windowGeometry {
	if len(areas) == 0 {
		return geometry
	}
	if area, ok := bestIntersectingWorkArea(geometry, areas); ok {
		return fitGeometry(geometry, area)
	}

	area := areas[0]
	geometry.X = area.X + max(0, (area.Width-geometry.Width)/2)
	geometry.Y = area.Y + max(0, (area.Height-geometry.Height)/2)
	return geometry
}

func bestIntersectingWorkArea(geometry windowGeometry, areas []workArea) (workArea, bool) {
	bestArea := workArea{}
	bestOverlap := 0
	for _, area := range areas {
		overlap := intersectionPixels(geometry, area)
		if overlap > bestOverlap {
			bestArea = area
			bestOverlap = overlap
		}
	}
	return bestArea, bestOverlap > 0
}

func intersectionPixels(geometry windowGeometry, area workArea) int {
	windowRight := geometry.X + geometry.Width
	windowBottom := geometry.Y + geometry.Height
	areaRight := area.X + area.Width
	areaBottom := area.Y + area.Height
	overlapWidth := max(0, min(windowRight, areaRight)-max(geometry.X, area.X))
	overlapHeight := max(0, min(windowBottom, areaBottom)-max(geometry.Y, area.Y))
	return overlapWidth * overlapHeight
}

func geometryInsideWorkArea(geometry windowGeometry, area workArea) bool {
	return geometry.X >= area.X &&
		geometry.Y >= area.Y &&
		geometry.X+geometry.Width <= area.X+area.Width &&
		geometry.Y+geometry.Height <= area.Y+area.Height
}

func geometriesOverlap(first windowGeometry, second windowGeometry) bool {
	return intersectionPixels(first, workArea{
		X:      second.X,
		Y:      second.Y,
		Width:  second.Width,
		Height: second.Height,
	}) > 0
}

func visiblyIntersects(geometry windowGeometry, area workArea) bool {
	windowRight := geometry.X + geometry.Width
	windowBottom := geometry.Y + geometry.Height
	areaRight := area.X + area.Width
	areaBottom := area.Y + area.Height
	overlapWidth := max(0, min(windowRight, areaRight)-max(geometry.X, area.X))
	overlapHeight := max(0, min(windowBottom, areaBottom)-max(geometry.Y, area.Y))
	minimumWidth := min(32, geometry.Width)
	minimumHeight := min(32, geometry.Height)
	return overlapWidth >= minimumWidth && overlapHeight >= minimumHeight
}

func resizeGeometryPreservingDock(
	geometry windowGeometry,
	width int,
	height int,
	area workArea,
) windowGeometry {
	const tolerance = 16
	leftDocked := abs(geometry.X-area.X) <= tolerance
	rightDocked := abs((geometry.X+geometry.Width)-(area.X+area.Width)) <= tolerance
	topDocked := abs(geometry.Y-area.Y) <= tolerance
	bottomDocked := abs((geometry.Y+geometry.Height)-(area.Y+area.Height)) <= tolerance

	geometry.Width = width
	geometry.Height = height
	switch {
	case leftDocked:
		geometry.X = area.X
	case rightDocked:
		geometry.X = area.X + area.Width - width
	}
	switch {
	case topDocked:
		geometry.Y = area.Y
	case bottomDocked:
		geometry.Y = area.Y + area.Height - height
	}
	return fitGeometry(geometry, area)
}

func fitGeometry(geometry windowGeometry, area workArea) windowGeometry {
	maximumX := area.X + max(0, area.Width-geometry.Width)
	maximumY := area.Y + max(0, area.Height-geometry.Height)
	geometry.X = min(max(geometry.X, area.X), maximumX)
	geometry.Y = min(max(geometry.Y, area.Y), maximumY)
	return geometry
}

func dockAuxiliaryStack(
	anchor windowGeometry,
	sizes []geometrySize,
	area workArea,
	vertical bool,
	gap int,
) []windowGeometry {
	if len(sizes) == 0 {
		return []windowGeometry{}
	}
	gap = max(0, gap)
	stackWidth := 0
	stackHeight := 0
	for index, size := range sizes {
		stackWidth = max(stackWidth, size.Width)
		stackHeight += size.Height
		if index > 0 {
			stackHeight += gap
		}
	}

	stack := windowGeometry{Width: stackWidth, Height: stackHeight}
	if vertical {
		stack.X = dockStackHorizontally(anchor, stack, area, gap)
		stack.Y = anchor.Y
	} else {
		stack.X = anchor.X + anchor.Width - stack.Width
		stack.Y = dockStackVertically(anchor, stack, area, gap)
	}
	stack = fitGeometry(stack, area)

	geometries := make([]windowGeometry, 0, len(sizes))
	y := stack.Y
	for _, size := range sizes {
		geometries = append(geometries, windowGeometry{
			X:      stack.X,
			Y:      y,
			Width:  size.Width,
			Height: size.Height,
		})
		y += size.Height + gap
	}
	return geometries
}

func dockStackHorizontally(
	anchor windowGeometry,
	stack windowGeometry,
	area workArea,
	gap int,
) int {
	areaRight := area.X + area.Width
	anchorRight := anchor.X + anchor.Width
	right := anchorRight + gap
	left := anchor.X - gap - stack.Width
	rightFits := right+stack.Width <= areaRight
	leftFits := left >= area.X
	if rightFits || (!leftFits && areaRight-anchorRight >= anchor.X-area.X) {
		return right
	}
	return left
}

func dockStackVertically(
	anchor windowGeometry,
	stack windowGeometry,
	area workArea,
	gap int,
) int {
	areaBottom := area.Y + area.Height
	anchorBottom := anchor.Y + anchor.Height
	below := anchorBottom + gap
	above := anchor.Y - gap - stack.Height
	belowFits := below+stack.Height <= areaBottom
	aboveFits := above >= area.Y
	if belowFits || (!aboveFits && areaBottom-anchorBottom >= anchor.Y-area.Y) {
		return below
	}
	return above
}

func collapsedPosition(geometry windowGeometry, area workArea) geometryPoint {
	position, _ := collapsedPositionForWorkAreas(geometry, area, nil)
	return position
}

func collapsedPositionForWorkAreas(
	geometry windowGeometry,
	area workArea,
	areas []workArea,
) (geometryPoint, bool) {
	type candidate struct {
		distance      float64
		position      geometryPoint
		visiblePixels int
	}
	candidates := []candidate{
		{
			distance: math.Abs(float64(geometry.X - area.X)),
			position: geometryPoint{
				X: area.X - geometry.Width + collapsedVisibleThickness,
				Y: geometry.Y,
			},
			visiblePixels: collapsedVisibleThickness * geometry.Height,
		},
		{
			distance: math.Abs(float64(
				(area.X + area.Width) - (geometry.X + geometry.Width),
			)),
			position: geometryPoint{
				X: area.X + area.Width - collapsedVisibleThickness,
				Y: geometry.Y,
			},
			visiblePixels: collapsedVisibleThickness * geometry.Height,
		},
		{
			distance: math.Abs(float64(geometry.Y - area.Y)),
			position: geometryPoint{
				X: geometry.X,
				Y: area.Y - geometry.Height + collapsedVisibleThickness,
			},
			visiblePixels: collapsedVisibleThickness * geometry.Width,
		},
		{
			distance: math.Abs(float64(
				(area.Y + area.Height) - (geometry.Y + geometry.Height),
			)),
			position: geometryPoint{
				X: geometry.X,
				Y: area.Y + area.Height - collapsedVisibleThickness,
			},
			visiblePixels: collapsedVisibleThickness * geometry.Width,
		},
	}

	for len(candidates) > 0 {
		nearest := 0
		for index := 1; index < len(candidates); index++ {
			if candidates[index].distance < candidates[nearest].distance {
				nearest = index
			}
		}
		selected := candidates[nearest]
		if !collapseTargetCrossesMonitor(
			geometry,
			selected.position,
			area,
			areas,
			selected.visiblePixels,
		) {
			return selected.position, true
		}
		candidates = append(candidates[:nearest], candidates[nearest+1:]...)
	}
	return geometryPoint{}, false
}

func collapseTargetCrossesMonitor(
	geometry windowGeometry,
	position geometryPoint,
	selectedArea workArea,
	areas []workArea,
	visiblePixels int,
) bool {
	target := geometry
	target.X = position.X
	target.Y = position.Y
	for _, area := range areas {
		if area == selectedArea {
			continue
		}
		if intersectionPixels(target, area) > visiblePixels {
			return true
		}
	}
	return false
}

func interpolate(start geometryPoint, target geometryPoint, step int, total int) geometryPoint {
	if total <= 0 || step >= total {
		return target
	}

	progress := float64(step) / float64(total)
	eased := 1 - math.Pow(1-progress, 3)
	return geometryPoint{
		X: start.X + int(math.Round(float64(target.X-start.X)*eased)),
		Y: start.Y + int(math.Round(float64(target.Y-start.Y)*eased)),
	}
}
