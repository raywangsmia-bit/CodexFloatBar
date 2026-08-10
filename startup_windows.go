//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appidentity"
	"golang.org/x/sys/windows/registry"
)

const (
	startupRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	startupValueName    = appidentity.AppID
)

type startupRegistry interface {
	read(string) (string, bool, error)
	write(string, string) error
	delete(string) error
}

type startupService struct {
	registry       startupRegistry
	executablePath func() (string, error)
}

type windowsStartupRegistry struct{}

func newStartupService() startupService {
	return startupService{
		registry:       windowsStartupRegistry{},
		executablePath: currentExecutablePath,
	}
}

func (service startupService) IsEnabled() (bool, error) {
	value, exists, err := service.registry.read(startupValueName)
	if err != nil {
		return false, fmt.Errorf("reading startup value: %w", err)
	}
	if !exists {
		return false, nil
	}

	path, err := service.executablePath()
	if err != nil {
		return false, fmt.Errorf("resolving executable path: %w", err)
	}
	expected, err := startupCommand(path)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(value), expected), nil
}

func (service startupService) SetEnabled(enabled bool) error {
	if !enabled {
		if err := service.registry.delete(startupValueName); err != nil {
			return fmt.Errorf("disabling startup: %w", err)
		}
		return nil
	}

	path, err := service.executablePath()
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}
	command, err := startupCommand(path)
	if err != nil {
		return err
	}
	if err := service.registry.write(startupValueName, command); err != nil {
		return fmt.Errorf("enabling startup: %w", err)
	}
	return nil
}

func currentExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func startupCommand(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("executable path is empty")
	}
	if strings.ContainsAny(path, "\x00\"") {
		return "", errors.New("executable path contains an invalid character")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("executable path is not absolute")
	}
	return `"` + filepath.Clean(path) + `"`, nil
}

func (windowsStartupRegistry) read(name string) (string, bool, error) {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		startupRegistryPath,
		registry.QUERY_VALUE,
	)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer key.Close()

	value, _, err := key.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (windowsStartupRegistry) write(name string, value string) error {
	key, _, err := registry.CreateKey(
		registry.CURRENT_USER,
		startupRegistryPath,
		registry.SET_VALUE,
	)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(name, value)
}

func (windowsStartupRegistry) delete(name string) error {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		startupRegistryPath,
		registry.SET_VALUE,
	)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer key.Close()

	err = key.DeleteValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}
