//go:build windows

package appsettings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveReplacesExistingNativeSettingsOnWindows(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	writeFixture(t, paths.Native, []byte(`{"schema":1,"theme":"old"}`))

	store := NewStore(paths)
	want := Appearance{
		Theme:        ThemeLight,
		Layout:       LayoutVertical,
		Scale:        1.1,
		AutoCollapse: true,
		MainWindow: &WindowPosition{
			X:    -1440,
			Y:    60,
			Unit: PositionPhysicalPixels,
		},
		VerticalMainWindow: &WindowPosition{
			X:    -1440,
			Y:    60,
			Unit: PositionPhysicalPixels,
		},
	}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement settings = %+v, want %+v", got, want)
	}
	entries, err := os.ReadDir(filepath.Dir(paths.Native))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(paths.Native) {
		t.Fatalf("replacement left temporary files: %+v", entries)
	}
}
