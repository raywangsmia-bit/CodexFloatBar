package codexdata

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Options makes every filesystem and clock dependency explicit for tests.
type Options struct {
	Paths    Paths
	Location *time.Location
	Now      func() time.Time
}

// Service reads immutable snapshots without retaining raw Codex content.
type Service struct {
	paths    Paths
	location *time.Location
	now      func() time.Time
	cacheMu  sync.Mutex
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
		paths:    paths,
		location: location,
		now:      now,
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

	var session RuntimeStatus
	var rateLimit RateLimitSummary
	var statistics StatisticsSnapshot
	var statisticsErr error
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		session = readLatestSessionStatus(
			ctx,
			service.paths.Sessions,
			service.paths.Logs,
		)
	}()
	go func() {
		defer wait.Done()
		rateLimit = readLatestRateLimit(
			ctx,
			service.paths.Sessions,
			service.paths.Logs,
			service.location,
		)
	}()
	go func() {
		defer wait.Done()
		service.cacheMu.Lock()
		defer service.cacheMu.Unlock()
		statistics, statisticsErr = readStatistics(
			ctx,
			service.paths.Sessions,
			service.paths.Cache,
			now,
			service.location,
		)
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
