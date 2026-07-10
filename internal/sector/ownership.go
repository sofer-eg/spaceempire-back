package sector

import (
	"context"
	"encoding/json"

	"spaceempire/back/internal/domain"
)

// ShipCapturedTopic is the bus topic changeShipOwner publishes to so the app
// ejects the old crew into spacesuits (TASK-100.3.9.2). It is deliberately
// distinct from EntityKilledTopic: a captured ship SURVIVES (re-owned), so the
// kill side-effects (bounty payout, quest kill-credit, ship respawn) must NOT
// fire — only the crew eject reuses the death path.
const ShipCapturedTopic = "ship.captured"

// ShipCapturedEvent tells the app who to eject when a live ship changes owner.
// OldOwner is the pre-capture pilot (the app skips it when it is the NPC player
// or 0, and when they are no longer actually flying this ship); Passengers are
// the riders. SectorID/Pos place the spawned spacesuits.
type ShipCapturedEvent struct {
	ShipID     domain.ShipID     `json:"shipId"`
	SectorID   domain.SectorID   `json:"sectorId"`
	Pos        domain.Vec2       `json:"pos"`
	OldOwner   domain.PlayerID   `json:"oldOwner"`
	Passengers []domain.PlayerID `json:"passengers"`
}

// changeShipOwner transfers a LIVE ship to newOwner under the one-writer
// invariant (TASK-100.3.9.2, FR-B1..B4) — the foundation the capture command
// (TASK-100.3.9.4) builds on. It:
//
//   - re-owns and neutralises the ship: PlayerID = newOwner, Race = 0 (so it is
//     no longer hostile to anyone via the race matrix), and resets its combat +
//     motion state (AttackTarget, Target, Vel, LastStep);
//   - ejects the old crew: clears PassengerPlayers and publishes ShipCapturedEvent
//     so the app spacesuits the old pilot (if a real player) and every passenger
//     — reusing the death-eject path, but without the kill side-effects;
//   - persists immediately via the Save path, which now writes race as well as
//     player_id, so the transfer survives cold-start (FR-B1 / NFR-002);
//   - does NOT touch cargo: a ship's hold is keyed by the ship's EntityRef with
//     goods_owner_id 0, and the ship id is stable, so the cargo follows the ship
//     to the new owner automatically (FR-B3 is a no-op in this model);
//   - does NOT make the captured ship the new owner's active ship (FR-B4) — that
//     stays a deliberate fleet switch (TASK-100.1).
func (w *Worker) changeShipOwner(ctx context.Context, s *sectorState, ship *domain.Ship, newOwner domain.PlayerID) {
	oldOwner := ship.PlayerID
	passengers := clonePlayerIDs(ship.PassengerPlayers)
	pos := ship.Pos

	ship.PlayerID = newOwner
	ship.Race = 0
	ship.AttackTarget = nil
	ship.Target = nil
	ship.Vel = domain.Vec2{}
	ship.LastStep = domain.Vec2{}
	ship.PassengerPlayers = nil

	s.markDirty(ship.ID)
	w.immediateSave(ship)
	w.publishShipCaptured(ctx, s, ShipCapturedEvent{
		ShipID:     ship.ID,
		SectorID:   s.sectorID,
		Pos:        pos,
		OldOwner:   oldOwner,
		Passengers: passengers,
	})
}

// publishShipCaptured emits the crew-eject event on the bus. Best-effort: a nil
// bus (pure unit tests) or a publish error is skipped/logged, never blocking the
// tick. Mirrors publishKilled / publishPoliceScan.
func (w *Worker) publishShipCaptured(ctx context.Context, s *sectorState, ev ShipCapturedEvent) {
	if w.bus == nil {
		return
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		w.logger.ErrorContext(ctx, "capture: marshal event", "err", err, "ship", int64(ev.ShipID))
		return
	}
	if err := w.bus.Publish(ctx, ShipCapturedTopic, payload); err != nil {
		w.logger.ErrorContext(ctx, "capture: publish event", "err", err, "ship", int64(ev.ShipID))
	}
}
