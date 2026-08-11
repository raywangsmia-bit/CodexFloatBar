//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUseSelfTestIdentityIsolatesRuntimeState(t *testing.T) {
	app := newNativeApp(t.TempDir(), "main-horizontal", time.Now())
	app.useSelfTestIdentity()

	if app.windowClass != nativeSelfTestWindowClass {
		t.Fatalf("window class = %q", app.windowClass)
	}
	if app.mutexName != nativeSelfTestMutexName {
		t.Fatalf("mutex name = %q", app.mutexName)
	}
	if !app.placementDisabled {
		t.Fatal("self-test identity did not disable placement persistence")
	}
}

func TestNativeSelfTestWritesStartupFailureReport(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "self-test.json")
	selfTest := newNativeSelfTest(outputPath)
	cause := errors.New("fixture startup failure")
	if err := selfTest.ensureFailureReport(cause); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var report nativeSelfTestReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatal("startup failure report unexpectedly passed")
	}
	if len(report.Checks) != 1 || report.Checks[0].Name != "startup" {
		t.Fatalf("checks = %#v", report.Checks)
	}
	if len(report.Failures) != 1 || report.Failures[0] != "startup: "+cause.Error() {
		t.Fatalf("failures = %#v", report.Failures)
	}
}
