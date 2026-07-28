package sector

import (
	"context"
	"time"

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
// ship's owner) and the ship must be in space (not docked). GoodsType is the
// goods id one generator costs (27); the handler owns that constant, the sector
// package stays free of the goods catalog. The debit happens inside apply, in
// the same transaction as the INSERT (TASK-144), so a lost ack cannot yield a
// free generator.
type InstallJammerCommand struct {
	PlayerID  domain.PlayerID
	ShipID    domain.ShipID
	GoodsType domain.GoodsTypeID
	Reply     chan<- InstallJammerResult
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
		res.JammerID, res.Err = w.installJammer(s, ship, c.GoodsType)
	}
	replyInstallJammer(c.Reply, res)
}

// installJammer charges the generator and creates it, then adds it to the
// sector's rendered layout + live combat set. The new generator reaches clients
// on the next tick via the 10.20 L2 big-radar StaticsAdded delta, and starts
// jamming jumps immediately (jammerActive reads statics.Jammers).
//
// The staticInstaller commits the goods debit and the INSERT as one transaction,
// so nothing is added to RAM unless both succeeded — and it is the only install
// path: with none wired the install is refused (ErrInstallerUnavailable) rather
// than handing out a free generator.
//
// The command apply path carries no context, so the DB call is bounded by
// cfg.RepoTimeout instead of running under an uninterruptible background
// context: a hung Postgres stalls the tick for at most that long (AC#3). Its
// real cost is charged to the drain's install budget so a queue full of installs
// cannot chain those stalls (see Worker.installBudget).
func (w *Worker) installJammer(s *sectorState, ship *domain.Ship, gtype domain.GoodsTypeID) (domain.JammerID, error) {
	if w.staticInstaller == nil {
		return 0, ErrInstallerUnavailable
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.RepoTimeout)
	defer cancel()

	hold := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)}
	started := time.Now()
	id, err := w.staticInstaller.InstallJammer(ctx, hold, gtype, jam)
	// Wall clock on purpose: the budget bounds real time parked on DB I/O, which
	// the injected (possibly fake) clock does not model.
	w.spendInstallBudget(time.Since(started))
	if err != nil {
		w.logInstallError(err, "jammer", ship, gtype)
		return 0, err
	}
	jam.ID = id
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
