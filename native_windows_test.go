//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"
)

func TestBundleWatcherIsExplicitlyOptIn(t *testing.T) {
	bundleRoot := t.TempDir()
	manifestPath := filepath.Join(bundleRoot, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := newNativeApp(bundleRoot, "", time.Now())
	app.startBundleWatcher()
	if app.lastManifestState != (fileState{}) {
		t.Fatal("disabled bundle watcher touched the manifest state")
	}

	app.watchBundleEnabled = true
	app.startBundleWatcher()
	if app.lastManifestState == (fileState{}) {
		t.Fatal("enabled bundle watcher did not record the initial manifest state")
	}
	app.stopWatcher()
}

func TestWindowRolesRouteFixedNativeWindows(t *testing.T) {
	app := nativeApp{
		window: 101,
		statisticsWindow: auxiliaryWindow{
			Role:   windowRoleStatistics,
			Handle: 202,
		},
		usageToastWindow: auxiliaryWindow{
			Role:   windowRoleUsageToast,
			Handle: 303,
		},
	}

	tests := []struct {
		window uintptr
		want   windowRole
	}{
		{window: 101, want: windowRoleMain},
		{window: 202, want: windowRoleStatistics},
		{window: 303, want: windowRoleUsageToast},
		{window: 404, want: windowRoleUnknown},
		{window: 0, want: windowRoleUnknown},
	}
	for _, test := range tests {
		if got := app.roleForWindow(test.window); got != test.want {
			t.Fatalf("window %d role = %d want %d", test.window, got, test.want)
		}
	}
}

func TestStatisticsDateHitRegionsOnlyCaptureMonthView(t *testing.T) {
	surface := &renderedSurface{
		Surface: bundleSurface{HitRegions: []hitRegion{{
			Action: "statistics-select-day-05",
			X:      0,
			Y:      0,
			Width:  20,
			Height: 20,
		}}},
		Variant: bundleVariant{Scale: 1},
	}
	app := nativeApp{
		status: &statusRuntime{
			current: codexdata.AppSnapshot{
				Statistics: codexdata.StatisticsSnapshot{
					RefreshedAt: time.Date(
						2026,
						time.August,
						19,
						12,
						0,
						0,
						0,
						time.UTC,
					),
				},
			},
			statistics: statisticsSelection{View: statisticsViewWeek},
		},
		statisticsWindow: auxiliaryWindow{
			Role:           windowRoleStatistics,
			Handle:         202,
			CurrentSurface: surface,
		},
	}
	if action := app.actionAtWindow(202, 5, 5); action != "" {
		t.Fatalf("weekly date hit captured %q; empty space must remain draggable", action)
	}
	app.status.statistics.View = statisticsViewMonth
	if action := app.actionAtWindow(202, 5, 5); action != "statistics-select-day-05" {
		t.Fatalf("month date hit = %q", action)
	}
	surface.Surface.HitRegions[0].Action = "statistics-select-day-00"
	if action := app.actionAtWindow(202, 5, 5); action != "" {
		t.Fatalf("hidden month cell captured %q", action)
	}
}

func TestUsageToastAnimationSuppressesHitActions(t *testing.T) {
	surface := &renderedSurface{
		Surface: bundleSurface{HitRegions: []hitRegion{{
			Action: "hide-toast",
			X:      0,
			Y:      0,
			Width:  20,
			Height: 20,
		}}},
		Variant: bundleVariant{Scale: 1},
	}
	app := nativeApp{
		usageToastWindow: auxiliaryWindow{
			Role:           windowRoleUsageToast,
			Handle:         303,
			CurrentSurface: surface,
		},
	}
	for _, visibility := range []auxiliaryVisibility{
		auxiliaryShowing,
		auxiliaryHiding,
	} {
		app.usageToastWindow.Visibility = visibility
		if action := app.actionAtWindow(303, 5, 5); action != "" {
			t.Fatalf("toast state %d accepted %q", visibility, action)
		}
		if !toastAnimationBlocksInput(visibility) {
			t.Fatalf("toast state %d did not request HTTRANSPARENT", visibility)
		}
	}
	app.usageToastWindow.Visibility = auxiliaryVisible
	if action := app.actionAtWindow(303, 5, 5); action != "hide-toast" {
		t.Fatalf("visible toast action = %q", action)
	}
	if toastAnimationBlocksInput(auxiliaryVisible) {
		t.Fatal("visible toast remained click-through")
	}
	if htTransparent != ^uintptr(0) {
		t.Fatalf("HTTRANSPARENT = %#x", htTransparent)
	}
}

func TestHiddenAuxiliaryWindowsDeferStatusComposition(t *testing.T) {
	app := nativeApp{}
	statistics := auxiliaryWindow{
		Role:       windowRoleStatistics,
		Handle:     202,
		Visibility: auxiliaryHidden,
	}
	if app.shouldComposeAuxiliaryStatus(&statistics) {
		t.Fatal("hidden statistics window requested status composition")
	}
	statistics.Visibility = auxiliaryVisible
	if !app.shouldComposeAuxiliaryStatus(&statistics) {
		t.Fatal("visible statistics window deferred status composition")
	}

	toast := auxiliaryWindow{
		Role:       windowRoleUsageToast,
		Handle:     303,
		Visibility: auxiliaryShowing,
	}
	if app.shouldComposeAuxiliaryStatus(&toast) {
		t.Fatal("showing toast should finish its existing surface before recomposition")
	}
	toast.Visibility = auxiliaryVisible
	if !app.shouldComposeAuxiliaryStatus(&toast) {
		t.Fatal("visible toast deferred status composition")
	}
	app.accountExpiryToastActive = true
	if app.shouldComposeAuxiliaryStatus(&toast) {
		t.Fatal("account-expiry content was replaced by a status composition")
	}
}

func TestAutoCollapseTimerStopsWhileHiddenAndRestartsWhenShown(t *testing.T) {
	tests := []struct {
		name        string
		enabled     bool
		mainVisible bool
		running     bool
		want        timerSyncAction
	}{
		{
			name:        "hide stops running timer",
			enabled:     true,
			mainVisible: false,
			running:     true,
			want:        timerSyncStop,
		},
		{
			name:        "show restarts enabled timer",
			enabled:     true,
			mainVisible: true,
			running:     false,
			want:        timerSyncStart,
		},
		{
			name:        "hidden enabled timer stays idle",
			enabled:     true,
			mainVisible: false,
			running:     false,
			want:        timerSyncNone,
		},
		{
			name:        "disabled visible timer stops",
			enabled:     false,
			mainVisible: true,
			running:     true,
			want:        timerSyncStop,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := autoCollapseTimerSyncAction(
				test.enabled,
				test.mainVisible,
				test.running,
			)
			if got != test.want {
				t.Fatalf("action = %d, want %d", got, test.want)
			}
		})
	}
}

func TestSurfaceForWindowUsesRoleSurface(t *testing.T) {
	mainSurface := &renderedSurface{Surface: bundleSurface{ID: "main-horizontal"}}
	statisticsSurface := &renderedSurface{Surface: bundleSurface{ID: statisticsSurfaceID}}
	toastSurface := &renderedSurface{Surface: bundleSurface{ID: usageToastSurfaceID}}
	app := nativeApp{
		window:         101,
		currentSurface: mainSurface,
		statisticsWindow: auxiliaryWindow{
			Role:           windowRoleStatistics,
			Handle:         202,
			CurrentSurface: statisticsSurface,
		},
		usageToastWindow: auxiliaryWindow{
			Role:           windowRoleUsageToast,
			Handle:         303,
			CurrentSurface: toastSurface,
		},
	}

	if got := app.surfaceForWindow(101); got != mainSurface {
		t.Fatal("main window did not resolve its surface")
	}
	if got := app.surfaceForWindow(202); got != statisticsSurface {
		t.Fatal("statistics window did not resolve its surface")
	}
	if got := app.surfaceForWindow(303); got != toastSurface {
		t.Fatal("usage toast window did not resolve its surface")
	}
	if got := app.surfaceForWindow(404); got != nil {
		t.Fatalf("unknown window resolved surface %#v", got)
	}
}
