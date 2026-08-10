//go:build windows

package main

import "testing"

func TestQuietUninstallUsesDedicatedRunningExitCode(t *testing.T) {
	if applicationRunningExitCode != 32 {
		t.Fatalf("application running exit code = %d, want 32", applicationRunningExitCode)
	}
}
