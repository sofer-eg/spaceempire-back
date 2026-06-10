package sector

import (
	"context"

	"spaceempire/back/internal/domain"
)

// Navigation-satellite deploy defaults (phase 10.15). These are install-time
// values, not per-tick knobs, so they live as package constants rather than in
// Config. They mirror the satellites table column defaults (migration 0047).
const (
	satelliteHP             = 5000
	satelliteShield         = 2000
	satelliteShieldRecharge = 20
)

// InstallSatelliteResult carries the freshly created satellite id back to the
// HTTP handler. On error SatelliteID is zero and Err is non-nil.
type InstallSatelliteResult struct {
	Err         error
	SatelliteID domain.SatelliteID
}

// InstallSatelliteCommand deploys a navigation satellite (phase 10.15) at the
// ship's current position. Ownership is enforced (PlayerID must match the
// ship's owner) and the ship must be in space (not docked). The cargo debit
// (1× goods id 26) happens outside the worker — the HTTP handler consumes it
// before Send and refunds on reply.Err, mirroring launch-missile.
type InstallSatelliteCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Reply    chan<- InstallSatelliteResult
}

func (c InstallSatelliteCommand) apply(w *Worker, s *sectorState) {
	var res InstallSatelliteResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked != nil:
		res.Err = ErrShipDocked
	default:
		res.SatelliteID, res.Err = w.installSatellite(s, ship)
	}
	replyInstallSatellite(c.Reply, res)
}

// installSatellite creates the satellite (persisted when a repo is wired, else
// a fallback id is allocated for pure unit tests) and adds it to the sector's
// rendered layout + live combat set. The new satellite reaches clients on the
// next tick via the 10.20 L2 big-radar StaticsAdded delta.
func (w *Worker) installSatellite(s *sectorState, ship *domain.Ship) (domain.SatelliteID, error) {
	owner := ship.PlayerID
	sat := domain.Satellite{
		OwnerID:        &owner,
		SectorID:       ship.SectorID,
		Pos:            ship.Pos,
		Race:           int(ship.Race),
		Built:          true,
		HP:             satelliteHP,
		Shield:         satelliteShield,
		MaxShield:      satelliteShield,
		ShieldRecharge: satelliteShieldRecharge,
	}
	if w.satelliteRepo != nil {
		id, err := w.satelliteRepo.Create(context.Background(), sat)
		if err != nil {
			return 0, err
		}
		sat.ID = id
	} else {
		sat.ID = s.allocSatelliteID()
	}
	s.addSatellite(sat)
	return sat.ID, nil
}

func replyInstallSatellite(reply chan<- InstallSatelliteResult, res InstallSatelliteResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}
