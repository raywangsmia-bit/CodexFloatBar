//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func defaultUIRoot() string {
	executablePath, executableErr := os.Executable()
	workingDirectory, workingDirectoryErr := os.Getwd()
	if workingDirectoryErr != nil {
		workingDirectory = "."
	}
	if executableErr != nil {
		return filepath.Join(workingDirectory, "ui")
	}
	return chooseUIRoot(executablePath, workingDirectory)
}

func chooseUIRoot(executablePath string, workingDirectory string) string {
	executableDirectory := filepath.Dir(executablePath)
	candidates := []string{
		filepath.Join(executableDirectory, "ui"),
		filepath.Join(filepath.Dir(executableDirectory), "ui"),
		filepath.Join(workingDirectory, "ui"),
	}
	for _, candidate := range candidates {
		manifest := filepath.Join(candidate, "dist", "manifest.json")
		if info, err := os.Stat(manifest); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return candidates[0]
}

func configureLog(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving log path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, fmt.Errorf("creating log directory: %w", err)
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}
	log.SetOutput(file)
	return func() {
		_ = file.Close()
	}, nil
}
