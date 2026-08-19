//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raywangsmia-bit/CodexFloatBar/internal/appidentity"
	"github.com/raywangsmia-bit/CodexFloatBar/internal/codexdata"
	"golang.org/x/sys/windows"
)

const statusCacheSaveInterval = 60 * time.Second

type statusRuntime struct {
	monitor           *codexdata.Monitor
	current           codexdata.AppSnapshot
	pending           atomic.Pointer[codexdata.AppSnapshot]
	cancel            context.CancelFunc
	startOnce         sync.Once
	stopOnce          sync.Once
	disabled          bool
	startErr          error
	quota             quotaLevelObserver
	statistics        statisticsSelection
	presentation      uiPresentation
	presentationKnown bool
}

type quotaLevelObserver struct {
	previous quotaTone
	known    bool
}

func newStatusRuntime() *statusRuntime {
	paths, err := defaultCodexDataPaths()
	if err != nil {
		return &statusRuntime{startErr: err}
	}
	service := codexdata.NewService(codexdata.Options{
		Paths:             paths,
		Location:          time.Local,
		Now:               time.Now,
		CacheSaveInterval: statusCacheSaveInterval,
	})
	return &statusRuntime{
		monitor: codexdata.NewMonitor(service, codexdata.MonitorOptions{}),
	}
}

func defaultCodexDataPaths() (codexdata.Paths, error) {
	userHome, err := windows.KnownFolderPath(windows.FOLDERID_Profile, 0)
	if err != nil {
		return codexdata.Paths{}, fmt.Errorf("locating the user profile: %w", err)
	}
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return codexdata.Paths{}, fmt.Errorf("locating local app data: %w", err)
	}
	return codexdata.DefaultPaths(userHome, localAppData), nil
}

func (runtime *statusRuntime) disable() {
	runtime.disabled = true
}

func (runtime *statusRuntime) start(window uintptr) error {
	if runtime == nil || runtime.disabled {
		return nil
	}
	runtime.startOnce.Do(func() {
		if runtime.startErr != nil {
			return
		}
		if runtime.monitor == nil {
			runtime.startErr = errors.New("Codex data monitor is unavailable")
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		runtime.cancel = cancel
		go runtime.forwardUpdates(window)
		go runtime.runMonitor(ctx)
	})
	return runtime.startErr
}

func (runtime *statusRuntime) runMonitor(ctx context.Context) {
	err := runtime.monitor.Run(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("Codex data monitor stopped: %v", err)
	}
}

func (runtime *statusRuntime) forwardUpdates(window uintptr) {
	for snapshot := range runtime.monitor.Updates() {
		latest := snapshot
		runtime.pending.Store(&latest)
		posted, _, lastErr := procPostMessageW.Call(window, wmNativeStatusChanged, 0, 0)
		if posted == 0 {
			log.Printf("posting Codex status update: %v", lastErr)
		}
	}
}

func (runtime *statusRuntime) acceptPending() bool {
	if runtime == nil {
		return false
	}
	snapshot := runtime.pending.Swap(nil)
	if snapshot == nil {
		return false
	}
	nextStatistics := normalizeStatisticsSelection(
		*snapshot,
		runtime.statistics,
	)
	nextPresentation := presentSnapshotWithStatistics(*snapshot, nextStatistics)
	changed := !runtime.presentationKnown ||
		!sameUIPresentation(runtime.presentation, nextPresentation)
	runtime.current = *snapshot
	runtime.statistics = nextStatistics
	runtime.presentation = nextPresentation
	runtime.presentationKnown = true
	return changed
}

func (runtime *statusRuntime) compose(surface *renderedSurface) (*renderedSurface, error) {
	return composeRenderedSurfaceWithPresentation(surface, runtime.currentPresentation())
}

func (runtime *statusRuntime) currentPresentation() uiPresentation {
	if !runtime.presentationKnown {
		runtime.presentation = presentSnapshotWithStatistics(
			runtime.current,
			runtime.statistics,
		)
		runtime.presentationKnown = true
	}
	return runtime.presentation
}

func (runtime *statusRuntime) applyStatisticsAction(action string) bool {
	if runtime == nil {
		return false
	}
	current := normalizeStatisticsSelection(runtime.current, runtime.statistics)
	next := current
	switch action {
	case "statistics-view-month":
		next.View = statisticsViewMonth
	case "statistics-view-week":
		next.View = statisticsViewWeek
	case "statistics-view-cumulative":
		next.View = statisticsViewCumulative
	case "statistics-view-detail":
		next.View = statisticsViewDetail
	case "statistics-previous-month":
		if !statisticsMonthNavigationVisible(current) || current.Month.IsZero() {
			return false
		}
		next.Month = current.Month.AddDate(0, -1, 0)
		next.SelectedDay = 0
	case "statistics-next-month":
		if !statisticsMonthNavigationVisible(current) || current.Month.IsZero() {
			return false
		}
		next.Month = current.Month.AddDate(0, 1, 0)
		next.SelectedDay = 0
	default:
		day, ok := runtime.statisticsDayForAction(current, action)
		if !ok {
			return false
		}
		if current.SelectedDay == day {
			next.SelectedDay = 0
		} else {
			next.SelectedDay = day
		}
		next.View = statisticsViewDetail
	}
	next = normalizeStatisticsSelection(runtime.current, next)
	if sameStatisticsSelection(current, next) {
		return false
	}
	runtime.statistics = next
	runtime.presentationKnown = false
	return true
}

func sameStatisticsSelection(left statisticsSelection, right statisticsSelection) bool {
	return left.View == right.View && left.Month.Equal(right.Month) &&
		left.SelectedDay == right.SelectedDay
}

func (runtime *statusRuntime) statisticsDayForAction(
	selection statisticsSelection,
	action string,
) (int, bool) {
	const prefix = "statistics-select-day-"
	if selection.View != statisticsViewMonth ||
		!strings.HasPrefix(action, prefix) || selection.Month.IsZero() {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(action, prefix))
	if err != nil || index < 0 || index >= 42 {
		return 0, false
	}
	monthStart := time.Date(
		selection.Month.Year(),
		selection.Month.Month(),
		1,
		0,
		0,
		0,
		0,
		selection.Month.Location(),
	)
	firstOffset := (int(monthStart.Weekday()) + 6) % 7
	day := index - firstOffset + 1
	lastDay := monthStart.AddDate(0, 1, -1).Day()
	if day < 1 || day > lastDay {
		return 0, false
	}
	selected := time.Date(
		monthStart.Year(),
		monthStart.Month(),
		day,
		0,
		0,
		0,
		0,
		monthStart.Location(),
	)
	refreshed := runtime.current.Statistics.RefreshedAt.In(monthStart.Location())
	if !refreshed.IsZero() && selected.After(refreshed) {
		return 0, false
	}
	return day, true
}

func (runtime *statusRuntime) tone() quotaTone {
	if runtime == nil {
		return quotaToneOffline
	}
	return runtime.currentPresentation().Tone
}

func (runtime *statusRuntime) shouldShowUsageToast() bool {
	if runtime == nil {
		return false
	}
	return runtime.quota.observe(runtime.tone())
}

func (observer *quotaLevelObserver) observe(next quotaTone) bool {
	switch next {
	case quotaToneGood, quotaToneWarn, quotaToneDanger:
	case quotaToneOffline:
		observer.previous = ""
		observer.known = false
		return false
	default:
		observer.previous = ""
		observer.known = false
		return false
	}

	notify := observer.known && quotaToneWorsened(observer.previous, next)
	observer.previous = next
	observer.known = true
	return notify
}

func quotaToneWorsened(previous quotaTone, next quotaTone) bool {
	switch previous {
	case quotaToneGood:
		return next == quotaToneWarn || next == quotaToneDanger
	case quotaToneWarn:
		return next == quotaToneDanger
	default:
		return false
	}
}

func (runtime *statusRuntime) refresh() bool {
	if runtime == nil || runtime.disabled || runtime.monitor == nil {
		return false
	}
	runtime.monitor.Refresh()
	return true
}

func (runtime *statusRuntime) clipboardText(codexRunning bool) string {
	var snapshot codexdata.AppSnapshot
	if runtime != nil {
		snapshot = runtime.current
	}
	presentation := presentSnapshot(snapshot)
	codexState := "未运行"
	if codexRunning {
		codexState = "运行中"
	}
	lines := []string{
		appidentity.ProductName,
		displayOr(snapshot.Account.DisplayText, "Codex: not signed in"),
		"Codex：" + codexState,
		"模型：" + presentation.Text["runtime.model"],
		"推理强度：" + presentation.Text["runtime.effort"],
		"速率：" + presentation.Text["runtime.speed"],
		"一周额度：" + presentation.Text["quota.remaining"],
		"额度重置：" + presentation.Text["quota.reset"],
	}
	return strings.Join(lines, "\r\n")
}

func (runtime *statusRuntime) stop() {
	if runtime == nil {
		return
	}
	runtime.stopOnce.Do(func() {
		if runtime.cancel != nil {
			runtime.cancel()
		}
	})
}

func (app *nativeApp) startStatusMonitor() error {
	if app.status == nil {
		return nil
	}
	return app.status.start(app.window)
}

func (app *nativeApp) stopStatusMonitor() {
	if app.status != nil {
		app.status.stop()
	}
}
