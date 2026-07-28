package sector

import (
	"context"
	"time"

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
// ship's owner) and the ship must be in space (not docked). GoodsType is the
// goods id one satellite costs (26); the handler owns that constant, the sector
// package stays free of the goods catalog. The debit happens inside apply, in
// the same transaction as the INSERT (TASK-144), so a lost ack cannot yield a
// free satellite.
type InstallSatelliteCommand struct {
	PlayerID  domain.PlayerID
	ShipID    domain.ShipID
	GoodsType domain.GoodsTypeID
	Reply     chan<- InstallSatelliteResult
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
		res.SatelliteID, res.Err = w.installSatellite(s, ship, c.GoodsType)
	}
	replyInstallSatellite(c.Reply, res)
}

// installSatellite charges the satellite and creates it, then adds it to the
// sector's rendered layout + live combat set. The new satellite reaches clients
// on the next tick via the 10.20 L2 big-radar StaticsAdded delta.
//
// The staticInstaller commits the goods debit and the INSERT as one transaction,
// so nothing is added to RAM unless both succeeded — and it is the only install
// path: with none wired the install is refused (ErrInstallerUnavailable) rather
// than handing out a free satellite.
//
// The command apply path carries no context, so the DB call is bounded by
// cfg.RepoTimeout instead of running under an uninterruptible background
// context: a hung Postgres stalls the tick for at most that long (AC#3). Its
// real cost is charged to the drain's DB budget, which caps ONE drain at
// ~2 × RepoTimeout so a queue of installs cannot park Run without a tick in
// between — it does not shorten the queue's total stall (see
// Worker.dbBudget).
func (w *Worker) installSatellite(s *sectorState, ship *domain.Ship, gtype domain.GoodsTypeID) (domain.SatelliteID, error) {
	if w.staticInstaller == nil {
		return 0, ErrInstallerUnavailable
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.RepoTimeout)
	defer cancel()

	hold := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)}
	started := time.Now()
	id, err := w.staticInstaller.InstallSatellite(ctx, hold, gtype, sat)
	w.spendDBBudget(started)
	if err != nil {
		w.logInstallError(err, "satellite", ship, gtype)
		return 0, err
	}
	sat.ID = id
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
