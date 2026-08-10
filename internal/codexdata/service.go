package codexdata

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// Options makes every filesystem and clock dependency explicit for tests.
type Options struct {
	Paths             Paths
	Location          *time.Location
	Now               func() time.Time
	CacheSaveInterval time.Duration
}

// Service reads immutable snapshots without retaining raw Codex content.
type Service struct {
	paths                  Paths
	location               *time.Location
	now                    func() time.Time
	cacheMu                sync.Mutex
	cacheSaveInterval      time.Duration
	statisticsCache        statisticsCache
	statisticsCacheRead    bool
	statisticsCacheDirty   bool
	statisticsCacheSavedAt time.Time
	sessionMu              sync.Mutex
	sessionStatusCache     map[string]cachedSessionStatus
	rateMu                 sync.Mutex
	rateLimitCache         map[string]cachedRateLimitCandidate
}

type cachedSessionStatus struct {
	file   sessionFile
	status RuntimeStatus
	found  bool
}

type cachedRateLimitCandidate struct {
	file      sessionFile
	candidate *rateLimitCandidate
}

// NewService creates a data service with no background goroutines.
func NewService(options Options) *Service {
	location := options.Location
	if location == nil {
		location = time.Local
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	paths := options.Paths
	if paths.Logs == nil {
		paths.Logs = []string{}
	}
	return &Service{
		paths:             paths,
		location:          location,
		now:               now,
		cacheSaveInterval: options.CacheSaveInterval,
		statisticsCache: emptyStatisticsCache(
			statisticsTimeZoneKey(location, now()),
		),
		sessionStatusCache: map[string]cachedSessionStatus{},
		rateLimitCache:     map[string]cachedRateLimitCandidate{},
	}
}

// Snapshot reads all sources and applies the WPF field-level precedence rules.
func (service *Service) Snapshot(ctx context.Context) (AppSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return AppSnapshot{}, err
	}
	now := service.now()
	config := readConfig(service.paths.Config)
	account := readAccount(service.paths.Auth)
	sessionFiles, err := newestSessionFiles(ctx, service.paths.Sessions, -1)
	if err != nil {
		sessionFiles = []sessionFile{}
	}

	var session RuntimeStatus
	var rateLimit RateLimitSummary
	var statistics StatisticsSnapshot
	var statisticsErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		session = service.readLatestSessionStatus(
			ctx,
			sessionFiles,
			service.paths.Logs,
		)
	}()
	go func() {
		defer wait.Done()
		rateLimit = service.readLatestRateLimit(
			ctx,
			sessionFiles,
			service.paths.Logs,
			service.location,
		)
	}()
	go func() {
		defer wait.Done()
		statistics, statisticsErr = service.readStatistics(ctx, now, sessionFiles)
	}()
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return AppSnapshot{}, err
	}
	if statisticsErr != nil && !errors.Is(statisticsErr, context.Canceled) {
		statistics = emptyStatistics(now)
	}
	return AppSnapshot{
		Account:     account,
		Config:      config,
		Runtime:     mergeRuntimeStatus(config, session),
		RateLimit:   rateLimit,
		Statistics:  statistics,
		RefreshedAt: now,
	}, nil
}

func (service *Service) readStatistics(
	ctx context.Context,
	now time.Time,
	sessionFiles []sessionFile,
) (StatisticsSnapshot, error) {
	service.cacheMu.Lock()
	defer service.cacheMu.Unlock()

	timeZone := statisticsTimeZoneKey(service.location, now)
	if !service.statisticsCacheRead {
		service.statisticsCache = readStatisticsCache(service.paths.Cache, timeZone)
		service.statisticsCacheRead = true
		if _, err := os.Stat(service.paths.Cache); err == nil {
			service.statisticsCacheSavedAt = now
		}
	}
	snapshot, nextCache, changed, err := updateStatistics(
		ctx,
		sessionPaths(sessionFiles),
		now,
		service.location,
		service.statisticsCache,
	)
	if changed {
		service.statisticsCache = nextCache
		service.statisticsCacheDirty = true
	}
	if err == nil {
		service.persistStatisticsCache(now, false)
	}
	return snapshot, err
}

func (service *Service) readLatestSessionStatus(
	ctx context.Context,
	files []sessionFile,
	logPaths []string,
) RuntimeStatus {
	service.sessionMu.Lock()
	defer service.sessionMu.Unlock()

	files = limitSessionFiles(files, 4)
	nextCache := make(map[string]cachedSessionStatus, len(files))
	defer func() {
		service.sessionStatusCache = nextCache
	}()
	for _, file := range files {
		if ctx.Err() != nil {
			return RuntimeStatus{}
		}
		key := cachePathKey(file.path)
		cached, ok := service.sessionStatusCache[key]
		if !ok || !sameSessionFile(cached.file, file) {
			cached.file = file
			cached.status, cached.found = findLatestSessionContext(file.path)
		}
		nextCache[key] = cached
		if cached.found {
			return cached.status
		}
	}
	return readLatestLogSessionStatus(ctx, logPaths)
}

func (service *Service) readLatestRateLimit(
	ctx context.Context,
	files []sessionFile,
	logPaths []string,
	location *time.Location,
) RateLimitSummary {
	service.rateMu.Lock()
	defer service.rateMu.Unlock()

	files = limitSessionFiles(files, 16)
	nextCache := make(map[string]cachedRateLimitCandidate, len(files))
	defer func() {
		service.rateLimitCache = nextCache
	}()
	var latest *rateLimitCandidate
	for fileIndex, file := range files {
		if ctx.Err() != nil {
			return unavailableRateLimit("等待 Codex 用量记录")
		}
		key := cachePathKey(file.path)
		cached, ok := service.rateLimitCache[key]
		if !ok || !sameSessionFile(cached.file, file) {
			cached.file = file
			cached.candidate = findSessionRateLimit(ctx, file.path, fileIndex)
		}
		nextCache[key] = cached
		if cached.candidate == nil {
			continue
		}
		candidate := *cached.candidate
		candidate.pathIndex = fileIndex
		if newerRateLimitCandidate(candidate, latest) {
			latest = &candidate
		}
	}
	if latest != nil {
		return summarizeRateLimit(*latest, location)
	}
	return readLatestLogRateLimit(ctx, logPaths, location)
}

func limitSessionFiles(files []sessionFile, limit int) []sessionFile {
	if len(files) <= limit {
		return files
	}
	return files[:limit]
}

func sameSessionFile(left sessionFile, right sessionFile) bool {
	return left.path == right.path && left.modTime == right.modTime && left.size == right.size
}

func sessionPaths(files []sessionFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	return paths
}

func (service *Service) persistStatisticsCache(now time.Time, force bool) {
	if !service.statisticsCacheDirty {
		return
	}
	intervalElapsed := service.statisticsCacheSavedAt.IsZero() ||
		now.Sub(service.statisticsCacheSavedAt) >= service.cacheSaveInterval
	shouldSave := force || service.cacheSaveInterval <= 0 || intervalElapsed
	if !shouldSave {
		return
	}
	if err := saveStatisticsCache(service.paths.Cache, service.statisticsCache); err != nil {
		return
	}
	service.statisticsCacheDirty = false
	service.statisticsCacheSavedAt = now
}

// FlushStatisticsCache persists any in-memory incremental progress.
func (service *Service) FlushStatisticsCache() {
	if service == nil {
		return
	}
	service.cacheMu.Lock()
	defer service.cacheMu.Unlock()
	service.persistStatisticsCache(service.now(), true)
}

// SelectedWeeklyWindow applies the UI's weekly-window preference.
func SelectedWeeklyWindow(summary RateLimitSummary) *RateLimitWindow {
	if isWeeklyWindow(summary.Secondary) {
		return summary.Secondary
	}
	if isWeeklyWindow(summary.Primary) {
		return summary.Primary
	}
	if summary.Secondary != nil {
		return summary.Secondary
	}
	return summary.Primary
}

func mergeRuntimeStatus(config ConfigSummary, session RuntimeStatus) RuntimeStatus {
	return RuntimeStatus{
		Model:           firstNonBlank(session.Model, config.Model),
		ReasoningEffort: firstNonBlank(session.ReasoningEffort, config.ReasoningEffort),
		SpeedTier:       firstNonBlank(session.SpeedTier, config.SpeedTier),
	}
}

func isWeeklyWindow(window *RateLimitWindow) bool {
	return window != nil && window.WindowMinutes == 10080
}

func emptyStatistics(now time.Time) StatisticsSnapshot {
	return StatisticsSnapshot{
		TokenTotals:          TokenBreakdown{Complete: true},
		DailyTokens:          map[string]int64{},
		DailyTokenBreakdowns: map[string]TokenBreakdown{},
		Weekly:               []WeeklyTokenUsage{},
		Monthly:              []MonthlyCumulativeUsage{},
		RefreshedAt:          now,
	}
}
