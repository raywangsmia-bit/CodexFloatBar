package codexdata

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type sourceChangeMask uint8

const (
	sourceConfigChanged sourceChangeMask = 1 << iota
	sourceAuthChanged
	sourceSessionsChanged
	sourceLogsChanged
	sourceAllChanged = sourceConfigChanged | sourceAuthChanged |
		sourceSessionsChanged | sourceLogsChanged
)

type sourceFileStamp struct {
	path    string
	exists  bool
	size    int64
	modTime int64
	mode    fs.FileMode
	info    fs.FileInfo
}

type sourceInventory struct {
	config   sourceFileStamp
	auth     sourceFileStamp
	logs     []sourceFileStamp
	sessions []sessionFile
}

func collectSourceInventory(
	ctx context.Context,
	paths Paths,
	metrics *ReadMetrics,
) (sourceInventory, error) {
	if err := ctx.Err(); err != nil {
		return sourceInventory{}, err
	}
	if metrics != nil && metrics.inventoryHook != nil {
		if err := metrics.inventoryHook(); err != nil {
			return sourceInventory{}, err
		}
	}
	config, err := sourceStamp(paths.Config)
	if err != nil {
		return sourceInventory{}, err
	}
	auth, err := sourceStamp(paths.Auth)
	if err != nil {
		return sourceInventory{}, err
	}
	inventory := sourceInventory{
		config: config,
		auth:   auth,
		logs:   make([]sourceFileStamp, 0, len(paths.Logs)),
	}
	for _, path := range paths.Logs {
		if err := ctx.Err(); err != nil {
			return sourceInventory{}, err
		}
		stamp, err := sourceStamp(path)
		if err != nil {
			return sourceInventory{}, err
		}
		inventory.logs = append(inventory.logs, stamp)
	}
	sessions, err := collectSessionInventory(ctx, paths.Sessions, metrics)
	if err != nil {
		return sourceInventory{}, err
	}
	inventory.sessions = sessions
	return inventory, nil
}

func collectSessionInventory(
	ctx context.Context,
	root string,
	metrics *ReadMetrics,
) ([]sessionFile, error) {
	files := []sessionFile{}
	if strings.TrimSpace(root) == "" {
		return files, nil
	}
	err := filepath.WalkDir(root, func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		metrics.addWalkFile()
		if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		files = append(files, sessionFile{
			path:    filepath.Clean(path),
			modTime: info.ModTime().UTC().UnixNano(),
			size:    info.Size(),
			mode:    info.Mode(),
			info:    info,
		})
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return files, nil
		}
		return []sessionFile{}, err
	}
	slices.SortFunc(files, compareSessionFiles)
	return files, nil
}

func compareSessionFiles(left sessionFile, right sessionFile) int {
	if left.modTime != right.modTime {
		if left.modTime > right.modTime {
			return -1
		}
		return 1
	}
	return strings.Compare(strings.ToLower(left.path), strings.ToLower(right.path))
}

func sourceStamp(path string) (sourceFileStamp, error) {
	stamp := sourceFileStamp{path: filepath.Clean(path)}
	if strings.TrimSpace(path) == "" {
		return stamp, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return stamp, nil
		}
		return sourceFileStamp{}, err
	}
	stamp.exists = true
	stamp.size = info.Size()
	stamp.modTime = info.ModTime().UTC().UnixNano()
	stamp.mode = info.Mode()
	stamp.info = info
	return stamp, nil
}

func diffSourceInventory(previous sourceInventory, next sourceInventory) sourceChangeMask {
	var changed sourceChangeMask
	if !sameSourceStamp(previous.config, next.config) {
		changed |= sourceConfigChanged
	}
	if !sameSourceStamp(previous.auth, next.auth) {
		changed |= sourceAuthChanged
	}
	if !sameSourceStamps(previous.logs, next.logs) {
		changed |= sourceLogsChanged
	}
	if !sameSessionFiles(previous.sessions, next.sessions) {
		changed |= sourceSessionsChanged
	}
	return changed
}

func sameSourceStamps(left []sourceFileStamp, right []sourceFileStamp) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameSourceStamp(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sameSourceStamp(left sourceFileStamp, right sourceFileStamp) bool {
	samePath := cachePathKey(left.path) == cachePathKey(right.path)
	if !samePath || left.exists != right.exists {
		return false
	}
	if !left.exists {
		return true
	}
	sameMetadata := left.size == right.size && left.modTime == right.modTime &&
		left.mode == right.mode
	return sameMetadata && sameFileIdentity(left.info, right.info)
}

func sameSessionFiles(left []sessionFile, right []sessionFile) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameSessionFile(left[index], right[index]) {
			return false
		}
	}
	return true
}

func replacedSessionKeys(previous []sessionFile, next []sessionFile) map[string]struct{} {
	previousByPath := make(map[string]sessionFile, len(previous))
	for _, file := range previous {
		previousByPath[cachePathKey(file.path)] = file
	}
	replaced := map[string]struct{}{}
	for _, file := range next {
		key := cachePathKey(file.path)
		oldFile, ok := previousByPath[key]
		if ok && !sameFileIdentity(oldFile.info, file.info) {
			replaced[key] = struct{}{}
		}
	}
	return replaced
}

func sameFileIdentity(left fs.FileInfo, right fs.FileInfo) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return os.SameFile(left, right)
}
