package codexdata

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// MonitorOptions controls periodic mtime checks.
type MonitorOptions struct {
	PollInterval time.Duration
}

// Monitor coalesces refresh requests and cancels obsolete reads.
type Monitor struct {
	service      *Service
	pollInterval time.Duration
	refreshes    chan struct{}
	updates      chan AppSnapshot
}

type refreshResult struct {
	snapshot AppSnapshot
	err      error
}

type sourceFileStamp struct {
	path    string
	size    int64
	modTime int64
	mode    fs.FileMode
}

// NewMonitor creates an idle monitor. Run owns its lifecycle.
func NewMonitor(service *Service, options MonitorOptions) *Monitor {
	interval := options.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	return &Monitor{
		service:      service,
		pollInterval: interval,
		refreshes:    make(chan struct{}, 1),
		updates:      make(chan AppSnapshot, 1),
	}
}

// Updates returns latest-value notifications. The channel closes when Run exits.
func (monitor *Monitor) Updates() <-chan AppSnapshot {
	return monitor.updates
}

// Refresh requests a refresh without blocking the caller.
func (monitor *Monitor) Refresh() {
	select {
	case monitor.refreshes <- struct{}{}:
	default:
	}
}

// Run performs the initial read, then refreshes only after mtime changes or Refresh.
func (monitor *Monitor) Run(ctx context.Context) error {
	defer close(monitor.updates)
	defer monitor.service.FlushStatisticsCache()
	ticker := time.NewTicker(monitor.pollInterval)
	defer ticker.Stop()

	stamp, _ := fingerprintSources(ctx, monitor.service.paths)
	results := make(chan refreshResult, 1)
	var refreshCancel context.CancelFunc
	running := false
	pending := true

	startRefresh := func() {
		refreshCtx, cancel := context.WithCancel(ctx)
		refreshCancel = cancel
		running = true
		pending = false
		go func() {
			snapshot, err := monitor.service.Snapshot(refreshCtx)
			results <- refreshResult{snapshot: snapshot, err: err}
		}()
	}
	startRefresh()

	for {
		select {
		case <-ctx.Done():
			if refreshCancel != nil {
				refreshCancel()
			}
			return ctx.Err()
		case <-monitor.refreshes:
			pending = true
			if running && refreshCancel != nil {
				refreshCancel()
				continue
			}
			startRefresh()
		case <-ticker.C:
			nextStamp, err := fingerprintSources(ctx, monitor.service.paths)
			if err != nil || nextStamp == stamp {
				continue
			}
			stamp = nextStamp
			pending = true
			if running && refreshCancel != nil {
				refreshCancel()
				continue
			}
			startRefresh()
		case result := <-results:
			running = false
			if refreshCancel != nil {
				refreshCancel()
			}
			refreshCancel = nil
			if result.err == nil {
				if !pending {
					publishLatest(monitor.updates, result.snapshot)
				}
			} else if !errors.Is(result.err, context.Canceled) &&
				!errors.Is(result.err, context.DeadlineExceeded) {
				return result.err
			}
			if pending {
				startRefresh()
			}
		}
	}
}

func publishLatest(updates chan AppSnapshot, snapshot AppSnapshot) {
	select {
	case updates <- snapshot:
		return
	default:
	}
	select {
	case <-updates:
	default:
	}
	updates <- snapshot
}

func fingerprintSources(ctx context.Context, paths Paths) ([32]byte, error) {
	stamps := []sourceFileStamp{}
	for _, path := range append(
		[]string{paths.Config, paths.Auth},
		paths.Logs...,
	) {
		if strings.TrimSpace(path) == "" {
			continue
		}
		stamp, ok := statSource(path)
		if ok {
			stamps = append(stamps, stamp)
		}
	}
	if strings.TrimSpace(paths.Sessions) != "" {
		err := filepath.WalkDir(
			paths.Sessions,
			func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					if os.IsNotExist(walkErr) {
						return nil
					}
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
					return nil
				}
				stamp, ok := statSource(path)
				if ok {
					stamps = append(stamps, stamp)
				}
				return nil
			},
		)
		if err != nil && !os.IsNotExist(err) {
			return [32]byte{}, err
		}
	}
	slices.SortFunc(stamps, func(left sourceFileStamp, right sourceFileStamp) int {
		return strings.Compare(cachePathKey(left.path), cachePathKey(right.path))
	})
	hash := sha256.New()
	buffer := make([]byte, 24)
	for _, stamp := range stamps {
		hash.Write([]byte(filepath.ToSlash(stamp.path)))
		hash.Write([]byte{0})
		binary.LittleEndian.PutUint64(buffer[0:8], uint64(stamp.size))
		binary.LittleEndian.PutUint64(buffer[8:16], uint64(stamp.modTime))
		binary.LittleEndian.PutUint64(buffer[16:24], uint64(stamp.mode))
		hash.Write(buffer)
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func statSource(path string) (sourceFileStamp, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return sourceFileStamp{}, false
	}
	return sourceFileStamp{
		path:    filepath.Clean(path),
		size:    info.Size(),
		modTime: info.ModTime().UTC().UnixNano(),
		mode:    info.Mode(),
	}, true
}
