package sector

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
)

// Pool wires N Workers to M sectors. The starter configuration is N=1
// (one worker owns every sector); larger N round-robins sectors across
// workers, which is the on-ramp to per-process sharding when one worker
// can no longer keep up.
type Pool struct {
	workers []*Worker
	routing map[domain.SectorID]int
	logger  *slog.Logger
}

// NewPool builds a Pool that owns every sector in `sectors`. `initial` may
// be nil or partial — sectors absent from it start with no ships. The
// distribution is deterministic round-robin over a sorted sector list so
// the same input always yields the same routing.
func NewPool(
	cfg PoolConfig,
	sectors []domain.SectorID,
	clk clock.Clock,
	repo ShipRepo,
	logger *slog.Logger,
	initial map[domain.SectorID][]domain.Ship,
	opts ...Option,
) *Pool {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	sorted := append([]domain.SectorID(nil), sectors...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Distribute sectors round-robin into per-worker buckets.
	buckets := make([]map[domain.SectorID][]domain.Ship, cfg.WorkersCount)
	for i := range buckets {
		buckets[i] = make(map[domain.SectorID][]domain.Ship)
	}
	routing := make(map[domain.SectorID]int, len(sorted))
	for i, id := range sorted {
		idx := i % cfg.WorkersCount
		buckets[idx][id] = initial[id]
		routing[id] = idx
	}

	workers := make([]*Worker, cfg.WorkersCount)
	for i := range workers {
		workers[i] = NewWorker(i, cfg.Worker, clk, repo, logger, buckets[i], opts...)
	}

	return &Pool{
		workers: workers,
		routing: routing,
		logger:  logger.With("component", "sector.pool"),
	}
}

// Workers exposes the underlying workers for tests and stats. Production
// callers should go through Send/Snapshot/Subscribe.
func (p *Pool) Workers() []*Worker {
	return p.workers
}

// Send routes the command to the worker that owns sectorID. Returns
// ErrSectorNotFound when no worker owns the sector and ErrInboxFull when
// the target worker's inbox is full.
func (p *Pool) Send(sectorID domain.SectorID, cmd Command) error {
	w, err := p.workerFor(sectorID)
	if err != nil {
		return err
	}
	return w.Send(sectorID, cmd)
}

// Snapshot returns the latest published snapshot for the sector, or a
// zero Snapshot if no worker owns it.
func (p *Pool) Snapshot(sectorID domain.SectorID) Snapshot {
	w, err := p.workerFor(sectorID)
	if err != nil {
		return Snapshot{}
	}
	return w.Snapshot(sectorID)
}

// Subscribe opens a per-player patch stream on the given sector.
func (p *Pool) Subscribe(ctx context.Context, sectorID domain.SectorID, playerID domain.PlayerID) (*Subscription, func(), error) {
	w, err := p.workerFor(sectorID)
	if err != nil {
		return nil, nil, err
	}
	return w.Subscribe(ctx, sectorID, playerID)
}

// Start synchronously wires every worker's intake subscription. Callers
// that launch Run in a goroutine should call Start first to guarantee that
// any Publish issued immediately afterwards reaches the worker — without
// it, Run-internal EnsureSubscriptions may race the publisher.
// Idempotent; safe to call twice.
func (p *Pool) Start(ctx context.Context) {
	for _, w := range p.workers {
		w.EnsureSubscriptions(ctx)
	}
}

// Run launches every worker as a goroutine and returns when ctx is
// canceled and every worker has exited. The first non-nil error from any
// worker.Run is returned. Run calls Start internally for production paths
// that do not invoke Start explicitly; tests that race Publish against the
// pool startup should still call Start themselves.
func (p *Pool) Run(ctx context.Context) error {
	p.Start(ctx)

	var wg sync.WaitGroup
	errs := make(chan error, len(p.workers))
	for _, w := range p.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- w.Run(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// LookupShipSector scans every owned worker's latest snapshot for a ship
// with the given id. Returns (sector, true) on hit, (0, false) when the
// ship is not currently in RAM anywhere — either it has never existed or
// it was deleted on the previous tick. Cost is O(workers × ships) per
// call; intended for low-frequency endpoints like /api/cmd/set-course.
func (p *Pool) LookupShipSector(shipID domain.ShipID) (domain.SectorID, bool) {
	for _, w := range p.workers {
		for _, sectorID := range w.Sectors() {
			snap := w.Snapshot(sectorID)
			for i := range snap.Ships {
				if snap.Ships[i].ID == shipID {
					return snap.Ships[i].SectorID, true
				}
			}
		}
	}
	return 0, false
}

// LookupPrimaryShipByPlayer returns the ship currently considered primary for
// the player — the lowest-id ship the player owns across every snapshot.
// Phase 3.14 uses it on WS subscribe so the client connects to the sector
// that actually hosts the player's ship, not whatever sector cfg.SectorID
// happens to point at. (0, 0, false) when the player has no ships in RAM —
// callers fall back to the configured default sector.
func (p *Pool) LookupPrimaryShipByPlayer(playerID domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	var (
		best    domain.ShipID
		bestSec domain.SectorID
		bestSet bool
	)
	for _, w := range p.workers {
		for _, sectorID := range w.Sectors() {
			snap := w.Snapshot(sectorID)
			for i := range snap.Ships {
				if snap.Ships[i].PlayerID != playerID {
					continue
				}
				if !bestSet || snap.Ships[i].ID < best {
					best = snap.Ships[i].ID
					bestSec = snap.Ships[i].SectorID
					bestSet = true
				}
			}
		}
	}
	if !bestSet {
		return 0, 0, false
	}
	return best, bestSec, true
}

// ShipsByPlayer returns every ship the player owns across all sectors in RAM
// (phase 10.14a fleet list). Read from the per-sector snapshots, so it is
// safe to call from any goroutine. Order is unspecified.
func (p *Pool) ShipsByPlayer(playerID domain.PlayerID) []domain.Ship {
	var out []domain.Ship
	for _, w := range p.workers {
		for _, sectorID := range w.Sectors() {
			snap := w.Snapshot(sectorID)
			for i := range snap.Ships {
				if snap.Ships[i].PlayerID == playerID {
					out = append(out, snap.Ships[i])
				}
			}
		}
	}
	return out
}

// Stats aggregates per-worker stats. Cheap, safe to call from any
// goroutine.
func (p *Pool) Stats() PoolStats {
	out := PoolStats{Workers: make([]WorkerStats, len(p.workers))}
	for i, w := range p.workers {
		out.Workers[i] = w.Stats()
	}
	return out
}

func (p *Pool) workerFor(sectorID domain.SectorID) (*Worker, error) {
	idx, ok := p.routing[sectorID]
	if !ok {
		return nil, ErrSectorNotFound
	}
	return p.workers[idx], nil
}
