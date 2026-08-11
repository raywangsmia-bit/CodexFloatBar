//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strconv"
	"strings"
)

const textSlotHorizontalAllowance = 3.0

func composeRenderedSurfaceWithPresentation(
	surface *renderedSurface,
	presentation uiPresentation,
) (*renderedSurface, error) {
	if surface == nil {
		return nil, nil
	}
	if len(surface.Surface.Dynamic.Text) == 0 &&
		len(surface.Surface.Dynamic.Progress) == 0 &&
		len(surface.Surface.Dynamic.Cells) == 0 {
		return surface, nil
	}
	bounds := surface.Image.Bounds()
	composed := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(composed, composed.Bounds(), surface.Image, bounds.Min, draw.Src)

	for _, slot := range surface.Surface.Dynamic.Progress {
		percent := max(0, min(100, presentation.Progress[slot.Bind]))
		rect := scaleSlotRect(slot.Rect, surface.Variant.Scale, composed.Bounds())
		rect.Max.X = rect.Min.X + rect.Dx()*percent/100
		value, err := presentationColor(slot.Color, slot.ToneColors, presentation.Tone)
		if err != nil {
			return nil, fmt.Errorf("progress slot %q: %w", slot.Bind, err)
		}
		fillRectangle(composed, rect, value)
	}
	for _, slot := range surface.Surface.Dynamic.Cells {
		if slot.Bind == "statistics.monthCells" &&
			presentation.StatisticsView != statisticsViewMonth {
			if err := drawStatisticsChart(
				composed,
				slot,
				surface.Variant.Scale,
				presentation,
			); err != nil {
				return nil, fmt.Errorf("cell slot %q: %w", slot.Bind, err)
			}
			continue
		}
		levels := presentation.Cells[slot.Bind]
		backgroundColor := slot.BackgroundColor
		if backgroundColor == "" {
			backgroundColor = slot.Colors[0]
		}
		background, err := parseSlotColor(backgroundColor)
		if err != nil {
			return nil, fmt.Errorf("cell slot %q background: %w", slot.Bind, err)
		}
		for index, logicalRect := range slot.Rects {
			level := hiddenMonthCellLevel
			if index < len(levels) {
				level = levels[index]
			}
			rect := scaleSlotRect(logicalRect, surface.Variant.Scale, composed.Bounds())
			if level < 0 {
				fillRectangle(composed, rect, background)
				continue
			}
			level = min(len(slot.Colors)-1, level)
			value, err := parseSlotColor(slot.Colors[level])
			if err != nil {
				return nil, fmt.Errorf("cell slot %q: %w", slot.Bind, err)
			}
			fillRectangle(composed, rect, value)
		}
	}
	if presentation.StatisticsView == statisticsViewDetail {
		if err := drawStatisticsDetailCards(
			composed,
			surface.Surface,
			surface.Variant.Scale,
		); err != nil {
			return nil, fmt.Errorf("statistics detail cards: %w", err)
		}
	}
	for _, slot := range surface.Surface.Dynamic.Text {
		if statisticsDetailBinding(slot.Bind) &&
			presentation.StatisticsView != statisticsViewDetail {
			continue
		}
		if err := drawTextSlotBackground(
			composed,
			slot,
			surface.Variant.Scale,
			presentation,
		); err != nil {
			return nil, fmt.Errorf("text slot %q background: %w", slot.Bind, err)
		}
		value, err := textPresentationColor(slot, presentation)
		if err != nil {
			return nil, fmt.Errorf("text slot %q: %w", slot.Bind, err)
		}
		rect := expandedTextSlotRect(
			slot.Rect,
			slot.Align,
			surface.Variant.Scale,
			composed.Bounds(),
		)
		mask, err := drawTextMask(textMaskRequest{
			Value:        presentation.Text[slot.Bind],
			Width:        rect.Dx(),
			Height:       rect.Dy(),
			FontFamilies: textSlotFontFamilies(slot),
			FontPixels:   max(1, slot.FontSize*surface.Variant.Scale),
			FontWeight:   slot.FontWeight,
			Align:        slot.Align,
		})
		if err != nil {
			return nil, fmt.Errorf("text slot %q: %w", slot.Bind, err)
		}
		blendMask(composed, rect.Min, mask, value)
	}
	result := *surface
	result.Image = composed
	return &result, nil
}

func expandedTextSlotRect(
	logical slotRect,
	align string,
	scale float64,
	bounds image.Rectangle,
) image.Rectangle {
	rect := scaleSlotRect(logical, scale, bounds)
	allowance := max(1, int(textSlotHorizontalAllowance*scale+0.5))
	switch align {
	case "right":
		rect.Min.X -= allowance
	case "center":
		left := allowance / 2
		rect.Min.X -= left
		rect.Max.X += allowance - left
	default:
		rect.Max.X += allowance
	}
	return rect.Intersect(bounds)
}

func drawTextSlotBackground(
	destination *image.NRGBA,
	slot textSlot,
	scale float64,
	presentation uiPresentation,
) error {
	view, isView := statisticsViewForBinding(slot.Bind)
	if !isView {
		return nil
	}
	selected := slot.ToneColors.Danger
	if view == presentation.StatisticsView {
		selected = slot.ToneColors.Warn
	}
	if selected == "" {
		return nil
	}
	value, err := parseSlotColor(selected)
	if err != nil {
		return err
	}
	fillRectangle(
		destination,
		scaleSlotRect(slot.Rect, scale, destination.Bounds()),
		value,
	)
	return nil
}

func textPresentationColor(
	slot textSlot,
	presentation uiPresentation,
) (color.NRGBA, error) {
	view, isView := statisticsViewForBinding(slot.Bind)
	if !isView {
		return presentationColor(slot.Color, slot.ToneColors, presentation.Tone)
	}
	selected := displayOr(slot.ToneColors.Offline, slot.Color)
	if view == presentation.StatisticsView {
		selected = displayOr(slot.ToneColors.Good, slot.Color)
	}
	return parseSlotColor(selected)
}

func statisticsViewForBinding(binding string) (statisticsView, bool) {
	switch binding {
	case "statistics.viewMonth":
		return statisticsViewMonth, true
	case "statistics.viewWeek":
		return statisticsViewWeek, true
	case "statistics.viewCumulative":
		return statisticsViewCumulative, true
	case "statistics.viewDetail":
		return statisticsViewDetail, true
	default:
		return "", false
	}
}

func statisticsDetailBinding(binding string) bool {
	return strings.HasPrefix(binding, "statistics.detail") ||
		strings.HasPrefix(binding, "statistics.label")
}

func drawStatisticsChart(
	destination *image.NRGBA,
	slot cellSlot,
	scale float64,
	presentation uiPresentation,
) error {
	if len(slot.Rects) == 0 || len(slot.Colors) < 5 {
		return fmt.Errorf("invalid statistics chart slot")
	}
	background, err := parseSlotColor(slot.Colors[0])
	if err != nil {
		return err
	}
	baseline, err := parseSlotColor(slot.Colors[1])
	if err != nil {
		return err
	}
	accent, err := parseSlotColor(slot.Colors[4])
	if err != nil {
		return err
	}

	bounds := image.Rectangle{}
	for index, logical := range slot.Rects {
		rect := scaleSlotRect(logical, scale, destination.Bounds())
		if index == 0 {
			bounds = rect
		} else {
			bounds = bounds.Union(rect)
		}
	}
	if bounds.Empty() {
		return nil
	}
	bounds.Min.Y = max(
		destination.Bounds().Min.Y,
		bounds.Min.Y-max(1, int(13*scale+0.5)),
	)
	fillRectangle(destination, bounds, background)
	if presentation.StatisticsView == statisticsViewDetail {
		return nil
	}
	chart := bounds.Inset(max(1, int(4*scale+0.5)))
	if chart.Empty() {
		return nil
	}
	fillRectangle(
		destination,
		image.Rect(chart.Min.X, chart.Max.Y-1, chart.Max.X, chart.Max.Y),
		baseline,
	)
	if len(presentation.ChartValues) == 0 {
		return nil
	}

	switch presentation.StatisticsView {
	case statisticsViewWeek:
		drawStatisticsBars(destination, chart, presentation.ChartValues, accent)
	case statisticsViewCumulative:
		drawStatisticsLine(destination, chart, presentation.ChartValues, accent)
	}
	return nil
}

func drawStatisticsDetailCards(
	destination *image.NRGBA,
	surface bundleSurface,
	scale float64,
) error {
	if strings.TrimSuffix(surface.ID, "-light") != "statistics" {
		return nil
	}
	backgroundValue := "#202428ff"
	if strings.HasSuffix(surface.ID, "-light") {
		backgroundValue = "#f1f4f6ff"
	}
	background, err := parseSlotColor(backgroundValue)
	if err != nil {
		return err
	}
	slots := make(map[string]textSlot, len(surface.Dynamic.Text))
	for _, slot := range surface.Dynamic.Text {
		slots[slot.Bind] = slot
	}
	for _, value := range surface.Dynamic.Text {
		if !strings.HasPrefix(value.Bind, "statistics.detail") {
			continue
		}
		labelBinding := strings.Replace(
			value.Bind,
			"statistics.detail",
			"statistics.label",
			1,
		)
		label, ok := slots[labelBinding]
		if !ok {
			return fmt.Errorf("missing label slot %q", labelBinding)
		}
		left := min(value.Rect.X, label.Rect.X)
		right := max(
			value.Rect.X+value.Rect.Width,
			label.Rect.X+label.Rect.Width,
		)
		top := value.Rect.Y - 10
		bottom := label.Rect.Y + label.Rect.Height + 10
		logical := slotRect{
			X: left, Y: top, Width: right - left, Height: bottom - top,
		}
		rect := scaleSlotRect(logical, scale, destination.Bounds())
		fillRoundedRectangle(
			destination,
			rect,
			max(1, int(6*scale+0.5)),
			background,
		)
	}
	return nil
}

func drawStatisticsBars(
	destination *image.NRGBA,
	bounds image.Rectangle,
	values []int64,
	accent color.NRGBA,
) {
	maximum := maxChartValue(values)
	if maximum <= 0 || len(values) == 0 {
		return
	}
	width := bounds.Dx()
	height := max(1, bounds.Dy()-1)
	for index, value := range values {
		left := bounds.Min.X + index*width/len(values)
		right := bounds.Min.X + (index+1)*width/len(values)
		gap := max(1, (right-left)/5)
		left += gap
		right -= gap
		if right <= left {
			right = left + 1
		}
		barHeight := 1
		if value > 0 {
			barHeight = max(2, int(float64(height)*float64(value)/float64(maximum)+0.5))
		}
		fillRectangle(
			destination,
			image.Rect(left, bounds.Max.Y-barHeight, right, bounds.Max.Y),
			accent,
		)
	}
}

func drawStatisticsLine(
	destination *image.NRGBA,
	bounds image.Rectangle,
	values []int64,
	accent color.NRGBA,
) {
	maximum := maxChartValue(values)
	if maximum <= 0 || len(values) == 0 {
		return
	}
	height := max(1, bounds.Dy()-1)
	points := make([]image.Point, 0, len(values))
	for index, value := range values {
		x := bounds.Min.X + bounds.Dx()/2
		if len(values) > 1 {
			x = bounds.Min.X + index*(bounds.Dx()-1)/(len(values)-1)
		}
		y := bounds.Max.Y - 1 - int(float64(height-1)*float64(value)/float64(maximum)+0.5)
		points = append(points, image.Pt(x, y))
	}
	for index := 1; index < len(points); index++ {
		drawStatisticsLineSegment(destination, points[index-1], points[index], accent)
	}
	for _, point := range points {
		fillRectangle(
			destination,
			image.Rect(point.X-1, point.Y-1, point.X+2, point.Y+2),
			accent,
		)
	}
}

func drawStatisticsLineSegment(
	destination *image.NRGBA,
	start image.Point,
	end image.Point,
	accent color.NRGBA,
) {
	deltaX := abs(end.X - start.X)
	deltaY := -abs(end.Y - start.Y)
	stepX := -1
	if start.X < end.X {
		stepX = 1
	}
	stepY := -1
	if start.Y < end.Y {
		stepY = 1
	}
	errorValue := deltaX + deltaY
	for {
		fillRectangle(
			destination,
			image.Rect(start.X, start.Y, start.X+2, start.Y+2),
			accent,
		)
		if start == end {
			return
		}
		doubled := 2 * errorValue
		if doubled >= deltaY {
			errorValue += deltaY
			start.X += stepX
		}
		if doubled <= deltaX {
			errorValue += deltaX
			start.Y += stepY
		}
	}
}

func maxChartValue(values []int64) int64 {
	var maximum int64
	for _, value := range values {
		maximum = max(maximum, value)
	}
	return maximum
}

func scaleSlotRect(rect slotRect, scale float64, bounds image.Rectangle) image.Rectangle {
	left := int(rect.X*scale + 0.5)
	top := int(rect.Y*scale + 0.5)
	right := int((rect.X+rect.Width)*scale + 0.5)
	bottom := int((rect.Y+rect.Height)*scale + 0.5)
	return image.Rect(left, top, right, bottom).Intersect(bounds)
}

func presentationColor(
	fallback string,
	colors toneColors,
	tone quotaTone,
) (color.NRGBA, error) {
	selected := fallback
	switch tone {
	case quotaToneGood:
		selected = displayOr(colors.Good, fallback)
	case quotaToneWarn:
		selected = displayOr(colors.Warn, fallback)
	case quotaToneDanger:
		selected = displayOr(colors.Danger, fallback)
	case quotaToneOffline:
		selected = displayOr(colors.Offline, fallback)
	}
	return parseSlotColor(selected)
}

func parseSlotColor(value string) (color.NRGBA, error) {
	if !validSlotColor(value) {
		return color.NRGBA{}, fmt.Errorf("invalid color %q", value)
	}
	red, _ := strconv.ParseUint(value[1:3], 16, 8)
	green, _ := strconv.ParseUint(value[3:5], 16, 8)
	blue, _ := strconv.ParseUint(value[5:7], 16, 8)
	alpha := uint64(255)
	if len(value) == 9 {
		alpha, _ = strconv.ParseUint(value[7:9], 16, 8)
	}
	return color.NRGBA{R: uint8(red), G: uint8(green), B: uint8(blue), A: uint8(alpha)}, nil
}

func fillRectangle(destination *image.NRGBA, rect image.Rectangle, value color.NRGBA) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			blendPixel(destination, x, y, value, 255)
		}
	}
}

func fillRoundedRectangle(
	destination *image.NRGBA,
	rect image.Rectangle,
	radius int,
	value color.NRGBA,
) {
	rect = rect.Intersect(destination.Bounds())
	if rect.Empty() {
		return
	}
	radius = min(radius, rect.Dx()/2, rect.Dy()/2)
	if radius <= 0 {
		fillRectangle(destination, rect, value)
		return
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			dx := 0
			switch {
			case x < rect.Min.X+radius:
				dx = rect.Min.X + radius - x
			case x >= rect.Max.X-radius:
				dx = x - (rect.Max.X - radius - 1)
			}
			dy := 0
			switch {
			case y < rect.Min.Y+radius:
				dy = rect.Min.Y + radius - y
			case y >= rect.Max.Y-radius:
				dy = y - (rect.Max.Y - radius - 1)
			}
			if dx == 0 || dy == 0 || dx*dx+dy*dy <= radius*radius {
				blendPixel(destination, x, y, value, 255)
			}
		}
	}
}

func blendMask(
	destination *image.NRGBA,
	origin image.Point,
	mask *image.Alpha,
	value color.NRGBA,
) {
	if mask == nil {
		return
	}
	for y := range mask.Bounds().Dy() {
		for x := range mask.Bounds().Dx() {
			coverage := mask.AlphaAt(mask.Bounds().Min.X+x, mask.Bounds().Min.Y+y).A
			blendPixel(destination, origin.X+x, origin.Y+y, value, coverage)
		}
	}
}

func blendPixel(
	destination *image.NRGBA,
	x int,
	y int,
	value color.NRGBA,
	coverage uint8,
) {
	if !image.Pt(x, y).In(destination.Bounds()) || coverage == 0 || value.A == 0 {
		return
	}
	destinationColor := destination.NRGBAAt(x, y)
	sourceAlpha := float64(value.A) * float64(coverage) / (255 * 255)
	destinationAlpha := float64(destinationColor.A) / 255
	outputAlpha := sourceAlpha + destinationAlpha*(1-sourceAlpha)
	if outputAlpha <= 0 {
		destination.SetNRGBA(x, y, color.NRGBA{})
		return
	}
	blendChannel := func(source uint8, target uint8) uint8 {
		result := (float64(source)*sourceAlpha +
			float64(target)*destinationAlpha*(1-sourceAlpha)) / outputAlpha
		return uint8(max(0, min(255, int(result+0.5))))
	}
	destination.SetNRGBA(x, y, color.NRGBA{
		R: blendChannel(value.R, destinationColor.R),
		G: blendChannel(value.G, destinationColor.G),
		B: blendChannel(value.B, destinationColor.B),
		A: uint8(max(0, min(255, int(outputAlpha*255+0.5)))),
	})
}
