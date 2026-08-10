package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFormatTrayBuild(t *testing.T) {
	startedAt := time.Date(2026, 8, 10, 18, 54, 37, 0, time.Local)

	if got := formatTrayBuild(startedAt); got != "b1008 18:37" {
		t.Fatalf("formatTrayBuild() = %q, want %q", got, "b1008 18:37")
	}
}

func TestFingerprintChangesForPathAndContent(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "a.css")
	if err := os.WriteFile(firstPath, []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := fingerprintTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(firstPath, filepath.Join(root, "b.css")); err != nil {
		t.Fatal(err)
	}
	second, err := fingerprintTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("fingerprint did not change after a rename")
	}

	if err := os.WriteFile(filepath.Join(root, "b.css"), []byte("body{color:red}"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := fingerprintTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatal("fingerprint did not change after a content edit")
	}
}
