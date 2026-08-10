package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appidentity"
)

const maxPlacementSize = 64 << 10

type windowPlacement struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Layout string `json:"layout"`
}

type placementStore struct {
	path string
}

func newPlacementStore() placementStore {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return placementStore{}
	}
	return placementStore{
		path: filepath.Join(cacheDir, appidentity.DataDirectory, "window-placement.json"),
	}
}

func (store placementStore) load() (windowPlacement, bool) {
	if store.path == "" {
		return windowPlacement{}, false
	}

	file, err := os.Open(store.path)
	if err != nil {
		return windowPlacement{}, false
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxPlacementSize+1))
	if err != nil || len(contents) > maxPlacementSize {
		return windowPlacement{}, false
	}
	var placement windowPlacement
	if err := json.Unmarshal(contents, &placement); err != nil {
		return windowPlacement{}, false
	}
	if placement.Layout != "horizontal" && placement.Layout != "vertical" {
		return windowPlacement{}, false
	}
	if int64(placement.X) < math.MinInt32 || int64(placement.X) > math.MaxInt32 ||
		int64(placement.Y) < math.MinInt32 || int64(placement.Y) > math.MaxInt32 {
		return windowPlacement{}, false
	}
	return placement, true
}

func (store placementStore) save(placement windowPlacement) error {
	if store.path == "" {
		return errors.New("user cache directory is unavailable")
	}

	contents, err := json.MarshalIndent(placement, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding window placement: %w", err)
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return fmt.Errorf("creating placement directory: %w", err)
	}
	return writeAtomic(store.path, contents)
}
