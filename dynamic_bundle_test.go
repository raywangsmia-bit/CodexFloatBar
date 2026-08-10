package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadManifestSupportsLegacyStaticAndSchemaTwoDynamicSlots(t *testing.T) {
	tests := []struct {
		name     string
		manifest bundleManifest
		wantText int
	}{
		{
			name:     "schema one static",
			manifest: dynamicTestManifest(bundleSchemaLegacy, dynamicSlots{}),
			wantText: 0,
		},
		{
			name:     "schema two dynamic",
			manifest: dynamicTestManifest(bundleSchema, validDynamicTestSlots()),
			wantText: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeDynamicTestManifest(t, test.manifest)
			manifest, err := readManifest(root)
			if err != nil {
				t.Fatal(err)
			}
			dynamic := manifest.Surfaces[0].Dynamic
			if len(dynamic.Text) != test.wantText {
				t.Fatalf("text slot count = %d, want %d", len(dynamic.Text), test.wantText)
			}
			if test.manifest.Schema == bundleSchema {
				if len(dynamic.Progress) != 1 || len(dynamic.Cells) != 1 {
					t.Fatalf("dynamic slots were not preserved: %+v", dynamic)
				}
				if len(dynamic.Cells[0].Rects) != 42 {
					t.Fatalf("cell rect count = %d, want 42", len(dynamic.Cells[0].Rects))
				}
			}
		})
	}
}

func TestReadManifestValidatesDynamicSlotsForBothSchemas(t *testing.T) {
	for _, schema := range []int{bundleSchemaLegacy, bundleSchema} {
		t.Run(bundleSchemaName(schema), func(t *testing.T) {
			dynamic := validDynamicTestSlots()
			dynamic.Text[0].Bind = "runtime.unknown"
			root := writeDynamicTestManifest(
				t,
				dynamicTestManifest(schema, dynamic),
			)
			if _, err := readManifest(root); err == nil {
				t.Fatal("readManifest accepted an invalid dynamic text binding")
			}
		})
	}
}

func TestReadManifestRejectsNULInDynamicFontFamily(t *testing.T) {
	dynamic := validDynamicTestSlots()
	dynamic.Text[0].FontFamily = "Segoe\x00UI"
	root := writeDynamicTestManifest(
		t,
		dynamicTestManifest(bundleSchema, dynamic),
	)
	if _, err := readManifest(root); err == nil {
		t.Fatal("readManifest accepted a font family that would panic UTF-16 conversion")
	}
}

func TestReadManifestRejectsInvalidFontCandidateContract(t *testing.T) {
	tests := []struct {
		name       string
		candidates []string
	}{
		{name: "NUL", candidates: []string{"Segoe UI", "Bad\x00Font"}},
		{name: "first family mismatch", candidates: []string{"Arial", "Segoe UI"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dynamic := validDynamicTestSlots()
			dynamic.Text[0].FontFamilies = test.candidates
			root := writeDynamicTestManifest(
				t,
				dynamicTestManifest(bundleSchema, dynamic),
			)
			if _, err := readManifest(root); err == nil {
				t.Fatal("readManifest accepted an invalid font candidate contract")
			}
		})
	}
}

func dynamicTestManifest(schema int, dynamic dynamicSlots) bundleManifest {
	return bundleManifest{
		Schema:         schema,
		Project:        projectID,
		DefaultSurface: "main-horizontal",
		Surfaces: []bundleSurface{
			{
				ID:            "main-horizontal",
				LogicalWidth:  100,
				LogicalHeight: 60,
				Dynamic:       dynamic,
				Variants: []bundleVariant{
					{
						Scale:  1,
						File:   "main-horizontal.png",
						Width:  100,
						Height: 60,
					},
				},
			},
		},
	}
}

func validDynamicTestSlots() dynamicSlots {
	cellRects := make([]slotRect, 0, 42)
	for index := range 42 {
		cellRects = append(cellRects, slotRect{
			X:      float64(1 + index%7*3),
			Y:      float64(30 + index/7*3),
			Width:  2,
			Height: 2,
		})
	}
	return dynamicSlots{
		Text: []textSlot{
			{
				Bind:         "runtime.model",
				Rect:         slotRect{X: 1, Y: 1, Width: 48, Height: 14},
				FontFamily:   "Segoe UI",
				FontFamilies: []string{"Segoe UI", "sans-serif"},
				FontSize:     12,
				FontWeight:   600,
				Color:        "#112233",
				Align:        "left",
				ToneColors:   toneColors{Offline: "#445566"},
			},
		},
		Progress: []progressSlot{
			{
				Bind:       "quota.progress",
				Rect:       slotRect{X: 1, Y: 20, Width: 50, Height: 4},
				Color:      "#112233",
				ToneColors: toneColors{Warn: "#ffaa00"},
			},
		},
		Cells: []cellSlot{
			{
				Bind:   "statistics.monthCells",
				Rects:  cellRects,
				Colors: []string{"#000000", "#111111", "#222222", "#333333", "#444444"},
			},
		},
	}
}

func writeDynamicTestManifest(t *testing.T, manifest bundleManifest) string {
	t.Helper()
	contents, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func bundleSchemaName(schema int) string {
	if schema == bundleSchemaLegacy {
		return "schema one"
	}
	return "schema two"
}
