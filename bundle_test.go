package main

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNearestVariant(t *testing.T) {
	surface := bundleSurface{
		ID: "main-horizontal",
		Variants: []bundleVariant{
			{Scale: 1, File: "100.png"},
			{Scale: 1.25, File: "125.png"},
			{Scale: 1.5, File: "150.png"},
			{Scale: 2, File: "200.png"},
		},
	}

	variant, err := surface.nearestVariant(1.4)
	if err != nil {
		t.Fatal(err)
	}
	if variant.Scale != 1.5 {
		t.Fatalf("got scale %.2f, want 1.5", variant.Scale)
	}
}

func TestNearestVariantPrefersDownscalingOnTie(t *testing.T) {
	surface := bundleSurface{
		ID: "main-horizontal",
		Variants: []bundleVariant{
			{Scale: 1.5, File: "150.png"},
			{Scale: 2, File: "200.png"},
		},
	}

	variant, err := surface.nearestVariant(1.75)
	if err != nil {
		t.Fatal(err)
	}
	if variant.Scale != 2 {
		t.Fatalf("got scale %.2f, want 2.0", variant.Scale)
	}
}

func TestSafeBundlePathRejectsParent(t *testing.T) {
	if _, err := safeBundlePath(t.TempDir(), "../escape.png"); err == nil {
		t.Fatal("safeBundlePath accepted a parent traversal")
	}
}

func TestActionAtUsesVariantScale(t *testing.T) {
	app := nativeApp{
		currentSurface: &renderedSurface{
			Surface: bundleSurface{
				HitRegions: []hitRegion{
					{Action: "hide", X: 100, Y: 10, Width: 20, Height: 20},
				},
			},
			Variant: bundleVariant{Scale: 1.5},
		},
	}

	if action := app.actionAt(165, 30); action != "hide" {
		t.Fatalf("got action %q, want hide", action)
	}
	if action := app.actionAt(149, 30); action != "" {
		t.Fatalf("got action %q outside the region", action)
	}
}

func TestLoadRenderedSurfaceScalesToExactDPI(t *testing.T) {
	root := t.TempDir()
	asset := image.NewNRGBA(image.Rect(0, 0, 200, 100))
	for y := range 100 {
		for x := range 200 {
			asset.SetNRGBA(x, y, color.NRGBA{R: 40, G: 80, B: 120, A: 255})
		}
	}
	assetFile, err := os.Create(filepath.Join(root, "surface@200.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(assetFile, asset); err != nil {
		_ = assetFile.Close()
		t.Fatal(err)
	}
	if err := assetFile.Close(); err != nil {
		t.Fatal(err)
	}

	manifest := bundleManifest{
		Schema:         bundleSchema,
		Project:        projectID,
		DefaultSurface: "main-horizontal",
		Surfaces: []bundleSurface{{
			ID:            "main-horizontal",
			LogicalWidth:  100,
			LogicalHeight: 50,
			Variants: []bundleVariant{{
				Scale:  2,
				File:   "surface@200.png",
				Width:  200,
				Height: 100,
			}},
		}},
	}
	contents, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}

	surface, err := loadRenderedSurface(root, "", 168)
	if err != nil {
		t.Fatal(err)
	}
	if surface.Variant.Scale != 1.75 || surface.Variant.Width != 175 || surface.Variant.Height != 88 {
		t.Fatalf("unexpected exact-DPI variant: %+v", surface.Variant)
	}
	if surface.Image.Bounds().Dx() != 175 || surface.Image.Bounds().Dy() != 88 {
		t.Fatalf("unexpected scaled image bounds: %v", surface.Image.Bounds())
	}

	appearanceScaled, err := loadRenderedSurfaceAtScale(root, "", 1.575)
	if err != nil {
		t.Fatal(err)
	}
	if appearanceScaled.Variant.Scale != 1.575 ||
		appearanceScaled.Variant.Width != 158 ||
		appearanceScaled.Variant.Height != 79 {
		t.Fatalf("unexpected appearance-scaled variant: %+v", appearanceScaled.Variant)
	}
	if _, err := loadRenderedSurfaceAtScale(root, "", 0); err == nil {
		t.Fatal("zero target scale was accepted")
	}
}
