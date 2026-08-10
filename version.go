package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const projectID = "codexfloatingbar"

type pageVersion struct {
	Update        string `json:"update"`
	Build         string `json:"build"`
	StaticVersion string `json:"staticVersion"`
}

func newPageVersion(startedAt time.Time, staticVersion string) pageVersion {
	local := startedAt.Local()
	return pageVersion{
		Update:        local.Format("2006-01-02"),
		Build:         local.Format("2006-01-02 15:04:05"),
		StaticVersion: staticVersion,
	}
}

func formatTrayBuild(startedAt time.Time) string {
	return "b" + startedAt.Local().Format("0201 15:05")
}

func fingerprintTree(root string) (string, error) {
	files := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("making relative path for %q: %w", path, err)
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walking static files: %w", err)
	}

	sort.Strings(files)
	hash := sha256.New()
	for _, relative := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		contents, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading static file %q: %w", relative, err)
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(contents)
		_, _ = hash.Write([]byte{0})
	}

	digest := hex.EncodeToString(hash.Sum(nil))
	return projectID + "-" + strings.ToLower(digest[:12]), nil
}
