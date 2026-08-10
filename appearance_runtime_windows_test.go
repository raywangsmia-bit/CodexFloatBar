//go:build windows

package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appsettings"
)

func TestAppearanceRuntimeConvertsLegacyDIPAndCombinesScaleWithDPI(t *testing.T) {
	runtime := &appearanceRuntime{current: appsettings.DefaultAppearance()}
	runtime.current.Scale = 1.1
	position := &appsettings.WindowPosition{
		X:    -100,
		Y:    80,
		Unit: appsettings.PositionLegacyDIP,
	}
	if !convertLegacyPosition(position, 144) {
		t.Fatal("legacy DIP position was not converted")
	}
	if position.X != -150 || position.Y != 120 ||
		position.Unit != appsettings.PositionPhysicalPixels {
		t.Fatalf("converted position = %+v", position)
	}
	if got := runtime.targetScale(144); math.Abs(got-1.65) > 0.0001 {
		t.Fatalf("target scale = %.4f, want 1.65", got)
	}
}

func TestAppearanceRuntimeDoesNotOverwriteAfterLoadFailure(t *testing.T) {
	root := t.TempDir()
	nativePath := filepath.Join(root, "settings-as-directory")
	if err := os.Mkdir(nativePath, 0o755); err != nil {
		t.Fatal(err)
	}
	runtime := &appearanceRuntime{
		store:   appsettings.NewStore(appsettings.Paths{Native: nativePath}),
		current: appsettings.DefaultAppearance(),
	}
	if err := runtime.load(96); err == nil {
		t.Fatal("directory-backed settings unexpectedly loaded")
	}
	if !runtime.readOnly {
		t.Fatal("failed load did not disable settings writes for this process")
	}
	runtime.setTheme(appsettings.ThemeLight)
	if err := runtime.save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(nativePath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("failed load path was overwritten")
	}
}

func TestAppearanceRuntimeTracksIndependentWindowPositions(t *testing.T) {
	runtime := &appearanceRuntime{current: appsettings.DefaultAppearance()}
	runtime.setMainPosition(geometryPoint{X: 10, Y: 20})
	runtime.setLayout(appsettings.LayoutVertical)
	runtime.setMainPosition(geometryPoint{X: -120, Y: 80})
	runtime.setStatisticsPosition(geometryPoint{X: -300, Y: 40})
	horizontal := runtime.mainPosition(appsettings.LayoutHorizontal)
	vertical := runtime.mainPosition(appsettings.LayoutVertical)
	if horizontal.X != 10 || horizontal.Y != 20 {
		t.Fatalf("horizontal position = %+v", horizontal)
	}
	if vertical.X != -120 || vertical.Y != 80 {
		t.Fatalf("vertical position = %+v", vertical)
	}
	if runtime.current.StatisticsWindow.X != -300 ||
		runtime.current.StatisticsWindow.Y != 40 {
		t.Fatalf("statistics position = %+v", runtime.current.StatisticsWindow)
	}
	runtime.setLayout(appsettings.LayoutHorizontal)
	if runtime.current.MainWindow.X != 10 || runtime.current.MainWindow.Y != 20 {
		t.Fatalf("restored active main position = %+v", runtime.current.MainWindow)
	}
}
