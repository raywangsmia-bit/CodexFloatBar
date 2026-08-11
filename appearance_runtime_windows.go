//go:build windows

package main

import (
	"fmt"
	"math"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appsettings"
	"golang.org/x/sys/windows"
)

type appearanceRuntime struct {
	store    *appsettings.Store
	current  appsettings.Appearance
	disabled bool
	readOnly bool
	dirty    bool
}

func newAppearanceRuntime() *appearanceRuntime {
	runtime := &appearanceRuntime{current: appsettings.DefaultAppearance()}
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return runtime
	}
	runtime.store = appsettings.NewStore(appsettings.DefaultPaths(localAppData))
	return runtime
}

func (runtime *appearanceRuntime) disable() {
	if runtime != nil {
		runtime.disabled = true
	}
}

func (runtime *appearanceRuntime) load(dpi uint32) error {
	if runtime == nil || runtime.disabled || runtime.store == nil {
		return nil
	}
	settings, err := runtime.store.Load()
	runtime.current = settings
	positions := []*appsettings.WindowPosition{
		runtime.current.MainWindow,
		runtime.current.HorizontalMainWindow,
		runtime.current.VerticalMainWindow,
		runtime.current.StatisticsWindow,
	}
	for _, position := range positions {
		if convertLegacyPosition(position, dpi) {
			runtime.dirty = true
		}
	}
	if err != nil {
		runtime.readOnly = true
	}
	return err
}

func convertLegacyPosition(position *appsettings.WindowPosition, dpi uint32) bool {
	if position == nil || position.Unit != appsettings.PositionLegacyDIP {
		return false
	}
	scale := float64(max(uint32(96), dpi)) / 96
	position.X = int(math.Round(float64(position.X) * scale))
	position.Y = int(math.Round(float64(position.Y) * scale))
	position.Unit = appsettings.PositionPhysicalPixels
	return true
}

func (runtime *appearanceRuntime) targetScale(dpi uint32) float64 {
	if runtime == nil {
		return float64(dpi) / 96
	}
	return float64(dpi) / 96 * runtime.current.Scale
}

func (runtime *appearanceRuntime) save() error {
	if runtime == nil || runtime.disabled || runtime.readOnly ||
		runtime.store == nil || !runtime.dirty {
		return nil
	}
	if err := runtime.store.Save(runtime.current); err != nil {
		return fmt.Errorf("saving appearance: %w", err)
	}
	runtime.dirty = false
	return nil
}

func (runtime *appearanceRuntime) setTheme(theme appsettings.Theme) {
	if runtime != nil && runtime.current.Theme != theme {
		runtime.current.Theme = theme
		runtime.dirty = true
	}
}

func (runtime *appearanceRuntime) setLayout(layout appsettings.Layout) {
	if runtime != nil && runtime.current.Layout != layout {
		runtime.current.Layout = layout
		runtime.current.MainWindow = runtime.mainPosition(layout)
		runtime.dirty = true
	}
}

func (runtime *appearanceRuntime) setScale(scale float64) {
	if runtime == nil {
		return
	}
	normalized := max(appsettings.MinScale, min(appsettings.MaxScale, scale))
	if math.Abs(runtime.current.Scale-normalized) > 0.0001 {
		runtime.current.Scale = normalized
		runtime.dirty = true
	}
}

func (runtime *appearanceRuntime) setAutoCollapse(enabled bool) {
	if runtime != nil && runtime.current.AutoCollapse != enabled {
		runtime.current.AutoCollapse = enabled
		runtime.dirty = true
	}
}

func (runtime *appearanceRuntime) setFollowCodex(enabled bool) {
	if runtime != nil && runtime.current.FollowCodex != enabled {
		runtime.current.FollowCodex = enabled
		runtime.dirty = true
	}
}

func (runtime *appearanceRuntime) setAccountExpiryDate(value string) {
	if runtime != nil && runtime.current.AccountExpiryDate != value {
		runtime.current.AccountExpiryDate = value
		runtime.dirty = true
	}
}

func (runtime *appearanceRuntime) setAccountExpiryReminder(enabled bool) {
	if runtime != nil && runtime.current.AccountExpiryReminder != enabled {
		runtime.current.AccountExpiryReminder = enabled
		runtime.dirty = true
	}
}

func (runtime *appearanceRuntime) setMainPosition(position geometryPoint) {
	if runtime == nil {
		return
	}
	runtime.setMainPositionForLayout(runtime.current.Layout, position)
}

func (runtime *appearanceRuntime) setMainPositionForLayout(
	layout appsettings.Layout,
	position geometryPoint,
) {
	if runtime == nil {
		return
	}
	next := &appsettings.WindowPosition{
		X:    position.X,
		Y:    position.Y,
		Unit: appsettings.PositionPhysicalPixels,
	}
	var current **appsettings.WindowPosition
	if layout == appsettings.LayoutVertical {
		current = &runtime.current.VerticalMainWindow
	} else {
		current = &runtime.current.HorizontalMainWindow
	}
	if samePosition(*current, next) {
		return
	}
	*current = next
	if runtime.current.Layout == layout {
		copy := *next
		runtime.current.MainWindow = &copy
	}
	runtime.dirty = true
}

func (runtime *appearanceRuntime) mainPosition(
	layout appsettings.Layout,
) *appsettings.WindowPosition {
	if runtime == nil {
		return nil
	}
	return runtime.current.MainWindowForLayout(layout)
}

func (runtime *appearanceRuntime) setStatisticsPosition(position geometryPoint) {
	if runtime == nil {
		return
	}
	next := &appsettings.WindowPosition{
		X:    position.X,
		Y:    position.Y,
		Unit: appsettings.PositionPhysicalPixels,
	}
	if !samePosition(runtime.current.StatisticsWindow, next) {
		runtime.current.StatisticsWindow = next
		runtime.dirty = true
	}
}

func samePosition(left *appsettings.WindowPosition, right *appsettings.WindowPosition) bool {
	return left != nil && right != nil &&
		left.X == right.X && left.Y == right.Y && left.Unit == right.Unit
}
