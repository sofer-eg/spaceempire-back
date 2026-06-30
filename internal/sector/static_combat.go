package sector

import (
	"context"
	"encoding/json"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// IsStaticTargetKind reports whether k is a destructible static a weapon may
// lock onto besides ships (phase 6.2b): station, shipyard, trade station,
// pirbase, laser tower, satellite. Gates are intentionally excluded — they are
// not destructible (ЧТЗ C-04, lifted by TASK-110). Exported so the HTTP launch
// handlers gate on the exact same set the worker enforces (TASK-113 FR-06,
// NFR-03 — one source of truth for the targetable-static set).
func IsStaticTargetKind(k domain.EntityKind) bool {
	switch k {
	case domain.EntityKindStation, domain.EntityKindShipyard,
		domain.EntityKindTradeStation, domain.EntityKindPirbase,
		domain.EntityKindLaserTower, domain.EntityKindSatellite:
		return true
	}
	return false
}

// chargeStatics recharges every destructible static's shield one tick, the
// static counterpart of chargeShields (port of TO_ObjectShieldCharge). Each
// shield that changed is marked dirty for the WS combat delta.
func chargeStatics(s *sectorState) {
	for ref, d := range s.destructibles {
		if d.ChargeShield() {
			s.markDestructibleDirty(ref)
		}
	}
}

// fireLaserAtStatic runs one tick of laser fire from attacker at a static
// target (phase 6.2b). The shot is gated by hostility: only a static whose
// owner is hostile to the attacker can be hit; a friendly/neutral or already
// destroyed target drops the engagement. On a kill the static is removed.
func (w *Worker) fireLaserAtStatic(ctx context.Context, s *sectorState, attackerID domain.ShipID, attacker *domain.Ship, ref domain.EntityRef) {
	d, ok := s.destructibles[ref]
	if !ok || d.HP <= 0 || !w.hostile(d.OwnerID, attacker) {
		// Target gone, dead, or not hostile (friendly/neutral invulnerable,
		// 6.2 rules) — stop shooting it.
		attacker.AttackTarget = nil
		s.markDirty(attackerID)
		return
	}
	beam, hit := combat.FireLaserAt(attacker, ref, d.Pos, d.HP, d)
	if !hit {
		return
	}
	s.addLaserEffect(beam)
	s.markDirty(attackerID)
	s.markDestructibleDirty(ref)
	if beam.Killed {
		attacker.AttackTarget = nil
		w.killStatic(ctx, s, d)
	}
}

// killStatic destroys a static: it drops the static from the live combat set
// (WS removal delta), removes it from the rendered/ticked layout so a dead
// tower stops firing and a dead station stops being a dock/trade target, and
// publishes the entity_killed event. RAM-only this phase — the destruction is
// not persisted, so a restart restores the static (see 6.2b deferred notes).
func (w *Worker) killStatic(ctx context.Context, s *sectorState, d *domain.DestructibleStatic) {
	ref := d.Ref
	pos := d.Pos
	s.removeDestructible(ref)
	removeStaticFromLayout(&s.statics, ref)
	// Persist tower destruction (8.5) so cold-start does not resurrect it.
	// Best-effort: a failure leaves the tower deleted in RAM (it reappears on
	// the next restart) but never blocks the kill. Other static kinds remain
	// RAM-only this phase (stations have millions of HP — see 8.5 deferred).
	if ref.Kind == domain.EntityKindLaserTower && w.towerRepo != nil {
		if err := w.towerRepo.Delete(ctx, domain.LaserTowerID(ref.ID)); err != nil {
			w.logger.ErrorContext(ctx, "kill: persist tower destruction", "err", err, "tower", ref.ID)
		}
	}
	// Persist satellite destruction (10.15) so cold-start does not resurrect a
	// killed navigation satellite. Best-effort, same contract as the tower.
	if ref.Kind == domain.EntityKindSatellite && w.satelliteRepo != nil {
		if err := w.satelliteRepo.Delete(ctx, domain.SatelliteID(ref.ID)); err != nil {
			w.logger.ErrorContext(ctx, "kill: persist satellite destruction", "err", err, "satellite", ref.ID)
		}
	}
	// Static kills carry no killer/victim-player attribution (bounties target
	// players, not stations) — Killer/VictimPlayer stay 0.
	w.publishKilled(ctx, s, EntityKilledEvent{Victim: ref, SectorID: s.sectorID, Pos: pos})
}

// publishKilled emits the entity_killed bus event for any victim (ship or
// static). Best-effort: a nil bus or a publish error is logged but never
// blocks the kill. Shared by the ship sweep and killStatic.
func (w *Worker) publishKilled(ctx context.Context, s *sectorState, ev EntityKilledEvent) {
	if w.bus == nil {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		w.logger.ErrorContext(ctx, "kill: marshal entity_killed", "err", err, "victim_kind", ev.Victim.Kind, "victim_id", ev.Victim.ID)
		return
	}
	if err := w.bus.Publish(ctx, EntityKilledTopic, payload); err != nil {
		w.logger.ErrorContext(ctx, "kill: publish entity_killed", "err", err, "victim_kind", ev.Victim.Kind, "victim_id", ev.Victim.ID)
	}
}

// removeStaticFromLayout drops the static identified by ref from the rendered
// SectorStatics so it stops ticking (towers) and stops being a dock/trade
// target. Unknown refs are a no-op.
func removeStaticFromLayout(s *domain.SectorStatics, ref domain.EntityRef) {
	switch ref.Kind {
	case domain.EntityKindStation:
		s.Stations = dropStatic(s.Stations, func(o domain.Station) bool { return int64(o.ID) == ref.ID })
	case domain.EntityKindShipyard:
		s.Shipyards = dropStatic(s.Shipyards, func(o domain.Shipyard) bool { return int64(o.ID) == ref.ID })
	case domain.EntityKindTradeStation:
		s.TradeStations = dropStatic(s.TradeStations, func(o domain.TradeStation) bool { return int64(o.ID) == ref.ID })
	case domain.EntityKindPirbase:
		s.Pirbases = dropStatic(s.Pirbases, func(o domain.Pirbase) bool { return int64(o.ID) == ref.ID })
	case domain.EntityKindLaserTower:
		s.LaserTowers = dropStatic(s.LaserTowers, func(o domain.LaserTower) bool { return int64(o.ID) == ref.ID })
	case domain.EntityKindSatellite:
		s.Satellites = dropStatic(s.Satellites, func(o domain.Satellite) bool { return int64(o.ID) == ref.ID })
	}
}

// dropStatic returns items with the first element matching pred removed,
// preserving order. The slice is rebuilt so the worker's by-value snapshot
// copies see the removal.
func dropStatic[T any](items []T, pred func(T) bool) []T {
	for i := range items {
		if pred(items[i]) {
			return append(items[:i:i], items[i+1:]...)
		}
	}
	return items
}
