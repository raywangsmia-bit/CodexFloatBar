//go:build windows

package codexprocess

import (
	"context"
	"time"
)

const (
	defaultPollInterval  = 2 * time.Second
	defaultMissThreshold = 3
	defaultEventDebounce = 100 * time.Millisecond
)

// Status is emitted when the confirmed Codex running state changes.
type Status struct {
	Running   bool
	Visible   bool
	CheckedAt time.Time
}

type observedState struct {
	Running bool
	Visible bool
}

// Detector reports whether a supported Codex desktop process is running.
type Detector interface {
	Running(context.Context) (bool, error)
}

// DetectorFunc adapts a function to Detector.
type DetectorFunc func(context.Context) (bool, error)

// Running calls the adapted detector function.
func (function DetectorFunc) Running(ctx context.Context) (bool, error) {
	return function(ctx)
}

type stateDetector interface {
	State(context.Context) (observedState, error)
}

type eventStateDetector interface {
	EventState(context.Context) (observedState, error)
}

// Options controls the monitor without changing the WPF-compatible defaults.
type Options struct {
	PollInterval  time.Duration
	MissThreshold int
	Detector      Detector
	Now           func() time.Time
	EventDebounce time.Duration
}

// Monitor polls at low frequency and publishes only confirmed state changes.
type Monitor struct {
	pollInterval  time.Duration
	missThreshold int
	detector      Detector
	now           func() time.Time
	updates       chan Status
	refresh       chan struct{}
	eventDebounce time.Duration
}

// NewMonitor creates an idle single-use monitor.
func NewMonitor(options Options) *Monitor {
	interval := options.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	missThreshold := options.MissThreshold
	if missThreshold <= 0 {
		missThreshold = defaultMissThreshold
	}
	detector := options.Detector
	if detector == nil {
		detector = newWindowsDetector()
	}
	eventDebounce := options.EventDebounce
	if eventDebounce <= 0 {
		eventDebounce = defaultEventDebounce
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Monitor{
		pollInterval:  interval,
		missThreshold: missThreshold,
		detector:      detector,
		now:           now,
		updates:       make(chan Status, 1),
		refresh:       make(chan struct{}, 1),
		eventDebounce: eventDebounce,
	}
}

// Updates returns latest-value state notifications and closes when Run exits.
func (monitor *Monitor) Updates() <-chan Status {
	return monitor.updates
}

// Refresh schedules a debounced visibility-only check.
func (monitor *Monitor) Refresh() {
	select {
	case monitor.refresh <- struct{}{}:
	default:
	}
}

// Run performs an immediate check and then polls until ctx is canceled.
func (monitor *Monitor) Run(ctx context.Context) error {
	defer close(monitor.updates)
	tracker := stateTracker{missThreshold: monitor.missThreshold}
	if err := monitor.poll(ctx, &tracker); err != nil {
		return err
	}

	ticker := time.NewTicker(monitor.pollInterval)
	defer ticker.Stop()
	var eventTimer *time.Timer
	var eventTimerChannel <-chan time.Time
	defer func() {
		if eventTimer != nil {
			eventTimer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := monitor.poll(ctx, &tracker); err != nil {
				return err
			}
		case <-monitor.refresh:
			if eventTimer == nil {
				eventTimer = time.NewTimer(monitor.eventDebounce)
				eventTimerChannel = eventTimer.C
			}
		case <-eventTimerChannel:
			eventTimer = nil
			eventTimerChannel = nil
			if err := monitor.pollEvent(ctx, &tracker); err != nil {
				return err
			}
		}
	}
}

func (monitor *Monitor) poll(ctx context.Context, tracker *stateTracker) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	observed, err := observeDetector(ctx, monitor.detector)
	return monitor.acceptObservation(ctx, tracker, observed, err)
}

func (monitor *Monitor) pollEvent(ctx context.Context, tracker *stateTracker) error {
	detector, ok := monitor.detector.(eventStateDetector)
	if !ok {
		return monitor.poll(ctx, tracker)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	observed, err := detector.EventState(ctx)
	return monitor.acceptObservation(ctx, tracker, observed, err)
}

func (monitor *Monitor) acceptObservation(
	ctx context.Context,
	tracker *stateTracker,
	observed observedState,
	err error,
) error {
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		tracker.resetMisses()
		return nil
	}
	confirmed, changed := tracker.observe(observed)
	if changed {
		publishLatest(monitor.updates, Status{
			Running:   confirmed.Running,
			Visible:   confirmed.Visible,
			CheckedAt: monitor.now(),
		})
	}
	return nil
}

type stateTracker struct {
	known         bool
	running       bool
	visible       bool
	misses        int
	missThreshold int
}

func (tracker *stateTracker) observe(observed observedState) (observedState, bool) {
	if observed.Running {
		tracker.misses = 0
		changed := !tracker.known || !tracker.running || tracker.visible != observed.Visible
		tracker.known = true
		tracker.running = true
		tracker.visible = observed.Visible
		return observedState{Running: true, Visible: observed.Visible}, changed
	}

	tracker.misses++
	if tracker.misses < tracker.missThreshold {
		return observedState{Running: tracker.running, Visible: tracker.visible}, false
	}
	tracker.misses = tracker.missThreshold
	if tracker.known && !tracker.running {
		return observedState{}, false
	}
	tracker.known = true
	tracker.running = false
	tracker.visible = false
	return observedState{}, true
}

func observeDetector(ctx context.Context, detector Detector) (observedState, error) {
	if detectorWithState, ok := detector.(stateDetector); ok {
		return detectorWithState.State(ctx)
	}
	running, err := detector.Running(ctx)
	if err != nil {
		return observedState{}, err
	}
	return observedState{Running: running, Visible: running}, nil
}

func (tracker *stateTracker) resetMisses() {
	tracker.misses = 0
}

func publishLatest(updates chan Status, status Status) {
	select {
	case updates <- status:
		return
	default:
	}
	select {
	case <-updates:
	default:
	}
	updates <- status
}
