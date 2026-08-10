//go:build windows

package main

import (
	"testing"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appsettings"
)

func TestSurfaceSelectionUsesThemeLayoutAndQuotaTone(t *testing.T) {
	manifest := bundleManifest{
		DefaultSurface: "main-horizontal",
		Surfaces: []bundleSurface{
			{ID: "main-horizontal"},
			{ID: "main-vertical"},
			{ID: "main-vertical-light"},
			{ID: "statistics"},
			{ID: "statistics-light"},
			{ID: "usage-toast"},
			{ID: "usage-toast-danger-light"},
		},
	}
	appearance := appsettings.DefaultAppearance()
	appearance.Theme = appsettings.ThemeLight
	appearance.Layout = appsettings.LayoutVertical
	if got := resolveMainSurfaceID(manifest, appearance); got != "main-vertical-light" {
		t.Fatalf("main surface = %q", got)
	}
	if got := resolveStatisticsSurfaceID(manifest, appearance.Theme); got != "statistics-light" {
		t.Fatalf("statistics surface = %q", got)
	}
	if got := resolveUsageToastSurfaceID(
		manifest,
		appearance.Theme,
		quotaToneDanger,
	); got != "usage-toast-danger-light" {
		t.Fatalf("danger toast surface = %q", got)
	}
}

func TestSurfaceSelectionFallsBackToLegacyDarkBundle(t *testing.T) {
	manifest := bundleManifest{
		DefaultSurface: "main-horizontal",
		Surfaces: []bundleSurface{
			{ID: "main-horizontal"},
			{ID: "main-vertical"},
			{ID: "statistics"},
			{ID: "usage-toast"},
		},
	}
	appearance := appsettings.DefaultAppearance()
	appearance.Theme = appsettings.ThemeLight
	appearance.Layout = appsettings.LayoutVertical
	if got := resolveMainSurfaceID(manifest, appearance); got != "main-vertical" {
		t.Fatalf("legacy main fallback = %q", got)
	}
	if got := resolveStatisticsSurfaceID(manifest, appearance.Theme); got != "statistics" {
		t.Fatalf("legacy statistics fallback = %q", got)
	}
	if got := resolveUsageToastSurfaceID(
		manifest,
		appearance.Theme,
		quotaToneDanger,
	); got != "usage-toast" {
		t.Fatalf("legacy toast fallback = %q", got)
	}
}
