//go:build windows

package main

import "github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"

func composeRenderedSurface(
	surface *renderedSurface,
	snapshot codexdata.AppSnapshot,
) (*renderedSurface, error) {
	return composeRenderedSurfaceWithStatistics(
		surface,
		snapshot,
		statisticsSelection{},
	)
}

func composeRenderedSurfaceWithStatistics(
	surface *renderedSurface,
	snapshot codexdata.AppSnapshot,
	statistics statisticsSelection,
) (*renderedSurface, error) {
	return composeRenderedSurfaceWithPresentation(
		surface,
		presentSnapshotWithStatistics(snapshot, statistics),
	)
}
