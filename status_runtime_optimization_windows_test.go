//go:build windows

package main

import (
	"testing"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"
)

func TestStatusRuntimeSkipsRenderWhenPresentationIsUnchanged(t *testing.T) {
	runtime := &statusRuntime{}
	first := codexdata.AppSnapshot{
		Runtime:     codexdata.RuntimeStatus{Model: "gpt-5.6"},
		RefreshedAt: time.Date(2026, time.August, 11, 1, 0, 0, 0, time.UTC),
	}
	runtime.pending.Store(&first)
	if !runtime.acceptPending() {
		t.Fatal("initial snapshot did not request a render")
	}

	refreshed := first
	refreshed.RefreshedAt = first.RefreshedAt.Add(time.Second)
	runtime.pending.Store(&refreshed)
	if runtime.acceptPending() {
		t.Fatal("timestamp-only snapshot requested a render")
	}
	if !runtime.current.RefreshedAt.Equal(refreshed.RefreshedAt) {
		t.Fatal("timestamp-only snapshot was not retained")
	}

	changed := refreshed
	changed.Runtime.Model = "gpt-5.6-mini"
	runtime.pending.Store(&changed)
	if !runtime.acceptPending() {
		t.Fatal("visible model change did not request a render")
	}
}

func TestSurfaceCacheMatchRequiresVersionAndScale(t *testing.T) {
	manifest := bundleManifest{Version: pageVersion{
		Build:         "2026-08-11 01:00:00",
		StaticVersion: "codexfloatingbar-abc123",
	}}
	surface := &renderedSurface{
		Manifest: manifest,
		Variant:  bundleVariant{Scale: 1.5},
	}
	if !surfaceMatches(surface, manifest, 1.5) {
		t.Fatal("matching base surface was not reusable")
	}
	changedScale := surfaceMatches(surface, manifest, 2)
	changedManifest := manifest
	changedManifest.Version.StaticVersion = "codexfloatingbar-def456"
	if changedScale || surfaceMatches(surface, changedManifest, 1.5) {
		t.Fatal("stale base surface was reusable")
	}
}
