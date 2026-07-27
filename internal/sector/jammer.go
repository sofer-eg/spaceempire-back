package sector

import (
	"context"

	"spaceempire/back/internal/domain"
)

// Hyper-interference generator deploy defaults (TASK-131). These are
// install-time values, not per-tick knobs, so they live as package constants
// rather than in Config. They mirror the jammers table column defaults
// (migration 0061). Shield is the SP value verbatim (ct_drones class 7
// shield=4000); HP scales the satellite's 5000 by the SP hull ratio
// (3560/2420 ≈ 1.47), giving a sturdier object than the navigation satellite.
const (
	jammerHP             = 7500
	jammerShield         = 4000
	jammerShieldRecharge = 20
)

// InstallJammerResult carries the freshly created jammer id back to the HTTP
// handler. On error JammerID is zero and Err is non-nil.
type InstallJammerResult struct {
	Err      error
	JammerID domain.JammerID
}

// InstallJammerCommand deploys a hyper-interference generator (TASK-131) at the
// ship's current position. Ownership is enforced (PlayerID must match the
// ship's owner) and the ship must be in space (not docked). The cargo debit
// (1× goods id 27) happens outside the worker — the HTTP handler consumes it
// before Send and refunds on reply.Err, mirroring install-satellite.
type InstallJammerCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	Reply    chan<- InstallJammerResult
}

func (c InstallJammerCommand) apply(w *Worker, s *sectorState) {
	var res InstallJammerResult
	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked != nil:
		res.Err = ErrShipDocked
	default:
		res.JammerID, res.Err = w.installJammer(s, ship)
	}
	replyInstallJammer(c.Reply, res)
}

// installJammer creates the generator (persisted when a repo is wired, else a
// fallback id is allocated for pure unit tests) and adds it to the sector's
// rendered layout + live combat set. The new generator reaches clients on the
// next tick via the 10.20 L2 big-radar StaticsAdded delta, and starts jamming
// jumps immediately (jammerActive reads statics.Jammers).
func (w *Worker) installJammer(s *sectorState, ship *domain.Ship) (domain.JammerID, error) {
	owner := ship.PlayerID
	jam := domain.Jammer{
		OwnerID:        &owner,
		SectorID:       ship.SectorID,
		Pos:            ship.Pos,
		Race:           int(ship.Race),
		Built:          true,
		HP:             jammerHP,
		Shield:         jammerShield,
		MaxShield:      jammerShield,
		ShieldRecharge: jammerShieldRecharge,
	}
	if w.jammerRepo != nil {
		id, err := w.jammerRepo.Create(context.Background(), jam)
		if err != nil {
			return 0, err
		}
		jam.ID = id
	} else {
		jam.ID = s.allocJammerID()
	}
	s.addJammer(jam)
	return jam.ID, nil
}

func replyInstallJammer(reply chan<- InstallJammerResult, res InstallJammerResult) {
	if reply == nil {
		return
	}
	select {
	case reply <- res:
	default:
	}
}
