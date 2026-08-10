package appsettings

import (
	"encoding/json"
	"math"
)

type legacyAppearance struct {
	Theme        int     `json:"Theme"`
	Scale        float64 `json:"Scale"`
	Layout       int     `json:"Layout"`
	AutoCollapse bool    `json:"AutoCollapse"`
}

type legacyPlacement struct {
	Left   float64 `json:"Left"`
	Top    float64 `json:"Top"`
	Width  float64 `json:"Width"`
	Height float64 `json:"Height"`
}

func (store *Store) migrateLegacy() Appearance {
	settings := DefaultAppearance()
	var appearance legacyAppearance
	if readLegacyJSON(store.paths.LegacyAppearance, &appearance) {
		settings.Theme = legacyTheme(appearance.Theme)
		settings.Layout = legacyLayout(appearance.Layout)
		settings.Scale = appearance.Scale
		settings.AutoCollapse = appearance.AutoCollapse
	}

	var placement legacyPlacement
	if readLegacyJSON(store.paths.LegacyPlacement, &placement) {
		settings.MainWindow = migrateLegacyPosition(placement)
	}
	return normalize(settings)
}

func readLegacyJSON(path string, target any) bool {
	if path == "" {
		return false
	}
	contents, exists, err := readLimited(path)
	if err != nil || !exists {
		return false
	}
	return json.Unmarshal(contents, target) == nil
}

func legacyTheme(value int) Theme {
	if value == 1 {
		return ThemeLight
	}
	return ThemeDark
}

func legacyLayout(value int) Layout {
	if value == 1 {
		return LayoutVertical
	}
	return LayoutHorizontal
}

func migrateLegacyPosition(placement legacyPlacement) *WindowPosition {
	values := []float64{
		placement.Left,
		placement.Top,
		placement.Width,
		placement.Height,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil
		}
	}
	if placement.Width <= 0 || placement.Height <= 0 {
		return nil
	}
	x := math.Round(placement.Left)
	y := math.Round(placement.Top)
	if x < math.MinInt32 || x > math.MaxInt32 || y < math.MinInt32 || y > math.MaxInt32 {
		return nil
	}
	return &WindowPosition{
		X:    int(x),
		Y:    int(y),
		Unit: PositionLegacyDIP,
	}
}
