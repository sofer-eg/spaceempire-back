package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// suitSpawner is the slice of the player spawner the respawner needs (ISP).
// *shipSpawner satisfies it.
type suitSpawner interface {
	SpawnSpacesuit(ctx context.Context, player domain.PlayerID, sectorID domain.SectorID, pos domain.Vec2, docked *domain.EntityRef) (domain.ShipID, error)
	SpawnFor(ctx context.Context, player domain.PlayerID) error
}

// handoffPublisher publishes the per-player handoff that moves a respawned
// player's WS to the home sector. *bus.InMemory satisfies it.
type handoffPublisher interface {
	Publish(ctx context.Context, topic string, payload []byte) error
}

// passengerEjector clears/repoints a player's active+passenger pointers when the
// ship they ride is destroyed (phase 10.23) or captured (TASK-100.3.9.2).
// ActiveShip is read on capture to eject only the pilot still flying the taken
// ship. *players.Repository satisfies it.
type passengerEjector interface {
	SetActiveShip(ctx context.Context, player domain.PlayerID, shipID domain.ShipID) error
	SetPassengerHost(ctx context.Context, player domain.PlayerID, hostID domain.ShipID) error
	ActiveShip(ctx context.Context, player domain.PlayerID) (domain.ShipID, bool, error)
}

// spacesuitRespawner reacts to a player ship death (phase 10.1): a normal ship
// drops the pilot into a weak spacesuit at the death spot; a destroyed
// spacesuit respawns a fresh ship at the home shipyard and moves the player's
// WS there. NPC and non-ship victims are ignored.
type spacesuitRespawner struct {
	spawner suitSpawner
	bus     handoffPublisher
	players passengerEjector
	npc     domain.PlayerID
	home    domain.SectorID
	logger  *slog.Logger
}

// OnKill handles one entity_killed event. Best-effort: failures are logged.
func (r spacesuitRespawner) OnKill(ctx context.Context, ev sector.EntityKilledEvent) {
	// Eject any passengers riding the dead ship into spacesuits at the death
	// spot (phase 10.23). Runs before the victim guard below because the host
	// may be an NPC ship (VictimPlayer == npc) yet still carry player riders.
	for _, pid := range ev.VictimPassengers {
		if pid == 0 {
			continue
		}
		r.ejectPassenger(ctx, pid, ev.SectorID, ev.Pos)
	}

	if ev.Victim.Kind != domain.EntityKindShip || ev.VictimPlayer == 0 || ev.VictimPlayer == r.npc {
		return
	}
	if !ev.VictimIsSpacesuit {
		suitID, err := r.spawner.SpawnSpacesuit(ctx, ev.VictimPlayer, ev.SectorID, ev.Pos, nil)
		if err != nil {
			r.logger.ErrorContext(ctx, "spacesuit: spawn", "err", err, "player", int64(ev.VictimPlayer))
			return
		}
		// Point the player at the suit, exactly as ejectPassenger does
		// (TASK-194). The dead hull's row is gone and the FK on
		// players.active_ship_id (ON DELETE SET NULL, migration 0048) has just
		// nulled the pointer, so without this every «which ship does this
		// player fly» resolution falls through to the lowest-id rule and lands
		// on whatever hull they left parked elsewhere — including in another
		// sector.
		//
		// No handoff is published here: the suit is in the sector the player's
		// WS is already on, and wsPushLoop skips same-sector handoffs anyway
		// (internal/api/ws.go:600). ejectPassenger publishes one because it is
		// also reused by the capture path.
		if err := r.players.SetActiveShip(ctx, ev.VictimPlayer, suitID); err != nil {
			r.logger.ErrorContext(ctx, "spacesuit: set active", "err", err, "player", int64(ev.VictimPlayer))
		}
		return
	}
	// Spacesuit destroyed → "Вы очнулись на верфи": fresh ship at the home
	// shipyard, and move the player's WS to it.
	if err := r.spawner.SpawnFor(ctx, ev.VictimPlayer); err != nil {
		r.logger.ErrorContext(ctx, "spacesuit: respawn", "err", err, "player", int64(ev.VictimPlayer))
		return
	}
	payload, err := json.Marshal(sector.PlayerHandoffEvent{
		PlayerID: ev.VictimPlayer, SourceSector: ev.SectorID, TargetSector: r.home,
	})
	if err != nil {
		r.logger.ErrorContext(ctx, "spacesuit: marshal handoff", "err", err)
		return
	}
	if err := r.bus.Publish(ctx, sector.PlayerHandoffTopic(ev.VictimPlayer), payload); err != nil {
		r.logger.ErrorContext(ctx, "spacesuit: publish handoff", "err", err)
	}
}

// OnShipCaptured ejects the crew of a captured (not destroyed) ship into
// spacesuits at the ship's spot (TASK-100.3.9.2, FR-B2 / AC-6), reusing the
// death-eject path but without any kill side-effect. Every passenger is ejected;
// the old owner is ejected only when they are a real player (not the NPC / 0)
// AND still actually flying this exact ship (active_ship_id == captured ship) —
// an owner who already abandoned it is left flying whatever they are on now.
func (r spacesuitRespawner) OnShipCaptured(ctx context.Context, ev sector.ShipCapturedEvent) {
	for _, pid := range ev.Passengers {
		if pid == 0 {
			continue
		}
		r.ejectPassenger(ctx, pid, ev.SectorID, ev.Pos)
	}

	if ev.OldOwner == 0 || ev.OldOwner == r.npc {
		return
	}
	active, has, err := r.players.ActiveShip(ctx, ev.OldOwner)
	if err != nil {
		r.logger.ErrorContext(ctx, "capture: active ship", "err", err, "player", int64(ev.OldOwner))
		return
	}
	if has && active == ev.ShipID {
		r.ejectPassenger(ctx, ev.OldOwner, ev.SectorID, ev.Pos)
	}
}

// ejectPassenger drops a rider of a destroyed host into a spacesuit at the death
// spot (phase 10.23): spawn the suit, clear the passenger link, make the suit
// active, and move the rider's WS to the death sector.
func (r spacesuitRespawner) ejectPassenger(ctx context.Context, player domain.PlayerID, sectorID domain.SectorID, pos domain.Vec2) {
	suitID, err := r.spawner.SpawnSpacesuit(ctx, player, sectorID, pos, nil)
	if err != nil {
		r.logger.ErrorContext(ctx, "eject passenger: spawn suit", "err", err, "player", int64(player))
		return
	}
	if err := r.players.SetPassengerHost(ctx, player, 0); err != nil {
		r.logger.ErrorContext(ctx, "eject passenger: clear host", "err", err, "player", int64(player))
	}
	if err := r.players.SetActiveShip(ctx, player, suitID); err != nil {
		r.logger.ErrorContext(ctx, "eject passenger: set active", "err", err, "player", int64(player))
	}
	payload, err := json.Marshal(sector.PlayerHandoffEvent{
		PlayerID: player, ShipID: suitID, SourceSector: sectorID, TargetSector: sectorID,
	})
	if err != nil {
		r.logger.ErrorContext(ctx, "eject passenger: marshal handoff", "err", err)
		return
	}
	if err := r.bus.Publish(ctx, sector.PlayerHandoffTopic(player), payload); err != nil {
		r.logger.ErrorContext(ctx, "eject passenger: publish handoff", "err", err)
	}
}
