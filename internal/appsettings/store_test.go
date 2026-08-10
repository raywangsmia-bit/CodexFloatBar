package appsettings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultPathsKeepNativeAndWPFSettingsSeparate(t *testing.T) {
	root := t.TempDir()
	paths := DefaultPaths(root)
	if paths.Native != filepath.Join(root, "CodexFloatingBar.Next", "settings.json") {
		t.Fatalf("native path = %q", paths.Native)
	}
	if paths.LegacyAppearance != filepath.Join(root, "CodexFloatingBar", "appearance.json") {
		t.Fatalf("legacy appearance path = %q", paths.LegacyAppearance)
	}
	if paths.LegacyPlacement != filepath.Join(root, "CodexFloatingBar", "window-placement.json") {
		t.Fatalf("legacy placement path = %q", paths.LegacyPlacement)
	}
}

func TestLoadMigratesWPFSettingsOnceWithoutModifyingThem(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	appearance := []byte(`{
  "Theme": 1,
  "Scale": 1.1,
  "Layout": 1,
  "AutoCollapse": true
}`)
	placement := []byte(`{
  "Left": -1279.6,
  "Top": 41.5,
  "Width": 180,
  "Height": 245
}`)
	writeFixture(t, paths.LegacyAppearance, appearance)
	writeFixture(t, paths.LegacyPlacement, placement)

	store := NewStore(paths)
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Appearance{
		Theme:        ThemeLight,
		Layout:       LayoutVertical,
		Scale:        1.1,
		AutoCollapse: true,
		FollowCodex:  true,
		MainWindow: &WindowPosition{
			X:    -1280,
			Y:    42,
			Unit: PositionLegacyDIP,
		},
		VerticalMainWindow: &WindowPosition{
			X:    -1280,
			Y:    42,
			Unit: PositionLegacyDIP,
		},
		StatisticsWindow: nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
	assertFileEquals(t, paths.LegacyAppearance, appearance)
	assertFileEquals(t, paths.LegacyPlacement, placement)

	writeFixture(t, paths.LegacyAppearance, []byte(`{"Theme":0,"Scale":0.9,"Layout":0}`))
	second, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("second load reread WPF settings: %+v", second)
	}
	nativeContents, err := os.ReadFile(paths.Native)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nativeContents), `"unit": "legacy-dip"`) {
		t.Fatalf("migrated position did not preserve its DIP unit: %s", nativeContents)
	}
}

func TestStorePreservesIndependentMainWindowPositions(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	store := NewStore(paths)
	horizontal := &WindowPosition{X: 40, Y: 60, Unit: PositionPhysicalPixels}
	vertical := &WindowPosition{X: -320, Y: 90, Unit: PositionPhysicalPixels}
	statistics := &WindowPosition{X: 600, Y: 110, Unit: PositionPhysicalPixels}
	if err := store.Save(Appearance{
		Theme:                ThemeDark,
		Layout:               LayoutVertical,
		Scale:                1,
		HorizontalMainWindow: horizontal,
		VerticalMainWindow:   vertical,
		StatisticsWindow:     statistics,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.HorizontalMainWindow, horizontal) ||
		!reflect.DeepEqual(got.VerticalMainWindow, vertical) {
		t.Fatalf("main window positions = %+v / %+v", got.HorizontalMainWindow, got.VerticalMainWindow)
	}
	if !reflect.DeepEqual(got.MainWindow, vertical) {
		t.Fatalf("active main window position = %+v, want %+v", got.MainWindow, vertical)
	}
	if !reflect.DeepEqual(got.StatisticsWindow, statistics) {
		t.Fatalf("shared statistics position = %+v, want %+v", got.StatisticsWindow, statistics)
	}
}

func TestCorruptNativeSettingsFallBackWithoutRereadingWPF(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	writeFixture(
		t,
		paths.LegacyAppearance,
		[]byte(`{"Theme":1,"Scale":1.1,"Layout":1,"AutoCollapse":true}`),
	)
	writeFixture(t, paths.Native, []byte(`{"schema":1,"theme":`))

	got, err := NewStore(paths).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultAppearance()) {
		t.Fatalf("settings = %+v, want defaults", got)
	}
}

func TestFollowCodexDefaultsOnAndPersistsDisabled(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	writeFixture(
		t,
		paths.Native,
		[]byte(`{"schema":1,"theme":"dark","layout":"horizontal","scale":1}`),
	)
	store := NewStore(paths)
	legacy, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.FollowCodex {
		t.Fatal("legacy settings should default to following Codex")
	}

	legacy.FollowCodex = false
	if err := store.Save(legacy); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.FollowCodex {
		t.Fatal("disabled follow Codex setting was not preserved")
	}
}

func TestSaveAtomicallyReplacesAndNormalizesSettings(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	store := NewStore(paths)
	if err := store.Save(Appearance{
		Theme:        ThemeLight,
		Layout:       LayoutVertical,
		Scale:        9,
		AutoCollapse: true,
		MainWindow: &WindowPosition{
			X:    -900,
			Y:    30,
			Unit: PositionPhysicalPixels,
		},
		StatisticsWindow: &WindowPosition{
			X:    -610,
			Y:    30,
			Unit: PositionPhysicalPixels,
		},
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	wantStatistics := WindowPosition{
		X:    -610,
		Y:    30,
		Unit: PositionPhysicalPixels,
	}
	if first.Scale != MaxScale || first.StatisticsWindow == nil ||
		*first.StatisticsWindow != wantStatistics {
		t.Fatalf("first save was not normalized or preserved: %+v", first)
	}
	if err := store.Save(Appearance{
		Theme:        ThemeDark,
		Layout:       LayoutHorizontal,
		Scale:        0.9,
		AutoCollapse: false,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Appearance{
		Theme:        ThemeDark,
		Layout:       LayoutHorizontal,
		Scale:        0.9,
		AutoCollapse: false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(filepath.Dir(paths.Native))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(paths.Native) {
		t.Fatalf("settings directory contains temporary files: %+v", entries)
	}
	contents, err := os.ReadFile(paths.Native)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"schema": 1`) {
		t.Fatalf("settings do not contain schema: %s", contents)
	}
}

func TestMissingAndCorruptLegacyFilesCreateDefaultNativeMarker(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	writeFixture(t, paths.LegacyAppearance, []byte(`not-json`))
	writeFixture(t, paths.LegacyPlacement, []byte(`{"Left":0,"Top":0,"Width":0,"Height":0}`))

	got, err := NewStore(paths).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultAppearance()) {
		t.Fatalf("settings = %+v, want defaults", got)
	}
	if _, err := os.Stat(paths.Native); err != nil {
		t.Fatalf("native migration marker was not created: %v", err)
	}
}

func writeFixture(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q changed during migration", path)
	}
}
