package sector

import (
	"time"

	"spaceempire/back/internal/domain"
)

// WorkerStats is a point-in-time snapshot of a Worker's metrics. Used by
// Pool.Stats and exposed for tests; Prometheus integration is a separate
// task (phase 7.1).
type WorkerStats struct {
	WorkerIndex int
	Overruns    uint64
	Sectors     []SectorStats
}

// SectorStats is the per-sector slice of metrics that the worker exposes.
// Captured from the latest atomic Snapshot, so it is consistent with what
// HTTP/WS clients see.
type SectorStats struct {
	SectorID         domain.SectorID
	Tick             uint64
	LastTickDuration time.Duration
	Ships            int

	// Handoffs out of / into this sector since worker start, keyed by the
	// counterpart sector. Maps may be nil.
	HandoffsOut map[domain.SectorID]uint64
	HandoffsIn  map[domain.SectorID]uint64

	// ProductionCycles is the number of station factory cycles completed
	// in this sector since worker start. Maps to the future Prometheus
	// counter production_cycles_total{sector}.
	ProductionCycles uint64
}

// PoolStats aggregates WorkerStats across every worker in the pool.
type PoolStats struct {
	Workers []WorkerStats
}

// HandoffsTotal flattens HandoffsOut across every sector in the pool into
// the (from, to) → count form expected by the prometheus exporter (phase
// 7.1) and tests in this phase. Returns nil when no handoffs have happened.
func (s PoolStats) HandoffsTotal() map[HandoffEdge]uint64 {
	var out map[HandoffEdge]uint64
	for _, w := range s.Workers {
		for _, sec := range w.Sectors {
			for to, n := range sec.HandoffsOut {
				if out == nil {
					out = make(map[HandoffEdge]uint64)
				}
				out[HandoffEdge{From: sec.SectorID, To: to}] += n
			}
		}
	}
	return out
}

// HandoffEdge identifies one directional sector→sector handoff bucket.
type HandoffEdge struct {
	From domain.SectorID
	To   domain.SectorID
}

// Stats returns a fresh WorkerStats reading. Cheap; safe to call from any
// goroutine because it touches only atomically-published snapshots and
// counters.
func (w *Worker) Stats() WorkerStats {
	sectors := make([]SectorStats, 0, len(w.sectors))
	for id, s := range w.sectors {
		snap := s.snap.Load()
		if snap == nil {
			sectors = append(sectors, SectorStats{SectorID: id})
			continue
		}
		sectors = append(sectors, SectorStats{
			SectorID:         id,
			Tick:             snap.Tick,
			LastTickDuration: snap.LastTickDuration,
			Ships:            len(snap.Ships),
			HandoffsOut:      snap.HandoffsOut,
			HandoffsIn:       snap.HandoffsIn,
			ProductionCycles: snap.ProductionCycles,
		})
	}
	return WorkerStats{
		WorkerIndex: w.idx,
		Overruns:    w.overruns.Load(),
		Sectors:     sectors,
	}
}
