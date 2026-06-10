package sector

import (
	"time"

	"spaceempire/back/internal/domain"
)

// MetricsSink receives per-tick telemetry from the worker (phase 7.1).
// Implementations must be safe for concurrent use and cheap — these are
// called on the hot tick path. A nil sink is never passed: NewWorker defaults
// to noopMetrics, so call sites need no guard.
type MetricsSink interface {
	// RecordTick reports one sector tick: total duration, the snapshot+
	// broadcast slice of it, the live ship count, the dirty-ship count, and
	// the current time-scale.
	RecordTick(sectorID domain.SectorID, tickDur, snapshotDur time.Duration, shipCount, dirtyCount int, timeScale float64)
	// IncTickOverrun counts a tick that exceeded the tick interval.
	IncTickOverrun(workerIdx int)
	// SetQueueDepth reports the worker's inbox depth (sampled per tick).
	SetQueueDepth(workerIdx, depth int)
	// IncHandoff counts a player sector handoff.
	IncHandoff(from, to domain.SectorID)
}

// noopMetrics is the default sink — every method is a no-op.
type noopMetrics struct{}

func (noopMetrics) RecordTick(domain.SectorID, time.Duration, time.Duration, int, int, float64) {}
func (noopMetrics) IncTickOverrun(int)                                                          {}
func (noopMetrics) SetQueueDepth(int, int)                                                      {}
func (noopMetrics) IncHandoff(domain.SectorID, domain.SectorID)                                 {}

// WithMetrics wires a MetricsSink. Without it the worker uses noopMetrics.
func WithMetrics(sink MetricsSink) Option {
	return func(w *Worker) {
		if sink != nil {
			w.metrics = sink
		}
	}
}
