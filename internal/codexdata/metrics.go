package codexdata

import "sync/atomic"

type sourceReadKind uint8

const (
	sourceReadConfig sourceReadKind = iota
	sourceReadAuth
	sourceReadStatistics
)

// ReadCounters is a point-in-time copy of opt-in data-service instrumentation.
// Counters contain only aggregate numbers and never source contents or paths.
type ReadCounters struct {
	TailBytes          int64
	WalkFiles          int64
	SnapshotsStarted   int64
	SnapshotsPublished int64
	SnapshotsCanceled  int64
}

// ReadMetrics collects aggregate read and refresh counters without logging.
// Pass one through Options when tests or diagnostics need measurements.
type ReadMetrics struct {
	tailBytes           atomic.Int64
	walkFiles           atomic.Int64
	snapshotsStarted    atomic.Int64
	snapshotsPublished  atomic.Int64
	snapshotsCanceled   atomic.Int64
	tailReadHook        func(int)
	tailReadErrorHook   func(int) error
	inventoryHook       func() error
	sourceReadErrorHook func(sourceReadKind) error
}

// Counters returns a consistent-enough point-in-time counter snapshot.
func (metrics *ReadMetrics) Counters() ReadCounters {
	if metrics == nil {
		return ReadCounters{}
	}
	return ReadCounters{
		TailBytes:          metrics.tailBytes.Load(),
		WalkFiles:          metrics.walkFiles.Load(),
		SnapshotsStarted:   metrics.snapshotsStarted.Load(),
		SnapshotsPublished: metrics.snapshotsPublished.Load(),
		SnapshotsCanceled:  metrics.snapshotsCanceled.Load(),
	}
}

func (metrics *ReadMetrics) addTailBytes(count int) error {
	if metrics != nil && count > 0 {
		metrics.tailBytes.Add(int64(count))
		if metrics.tailReadHook != nil {
			metrics.tailReadHook(count)
		}
		if metrics.tailReadErrorHook != nil {
			return metrics.tailReadErrorHook(count)
		}
	}
	return nil
}

func (metrics *ReadMetrics) addWalkFile() {
	if metrics != nil {
		metrics.walkFiles.Add(1)
	}
}

func (metrics *ReadMetrics) beforeSourceRead(kind sourceReadKind) error {
	if metrics == nil || metrics.sourceReadErrorHook == nil {
		return nil
	}
	return metrics.sourceReadErrorHook(kind)
}

func (metrics *ReadMetrics) snapshotStarted() {
	if metrics != nil {
		metrics.snapshotsStarted.Add(1)
	}
}

func (metrics *ReadMetrics) snapshotPublished() {
	if metrics != nil {
		metrics.snapshotsPublished.Add(1)
	}
}

func (metrics *ReadMetrics) snapshotCanceled() {
	if metrics != nil {
		metrics.snapshotsCanceled.Add(1)
	}
}
