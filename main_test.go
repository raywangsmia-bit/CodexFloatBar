//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChooseUIRootUsesExecutableDirectory(t *testing.T) {
	root := t.TempDir()
	executableDirectory := filepath.Join(root, "app")
	uiRoot := filepath.Join(executableDirectory, "ui")
	if err := os.MkdirAll(filepath.Join(uiRoot, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiRoot, "dist", "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := chooseUIRoot(filepath.Join(executableDirectory, "app.exe"), filepath.Join(root, "elsewhere"))
	if got != uiRoot {
		t.Fatalf("got %q want %q", got, uiRoot)
	}
}

func TestChooseUIRootSupportsDeveloperBinDirectory(t *testing.T) {
	root := t.TempDir()
	uiRoot := filepath.Join(root, "ui")
	if err := os.MkdirAll(filepath.Join(uiRoot, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uiRoot, "dist", "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := chooseUIRoot(filepath.Join(root, "bin", "app.exe"), filepath.Join(root, "elsewhere"))
	if got != uiRoot {
		t.Fatalf("got %q want %q", got, uiRoot)
	}
}
