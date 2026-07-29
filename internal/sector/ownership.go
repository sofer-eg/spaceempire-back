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
//     the write goes FIRST and the RAM ship is only re-owned once it lands
//     (TASK-148). player_id and race are Save-only columns — BatchUpdate writes
//     FinalTarget/HP/Shield and nothing else — so a failed save has no later
//     write to recover it: the pre-TASK-148 order left the captor flying a ship
//     that reverts to its old owner at the next cold start, and the old crew
//     ejected into spacesuits over a capture that never persisted. Refusing
//     instead costs the captor the attempt (the energy is spent either way, as
//     on a failed roll) and leaves RAM and DB agreed;
//   - does NOT touch cargo: a ship's hold is keyed by the ship's EntityRef with
//     goods_owner_id 0, and the ship id is stable, so the cargo follows the ship
//     to the new owner automatically (FR-B3 is a no-op in this model);
//   - does NOT make the captured ship the new owner's active ship (FR-B4) — that
//     stays a deliberate fleet switch (TASK-100.1).
func (w *Worker) changeShipOwner(ctx context.Context, s *sectorState, ship *domain.Ship, newOwner domain.PlayerID) error {
	oldOwner := ship.PlayerID
	passengers := clonePlayerIDs(ship.PassengerPlayers)
	pos := ship.Pos

	// The transferred ship as it must exist AFTER the capture, built as a copy so
	// the live ship is untouched until the row is written.
	next := *ship
	next.PlayerID = newOwner
	next.Race = 0
	next.AttackTarget = nil
	next.Target = nil
	next.Vel = domain.Vec2{}
	next.LastStep = domain.Vec2{}
	next.PassengerPlayers = nil

	if err := w.saveShip(next); err != nil {
		w.logOwnerTransferError(err, s, ship, newOwner)
		return err
	}

	*ship = next
	s.markDirty(ship.ID)
	w.publishShipCaptured(ctx, s, ShipCapturedEvent{
		ShipID:     ship.ID,
		SectorID:   s.sectorID,
		Pos:        pos,
		OldOwner:   oldOwner,
		Passengers: passengers,
	})
	return nil
}

// logOwnerTransferError records a capture that could not be persisted
// (TASK-148). A clean failure rolled the UPDATE back, so nothing moved and the
// caller reports a failed capture: WARN.
//
// A deadline is in doubt in the direction that costs the captor rather than the
// victim: the UPDATE may have COMMITted after the deadline fired, in which case
// the row says the captor owns the ship while RAM — which this function has left
// untouched — still flies it for the old owner, and no crew was ejected. The
// discrepancy is invisible until the next cold start, when the ship changes hands
// on its own. The reverse choice (re-own in RAM anyway) is worse: it ejects the
// old crew into spacesuits on the strength of a write that may have rolled back.
// ERROR with both player ids so the row can be checked by hand.
func (w *Worker) logOwnerTransferError(err error, s *sectorState, ship *domain.Ship, newOwner domain.PlayerID) {
	if dbDeadline(err) {
		w.logger.Error("ship ownership transfer in doubt: the row may name the new owner while the sector still flies it for the old one",
			"err", err, "ship", int64(ship.ID), "old_owner", int64(ship.PlayerID),
			"new_owner", int64(newOwner), "sector", int64(s.sectorID),
			"repo_timeout", w.cfg.RepoTimeout)
		return
	}
	w.logger.Warn("ship ownership transfer failed",
		"err", err, "ship", int64(ship.ID), "old_owner", int64(ship.PlayerID),
		"new_owner", int64(newOwner), "sector", int64(s.sectorID))
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
	if err := w.publishEffect(ShipCapturedTopic, payload); err != nil {
		// The ship is already re-owned; this event is what puts the old crew into
		// spacesuits, and nothing retries it.
		w.logger.ErrorContext(ctx, "capture: ship_captured not delivered, the old crew was not ejected",
			"err", err, "ship", int64(ev.ShipID), "old_owner", int64(ev.OldOwner))
	}
}
