package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlacementStoreRoundTrip(t *testing.T) {
	store := placementStore{path: filepath.Join(t.TempDir(), "placement.json")}
	want := windowPlacement{X: -1280, Y: 42, Layout: "vertical"}
	if err := store.save(want); err != nil {
		t.Fatal(err)
	}
	got, ok := store.load()
	if !ok {
		t.Fatal("saved placement was not loaded")
	}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestPlacementStoreRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "placement.json")
	contents := strings.Repeat(" ", maxPlacementSize+1)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := (placementStore{path: path}).load(); ok {
		t.Fatal("oversized placement was accepted")
	}
}
