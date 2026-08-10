//go:build windows

package main

import "testing"

func TestMainWindowStaysOutOfTaskbar(t *testing.T) {
	style := mainWindowExtendedStyle()
	if style&wsExToolWindow == 0 {
		t.Fatal("main window is missing WS_EX_TOOLWINDOW")
	}
	if style&wsExAppWindow != 0 {
		t.Fatal("main window must not use WS_EX_APPWINDOW")
	}
}

func TestAuxiliaryWindowsStayOutOfTaskbar(t *testing.T) {
	statisticsStyle := auxiliaryWindowExtendedStyle(windowRoleStatistics)
	if statisticsStyle&wsExToolWindow == 0 {
		t.Fatal("statistics window is missing WS_EX_TOOLWINDOW")
	}
	if statisticsStyle&wsExAppWindow != 0 {
		t.Fatal("statistics window must not use WS_EX_APPWINDOW")
	}

	toastStyle := auxiliaryWindowExtendedStyle(windowRoleUsageToast)
	if toastStyle&wsExNoActivate == 0 {
		t.Fatal("usage toast is missing WS_EX_NOACTIVATE")
	}
}
