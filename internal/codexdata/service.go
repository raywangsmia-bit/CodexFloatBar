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
	Metrics           *ReadMetrics
}

// Service reads immutable snapshots without retaining raw Codex content.
type Service struct {
	paths                  Paths
	location               *time.Location
	now                    func() time.Time
	metrics                *ReadMetrics
	snapshotMu             sync.Mutex
	lastInventory          sourceInventory
	hasInventory           bool
	config                 ConfigSummary
	configRead             bool
	account                AccountSummary
	accountRead            bool
	sessionSignals         sessionSignals
	logRuntime             RuntimeStatus
	logRuntimeRead         bool
	logRuntimeGeneration   uint64
	logRate                RateLimitSummary
	logRateRead            bool
	logRateGeneration      uint64
	logsGeneration         uint64
	tailMu                 sync.Mutex
	sessionTails           map[string]sessionTailState
	cacheMu                sync.Mutex
	cacheSaveInterval      time.Duration
	statisticsCache        statisticsCache
	statisticsCacheRead    bool
	statisticsCacheDirty   bool
	statisticsCacheSavedAt time.Time
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
		metrics:           options.Metrics,
		cacheSaveInterval: options.CacheSaveInterval,
		statisticsCache: emptyStatisticsCache(
			statisticsTimeZoneKey(location, now()),
		),
		sessionTails: map[string]sessionTailState{},
	}
}

// Snapshot reads all sources and applies the WPF field-level precedence rules.
func (service *Service) Snapshot(ctx context.Context) (AppSnapshot, error) {
	inventory, err := collectSourceInventory(ctx, service.paths, service.metrics)
	if err != nil {
		return AppSnapshot{}, err
	}
	return service.snapshotFromInventory(ctx, inventory)
}

func (service *Service) snapshotFromInventory(
	ctx context.Context,
	inventory sourceInventory,
) (AppSnapshot, error) {
	service.metrics.snapshotStarted()
	snapshot, err := service.snapshotFromInventoryLocked(ctx, inventory)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		service.metrics.snapshotCanceled()
	}
	return snapshot, err
}

func (service *Service) snapshotFromInventoryLocked(
	ctx context.Context,
	inventory sourceInventory,
) (AppSnapshot, error) {
	service.snapshotMu.Lock()
	defer service.snapshotMu.Unlock()
	if err := ctx.Err(); err != nil {
		return AppSnapshot{}, err
	}

	changed := sourceAllChanged
	invalidatedStatistics := map[string]struct{}{}
	if service.hasInventory {
		changed = diffSourceInventory(service.lastInventory, inventory)
		invalidatedStatistics = replacedSessionKeys(
			service.lastInventory.sessions,
			inventory.sessions,
		)
	}
	if !service.configRead || changed&sourceConfigChanged != 0 {
		config := missingConfigSummary(service.paths.Config)
		if inventory.config.exists {
			var err error
			config, err = readConfigContext(ctx, service.paths.Config, service.metrics)
			if err != nil {
				return AppSnapshot{}, err
			}
		}
		service.config = config
		service.configRead = true
	}
	if !service.accountRead || changed&sourceAuthChanged != 0 {
		account := notSignedInAccount()
		if inventory.auth.exists {
			var err error
			account, err = readAccountContext(ctx, service.paths.Auth, service.metrics)
			if err != nil {
				return AppSnapshot{}, err
			}
		}
		service.account = account
		service.accountRead = true
	}

	now := service.now()
	statistics := StatisticsSnapshot{}
	if changed&sourceSessionsChanged != 0 {
		signals, err := service.readSessionSignals(ctx, inventory.sessions)
		if err != nil {
			return AppSnapshot{}, err
		}
		service.sessionSignals = signals
		var statisticsErr error
		statistics, statisticsErr = service.readStatistics(
			ctx,
			now,
			inventory.sessions,
			invalidatedStatistics,
		)
		if statisticsErr != nil {
			return AppSnapshot{}, statisticsErr
		}
	} else {
		statistics = service.statisticsSnapshot(now)
	}
	if err := ctx.Err(); err != nil {
		return AppSnapshot{}, err
	}

	logsChanged := changed&sourceLogsChanged != 0
	if logsChanged {
		service.logsGeneration++
	}
	runtimeUsesLogs := !service.sessionSignals.runtimeFound
	runtimeLogStale := service.logRuntimeGeneration != service.logsGeneration
	shouldReadRuntimeLog := !service.logRuntimeRead || runtimeLogStale
	if runtimeUsesLogs && shouldReadRuntimeLog {
		logRuntime, err := readLatestLogSessionStatusWithMetrics(
			ctx,
			service.paths.Logs,
			service.metrics,
		)
		if err != nil {
			return AppSnapshot{}, err
		}
		if err := ctx.Err(); err != nil {
			return AppSnapshot{}, err
		}
		service.logRuntime = logRuntime
		service.logRuntimeRead = true
		service.logRuntimeGeneration = service.logsGeneration
	}
	rateUsesLogs := service.sessionSignals.rate == nil
	rateLogStale := service.logRateGeneration != service.logsGeneration
	shouldReadRateLog := !service.logRateRead || rateLogStale
	if rateUsesLogs && shouldReadRateLog {
		logRate, err := readLatestLogRateLimitWithMetrics(
			ctx,
			service.paths.Logs,
			service.location,
			service.metrics,
		)
		if err != nil {
			return AppSnapshot{}, err
		}
		if err := ctx.Err(); err != nil {
			return AppSnapshot{}, err
		}
		service.logRate = logRate
		service.logRateRead = true
		service.logRateGeneration = service.logsGeneration
	}
	if err := ctx.Err(); err != nil {
		return AppSnapshot{}, err
	}

	runtimeSource := service.logRuntime
	if service.sessionSignals.runtimeFound {
		runtimeSource = service.sessionSignals.runtime
	}
	rateLimit := cloneRateLimitSummary(service.logRate)
	if service.sessionSignals.rate != nil {
		rateLimit = summarizeRateLimit(*service.sessionSignals.rate, service.location)
	}
	snapshot := AppSnapshot{
		Account:     service.account,
		Config:      service.config,
		Runtime:     mergeRuntimeStatus(service.config, runtimeSource),
		RateLimit:   rateLimit,
		Statistics:  statistics,
		RefreshedAt: now,
	}
	service.lastInventory = inventory
	service.hasInventory = true
	return snapshot, nil
}

func (service *Service) readStatistics(
	ctx context.Context,
	now time.Time,
	sessionFiles []sessionFile,
	invalidatedKeys map[string]struct{},
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
	snapshot, nextCache, changed, err := updateStatistics(ctx, statisticsUpdateRequest{
		Files:           sessionFiles,
		Now:             now,
		Location:        service.location,
		Cache:           service.statisticsCache,
		InvalidatedKeys: invalidatedKeys,
		Metrics:         service.metrics,
	})
	if changed {
		service.statisticsCache = nextCache
		service.statisticsCacheDirty = true
	}
	if err == nil {
		service.persistStatisticsCache(now, false)
	}
	return snapshot, err
}

func limitSessionFiles(files []sessionFile, limit int) []sessionFile {
	if len(files) <= limit {
		return files
	}
	return files[:limit]
}

func sameSessionFile(left sessionFile, right sessionFile) bool {
	samePath := cachePathKey(left.path) == cachePathKey(right.path)
	sameMetadata := left.modTime == right.modTime && left.size == right.size &&
		left.mode == right.mode
	return samePath && sameMetadata && sameFileIdentity(left.info, right.info)
}

func (service *Service) statisticsSnapshot(now time.Time) StatisticsSnapshot {
	service.cacheMu.Lock()
	defer service.cacheMu.Unlock()
	summaries := make([]sessionSummary, 0, len(service.statisticsCache.Files))
	for _, file := range service.statisticsCache.Files {
		summaries = append(summaries, file.Summary)
	}
	return buildStatisticsSnapshot(summaries, now, service.location)
}

func cloneRateLimitSummary(summary RateLimitSummary) RateLimitSummary {
	summary.Primary = cloneRateLimitWindow(summary.Primary)
	summary.Secondary = cloneRateLimitWindow(summary.Secondary)
	return summary
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
