package appsettings

import "math"

const (
	MinScale = 0.82
	MaxScale = 1.18
)

type Theme string

const (
	ThemeDark  Theme = "dark"
	ThemeLight Theme = "light"
)

type Layout string

const (
	LayoutHorizontal Layout = "horizontal"
	LayoutVertical   Layout = "vertical"
)

type PositionUnit string

const (
	PositionPhysicalPixels PositionUnit = "physical-pixels"
	PositionLegacyDIP      PositionUnit = "legacy-dip"
)

type WindowPosition struct {
	X    int          `json:"x"`
	Y    int          `json:"y"`
	Unit PositionUnit `json:"unit"`
}

type Appearance struct {
	Theme                Theme           `json:"theme"`
	Layout               Layout          `json:"layout"`
	Scale                float64         `json:"scale"`
	AutoCollapse         bool            `json:"autoCollapse"`
	FollowCodex          bool            `json:"followCodex"`
	MainWindow           *WindowPosition `json:"mainWindow,omitempty"`
	HorizontalMainWindow *WindowPosition `json:"horizontalMainWindow,omitempty"`
	VerticalMainWindow   *WindowPosition `json:"verticalMainWindow,omitempty"`
	StatisticsWindow     *WindowPosition `json:"statisticsWindow,omitempty"`
}

func DefaultAppearance() Appearance {
	return Appearance{
		Theme:        ThemeDark,
		Layout:       LayoutHorizontal,
		Scale:        1,
		AutoCollapse: false,
		FollowCodex:  true,
	}
}

func normalize(settings Appearance) Appearance {
	result := settings
	if result.Theme != ThemeDark && result.Theme != ThemeLight {
		result.Theme = ThemeDark
	}
	if result.Layout != LayoutHorizontal && result.Layout != LayoutVertical {
		result.Layout = LayoutHorizontal
	}
	if math.IsNaN(result.Scale) || math.IsInf(result.Scale, 0) {
		result.Scale = 1
	}
	result.Scale = max(MinScale, min(MaxScale, result.Scale))
	legacyMainWindow := validPosition(result.MainWindow)
	result.HorizontalMainWindow = validPosition(result.HorizontalMainWindow)
	result.VerticalMainWindow = validPosition(result.VerticalMainWindow)
	if result.Layout == LayoutHorizontal && result.HorizontalMainWindow == nil {
		result.HorizontalMainWindow = clonePosition(legacyMainWindow)
	}
	if result.Layout == LayoutVertical && result.VerticalMainWindow == nil {
		result.VerticalMainWindow = clonePosition(legacyMainWindow)
	}
	result.MainWindow = result.MainWindowForLayout(result.Layout)
	result.StatisticsWindow = validPosition(result.StatisticsWindow)
	return result
}

func (settings Appearance) MainWindowForLayout(layout Layout) *WindowPosition {
	if layout == LayoutVertical {
		return clonePosition(settings.VerticalMainWindow)
	}
	return clonePosition(settings.HorizontalMainWindow)
}

func clonePosition(position *WindowPosition) *WindowPosition {
	if position == nil {
		return nil
	}
	copy := *position
	return &copy
}

func validPosition(position *WindowPosition) *WindowPosition {
	if position == nil {
		return nil
	}
	if int64(position.X) < math.MinInt32 || int64(position.X) > math.MaxInt32 ||
		int64(position.Y) < math.MinInt32 || int64(position.Y) > math.MaxInt32 {
		return nil
	}
	copy := *position
	if copy.Unit != PositionLegacyDIP && copy.Unit != PositionPhysicalPixels {
		copy.Unit = PositionPhysicalPixels
	}
	return &copy
}
