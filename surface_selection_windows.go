//go:build windows

package main

import "github.com/raywangsmia-bit/CodexFloatBar/internal/appsettings"

func preferredMainSurfaceID(appearance appsettings.Appearance) string {
	base := "main-horizontal"
	if appearance.Layout == appsettings.LayoutVertical {
		base = "main-vertical"
	}
	return themedSurfaceID(base, appearance.Theme)
}

func preferredStatisticsSurfaceID(theme appsettings.Theme) string {
	return themedSurfaceID("statistics", theme)
}

func preferredUsageToastSurfaceID(theme appsettings.Theme, tone quotaTone) string {
	base := "usage-toast"
	switch tone {
	case quotaToneGood:
		base = "usage-toast-good"
	case quotaToneDanger:
		base = "usage-toast-danger"
	case quotaToneOffline:
		base = "usage-toast-offline"
	}
	return themedSurfaceID(base, theme)
}

func themedSurfaceID(base string, theme appsettings.Theme) string {
	if theme == appsettings.ThemeLight {
		return base + "-light"
	}
	return base
}

func resolveMainSurfaceID(
	manifest bundleManifest,
	appearance appsettings.Appearance,
) string {
	preferred := preferredMainSurfaceID(appearance)
	fallbackAppearance := appearance
	fallbackAppearance.Theme = appsettings.ThemeDark
	return firstManifestSurface(
		manifest,
		preferred,
		preferredMainSurfaceID(fallbackAppearance),
		manifest.DefaultSurface,
	)
}

func resolveStatisticsSurfaceID(
	manifest bundleManifest,
	theme appsettings.Theme,
) string {
	return firstManifestSurface(
		manifest,
		preferredStatisticsSurfaceID(theme),
		"statistics",
	)
}

func resolveUsageToastSurfaceID(
	manifest bundleManifest,
	theme appsettings.Theme,
	tone quotaTone,
) string {
	return firstManifestSurface(
		manifest,
		preferredUsageToastSurfaceID(theme, tone),
		preferredUsageToastSurfaceID(appsettings.ThemeDark, tone),
		"usage-toast",
	)
}

func firstManifestSurface(manifest bundleManifest, candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" && manifest.hasSurface(candidate) {
			return candidate
		}
	}
	return ""
}

func (manifest bundleManifest) hasSurface(surfaceID string) bool {
	for _, surface := range manifest.Surfaces {
		if surface.ID == surfaceID {
			return true
		}
	}
	return false
}
