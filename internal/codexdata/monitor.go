package codexdata

import (
	"context"
	"errors"
	"time"
)

// MonitorOptions controls periodic source-inventory checks.
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

// Run performs the initial read, then refreshes only after source changes or Refresh.
func (monitor *Monitor) Run(ctx context.Context) error {
	defer close(monitor.updates)
	defer monitor.service.FlushStatisticsCache()
	ticker := time.NewTicker(monitor.pollInterval)
	defer ticker.Stop()

	inventory, inventoryErr := collectSourceInventory(
		ctx,
		monitor.service.paths,
		monitor.service.metrics,
	)
	if inventoryErr != nil && (errors.Is(inventoryErr, context.Canceled) ||
		errors.Is(inventoryErr, context.DeadlineExceeded)) {
		return inventoryErr
	}
	observed := inventory
	pendingInventory := inventory
	haveInventory := inventoryErr == nil
	results := make(chan refreshResult, 1)
	var refreshCancel context.CancelFunc
	running := false
	pending := haveInventory
	retry := false

	startRefresh := func() {
		if !haveInventory || !pending {
			return
		}
		refreshInventory := pendingInventory
		refreshCtx, cancel := context.WithCancel(ctx)
		refreshCancel = cancel
		running = true
		pending = false
		retry = false
		go func() {
			snapshot, err := monitor.service.snapshotFromInventory(
				refreshCtx,
				refreshInventory,
			)
			results <- refreshResult{snapshot: snapshot, err: err}
		}()
	}
	if pending {
		startRefresh()
	}

	for {
		select {
		case <-ctx.Done():
			if refreshCancel != nil {
				refreshCancel()
			}
			return ctx.Err()
		case <-monitor.refreshes:
			nextInventory, err := collectSourceInventory(
				ctx,
				monitor.service.paths,
				monitor.service.metrics,
			)
			if err != nil {
				if errors.Is(err, context.Canceled) ||
					errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				retry = true
				continue
			}
			haveInventory = true
			observed = nextInventory
			pendingInventory = nextInventory
			retry = false
			pending = true
			if running && refreshCancel != nil {
				refreshCancel()
				continue
			}
			startRefresh()
		case <-ticker.C:
			nextInventory, err := collectSourceInventory(
				ctx,
				monitor.service.paths,
				monitor.service.metrics,
			)
			if err != nil {
				continue
			}
			inventoryChanged := !haveInventory ||
				diffSourceInventory(observed, nextInventory) != 0
			if !inventoryChanged && !retry {
				continue
			}
			haveInventory = true
			observed = nextInventory
			pendingInventory = nextInventory
			retry = false
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
					monitor.service.metrics.snapshotPublished()
				} else {
					monitor.service.metrics.snapshotCanceled()
				}
			} else if !errors.Is(result.err, context.Canceled) &&
				!errors.Is(result.err, context.DeadlineExceeded) {
				retry = true
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
