//go:build windows

package main

import (
	"errors"
	"testing"
)

func TestStartupCommandQuotesAbsoluteExecutable(t *testing.T) {
	command, err := startupCommand(`C:\Program Files\CodexFloatingBar\CodexFloatingBar.Next.exe`)
	if err != nil {
		t.Fatal(err)
	}
	want := `"C:\Program Files\CodexFloatingBar\CodexFloatingBar.Next.exe"`
	if command != want {
		t.Fatalf("startup command = %q, want %q", command, want)
	}
}

func TestStartupCommandRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{
		"",
		`relative\CodexFloatingBar.exe`,
		`C:\Apps\bad"path.exe`,
		"C:\\Apps\\bad\x00path.exe",
	} {
		if _, err := startupCommand(path); err == nil {
			t.Fatalf("unsafe path %q was accepted", path)
		}
	}
}

func TestStartupServiceEnablesIndependentNextValue(t *testing.T) {
	registry := newFakeStartupRegistry()
	service := startupService{
		registry: registry,
		executablePath: func() (string, error) {
			return `C:\Program Files\CodexFloatingBar\CodexFloatingBar.Next.exe`, nil
		},
	}

	if err := service.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	want := `"C:\Program Files\CodexFloatingBar\CodexFloatingBar.Next.exe"`
	if got := registry.values[startupValueName]; got != want {
		t.Fatalf("stored startup command = %q, want %q", got, want)
	}
	if _, exists := registry.values["CodexFloatingBar"]; exists {
		t.Fatal("native startup modified the WPF value")
	}

	enabled, err := service.IsEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("matching startup command is not enabled")
	}
}

func TestStartupServiceRejectsStaleExecutableValue(t *testing.T) {
	registry := newFakeStartupRegistry()
	registry.values[startupValueName] = `"C:\Old\CodexFloatingBar.Next.exe"`
	service := startupService{
		registry: registry,
		executablePath: func() (string, error) {
			return `C:\Current\CodexFloatingBar.Next.exe`, nil
		},
	}

	enabled, err := service.IsEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("stale startup command reported enabled")
	}
}

func TestStartupServiceComparisonIsCaseInsensitive(t *testing.T) {
	registry := newFakeStartupRegistry()
	registry.values[startupValueName] = `"c:\apps\codexfloatingbar.next.exe"`
	service := startupService{
		registry: registry,
		executablePath: func() (string, error) {
			return `C:\Apps\CodexFloatingBar.Next.exe`, nil
		},
	}

	enabled, err := service.IsEnabled()
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("case-only path difference reported disabled")
	}
}

func TestStartupServiceDisablesWithoutResolvingExecutable(t *testing.T) {
	registry := newFakeStartupRegistry()
	registry.values[startupValueName] = `"C:\Apps\CodexFloatingBar.Next.exe"`
	service := startupService{
		registry: registry,
		executablePath: func() (string, error) {
			return "", errors.New("must not be called")
		},
	}

	if err := service.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, exists := registry.values[startupValueName]; exists {
		t.Fatal("startup value was not deleted")
	}
}

type fakeStartupRegistry struct {
	values      map[string]string
	readError   error
	writeError  error
	deleteError error
}

func newFakeStartupRegistry() *fakeStartupRegistry {
	return &fakeStartupRegistry{values: map[string]string{}}
}

func (registry *fakeStartupRegistry) read(name string) (string, bool, error) {
	if registry.readError != nil {
		return "", false, registry.readError
	}
	value, exists := registry.values[name]
	return value, exists, nil
}

func (registry *fakeStartupRegistry) write(name string, value string) error {
	if registry.writeError != nil {
		return registry.writeError
	}
	registry.values[name] = value
	return nil
}

func (registry *fakeStartupRegistry) delete(name string) error {
	if registry.deleteError != nil {
		return registry.deleteError
	}
	delete(registry.values, name)
	return nil
}
