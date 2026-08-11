package appsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appidentity"
)

const (
	settingsSchema  = 1
	maxSettingsSize = 64 << 10
)

var errSettingsTooLarge = errors.New("settings file is too large")

type Paths struct {
	Native           string
	LegacyAppearance string
	LegacyPlacement  string
}

func DefaultPaths(localAppData string) Paths {
	return Paths{
		Native: filepath.Join(
			localAppData,
			appidentity.DataDirectory,
			"settings.json",
		),
		LegacyAppearance: filepath.Join(
			localAppData,
			"CodexFloatingBar",
			"appearance.json",
		),
		LegacyPlacement: filepath.Join(
			localAppData,
			"CodexFloatingBar",
			"window-placement.json",
		),
	}
}

type Store struct {
	paths Paths
}

func NewStore(paths Paths) *Store {
	return &Store{paths: paths}
}

func (store *Store) Load() (Appearance, error) {
	settings, state, err := store.loadNative()
	if err != nil {
		return DefaultAppearance(), err
	}
	switch state {
	case nativeSettingsValid:
		return settings, nil
	case nativeSettingsInvalid:
		return DefaultAppearance(), nil
	}

	migrated := normalize(store.migrateLegacy())
	if err := store.Save(migrated); err != nil {
		return migrated, fmt.Errorf("saving migrated settings: %w", err)
	}
	return migrated, nil
}

func (store *Store) Save(settings Appearance) error {
	if store == nil || store.paths.Native == "" {
		return errors.New("native settings path is unavailable")
	}
	normalized := normalize(settings)
	followCodex := normalized.FollowCodex
	disk := diskSettings{
		Schema:                settingsSchema,
		Theme:                 normalized.Theme,
		Layout:                normalized.Layout,
		Scale:                 normalized.Scale,
		AutoCollapse:          normalized.AutoCollapse,
		FollowCodex:           &followCodex,
		AccountExpiryDate:     normalized.AccountExpiryDate,
		AccountExpiryReminder: normalized.AccountExpiryReminder,
		MainWindow:            normalized.MainWindow,
		HorizontalMainWindow:  normalized.HorizontalMainWindow,
		VerticalMainWindow:    normalized.VerticalMainWindow,
		StatisticsWindow:      normalized.StatisticsWindow,
	}
	contents, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding native settings: %w", err)
	}
	contents = append(contents, '\n')
	if err := writeAtomic(store.paths.Native, contents); err != nil {
		return fmt.Errorf("writing native settings: %w", err)
	}
	return nil
}

type nativeSettingsState uint8

const (
	nativeSettingsMissing nativeSettingsState = iota
	nativeSettingsValid
	nativeSettingsInvalid
)

type diskSettings struct {
	Schema                int             `json:"schema"`
	Theme                 Theme           `json:"theme"`
	Layout                Layout          `json:"layout"`
	Scale                 float64         `json:"scale"`
	AutoCollapse          bool            `json:"autoCollapse"`
	FollowCodex           *bool           `json:"followCodex,omitempty"`
	AccountExpiryDate     string          `json:"accountExpiryDate,omitempty"`
	AccountExpiryReminder bool            `json:"accountExpiryReminder,omitempty"`
	MainWindow            *WindowPosition `json:"mainWindow,omitempty"`
	HorizontalMainWindow  *WindowPosition `json:"horizontalMainWindow,omitempty"`
	VerticalMainWindow    *WindowPosition `json:"verticalMainWindow,omitempty"`
	StatisticsWindow      *WindowPosition `json:"statisticsWindow,omitempty"`
}

func (store *Store) loadNative() (Appearance, nativeSettingsState, error) {
	if store == nil || store.paths.Native == "" {
		return Appearance{}, nativeSettingsMissing, errors.New("native settings path is unavailable")
	}
	contents, exists, err := readLimited(store.paths.Native)
	if errors.Is(err, errSettingsTooLarge) {
		return Appearance{}, nativeSettingsInvalid, nil
	}
	if err != nil {
		return Appearance{}, nativeSettingsMissing, fmt.Errorf("reading native settings: %w", err)
	}
	if !exists {
		return Appearance{}, nativeSettingsMissing, nil
	}

	var disk diskSettings
	if err := json.Unmarshal(contents, &disk); err != nil || disk.Schema != settingsSchema {
		return Appearance{}, nativeSettingsInvalid, nil
	}
	settings := DefaultAppearance()
	settings.Theme = disk.Theme
	settings.Layout = disk.Layout
	settings.Scale = disk.Scale
	settings.AutoCollapse = disk.AutoCollapse
	settings.MainWindow = disk.MainWindow
	settings.HorizontalMainWindow = disk.HorizontalMainWindow
	settings.VerticalMainWindow = disk.VerticalMainWindow
	settings.StatisticsWindow = disk.StatisticsWindow
	if disk.FollowCodex != nil {
		settings.FollowCodex = *disk.FollowCodex
	}
	settings.AccountExpiryDate = disk.AccountExpiryDate
	settings.AccountExpiryReminder = disk.AccountExpiryReminder
	return normalize(settings), nativeSettingsValid, nil
}

func readLimited(path string) ([]byte, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, false, nil
	}
	if err != nil {
		return []byte{}, false, err
	}
	defer file.Close()

	contents, err := io.ReadAll(io.LimitReader(file, maxSettingsSize+1))
	if err != nil {
		return []byte{}, true, err
	}
	if len(contents) > maxSettingsSize {
		return []byte{}, true, errSettingsTooLarge
	}
	return contents, true, nil
}

func writeAtomic(path string, contents []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("creating settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary settings file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("setting temporary file permissions: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("writing temporary settings file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("syncing temporary settings file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary settings file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replacing settings file: %w", err)
	}
	return nil
}
