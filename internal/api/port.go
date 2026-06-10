package api

import (
	"context"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// SectorRouter is the minimal contract the HTTP/WS layer needs from the
// sector-side of the world. It is defined here (consumer side) per ISP —
// *sector.Pool implements it implicitly.
type SectorRouter interface {
	Send(sectorID domain.SectorID, cmd sector.Command) error
	Snapshot(sectorID domain.SectorID) sector.Snapshot
	Subscribe(ctx context.Context, sectorID domain.SectorID, playerID domain.PlayerID) (*sector.Subscription, func(), error)
	// LookupShipSector reports which sector currently hosts the ship. The
	// autopilot endpoint uses it to send SetCourseCommand to the right
	// worker after a handoff has migrated the ship out of its origin
	// sector. Returns (0, false) when no worker knows the ship.
	LookupShipSector(shipID domain.ShipID) (domain.SectorID, bool)
	// LookupPrimaryShipByPlayer returns the player's lowest-id ship and the
	// sector it currently lives in. WS subscribe uses it to lock on to the
	// sector the player's ship is in, not the configured default sector.
	// Returns (0, 0, false) when the player has no ships in RAM.
	LookupPrimaryShipByPlayer(playerID domain.PlayerID) (domain.ShipID, domain.SectorID, bool)
}

// PathRouter is the slice of world.PathRouter the autopilot endpoint
// needs: validating that the requested course is reachable without
// running BFS twice. Declared here per ISP — *world.PathRouter implements
// it implicitly.
type PathRouter interface {
	Hops(from, to domain.SectorID) (int, bool)
}

// ActiveShipReader resolves a player's explicitly-selected active ship
// (phase 10.14a). The WS subscribe path uses it so the connection locks on
// to the sector of the ship the player currently controls — a spacesuit or a
// switched-to fleet member — instead of the lowest-id ship. ok=false means
// active_ship_id is NULL → caller falls back to the min-id rule. Declared here
// (consumer side) per ISP; *players.Repository implements it implicitly.
type ActiveShipReader interface {
	ActiveShip(ctx context.Context, playerID domain.PlayerID) (domain.ShipID, bool, error)
	// PassengerHost returns the host ship a player rides as a passenger (10.23);
	// ok=false when not a passenger. WS subscribe prefers it so a reload while
	// riding re-binds to the host's sector (not the player's idle own ship).
	PassengerHost(ctx context.Context, playerID domain.PlayerID) (domain.ShipID, bool, error)
}

// ActiveShipWriter persists the player's active-ship selection (phase 10.14a).
// The activate endpoint calls it after validating ownership. Declared here per
// ISP; *players.Repository implements it implicitly.
type ActiveShipWriter interface {
	SetActiveShip(ctx context.Context, playerID domain.PlayerID, shipID domain.ShipID) error
}

// FleetReader lists every ship a player owns across all sectors (phase 10.14a
// fleet panel). Declared here per ISP; *sector.Pool implements it implicitly.
type FleetReader interface {
	ShipsByPlayer(playerID domain.PlayerID) []domain.Ship
}
