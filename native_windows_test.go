//go:build windows

package main

import "testing"

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
