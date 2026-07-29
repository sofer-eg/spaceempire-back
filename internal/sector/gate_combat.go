package sector

import (
	"context"

	"spaceempire/back/internal/domain"
)

// GateRepo persists gate destruction (TASK-110). Wired via WithGates; nil leaves
// destruction RAM-only, exactly like the tower/satellite/jammer repos.
type GateRepo interface {
	// MarkDestroyed flags the gate so the next cold start leaves it — and its
	// sector link — out of the topology.
	MarkDestroyed(ctx context.Context, id domain.GateID) error
}

// WithGates enables gate combat persistence. Without it a destroyed gate is
// severed in the live topology but comes back on the next restart.
func WithGates(repo GateRepo) Option {
	return func(w *Worker) {
		w.gateRepo = repo
	}
}

// seedGateEndpoints registers this sector's side of every live gate as a
// destructible (TASK-110). Called at cold start for each owned sector, once the
// options have supplied the topology.
//
// A gate has two endpoints and each linked sector's worker owns only its own: the
// two sides take damage independently, because one live HP pool cannot have two
// writers. Destroying either side destroys the gate — see killGate.
//
// Gates deliberately stay out of domain.SectorStatics: they are world topology,
// not sector layout, and the topology is where every consumer already looks them
// up (jumps, routes, /api/world). Only the combat state lands here.
func (w *Worker) seedGateEndpoints(s *sectorState) {
	if w.topology == nil {
		return
	}
	for _, g := range w.topology.GatesInSector(s.sectorID) {
		pos, ok := g.EndpointPos(s.sectorID)
		if !ok {
			continue
		}
		ref := g.ObjectID()
		d := domain.DestructibleStatic{
			Ref: ref,
			Pos: pos,
			// No OwnerID: gates belong to no player, so the hostility oracle
			// treats them the way it treats a pirbase — see hostileToGate.
			HP:             g.HP,
			Shield:         g.Shield,
			MaxShield:      g.MaxShield,
			ShieldRecharge: g.ShieldRecharge,
		}
		s.destructibles[ref] = &d
	}
}

// killGate is the gate branch of a static kill: it severs the link in the live
// topology (which is what stops jumps and re-routes every ship in flight),
// persists the wreck, and — since the twin sector's worker is not this goroutine —
// leaves the other endpoint to sweepDestroyedGates.
//
// DestroyGate reports whether the gate was still in the graph. A false means the
// other endpoint died first (both sides can be shot in the same tick pair), so the
// sever and the persist already happened and this is just the second half
// catching up.
func (w *Worker) killGate(ctx context.Context, id domain.GateID) {
	if w.topology == nil {
		return
	}
	if !w.topology.DestroyGate(id) {
		return
	}
	if w.gateRepo == nil {
		return
	}
	// Best-effort, same contract as the tower/satellite/jammer persists: a failure
	// leaves the link severed in RAM and restored on the next restart, but never
	// blocks the kill.
	err := w.dbCall(ctx, func(ctx context.Context) error {
		return w.gateRepo.MarkDestroyed(ctx, id)
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "kill: persist gate destruction", "err", err, "gate", int64(id))
	}
}

// sweepDestroyedGates drops gate endpoints whose gate is no longer in the
// topology, once per tick. It is how the twin sector learns its side is gone: the
// gate was destroyed by the OTHER sector's worker, which cannot touch this
// sector's state (one writer per sector), so instead of a cross-worker message
// each side reconciles against the shared topology it already reads.
//
// Cheap by construction: a sector has a handful of gates, and the loop only runs
// over endpoints already in the destructible map.
func (w *Worker) sweepDestroyedGates(s *sectorState) {
	if w.topology == nil {
		return
	}
	for ref := range s.destructibles {
		if ref.Kind != domain.EntityKindGate {
			continue
		}
		if w.topology.Gate(domain.GateID(ref.ID)) == nil {
			s.removeDestructible(ref)
		}
	}
}
