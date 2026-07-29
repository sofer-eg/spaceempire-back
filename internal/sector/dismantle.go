package sector

import (
	"context"
	"errors"

	"spaceempire/back/internal/domain"
)

var (
	// ErrDeployedNotFound is reported by DismantleStaticCommand when the target
	// is not a deployed object of this sector (wrong id, wrong sector, or it was
	// destroyed between the click and the tick). HTTP maps it to 404.
	ErrDeployedNotFound = errors.New("sector: deployed object not found")
	// ErrDeployedOutOfRange is reported when the ship is farther than
	// PickupRange from the object it tries to dismantle. HTTP maps it to 422.
	ErrDeployedOutOfRange = errors.New("sector: deployed object out of range")
	// ErrNotDismantlable is reported when the target kind is not a
	// player-deployed object (only jammers and navigation satellites are).
	// HTTP maps it to 400.
	ErrNotDismantlable = errors.New("sector: object cannot be dismantled")
)

// DismantleStaticCommand takes a deployed hyper-interference generator or
// navigation satellite back into the ship's hold (TASK-146). Before it, a
// deployment was irreversible: there was no take-down command, and
// fireLaserAtStatic refuses to shoot an object of your own — so a generator
// parked next to your own station was a permanent self-debuff (it jams its owner
// too) on a ≈1.13M cr object.
//
// Gates: the ship must be the player's own and in space, the object must be in
// this sector, owned by the same player, and within PickupRange — the same reach
// a container pickup uses, because this is the same physical action of taking an
// object into the hold. GoodsType is the goods id one unit of that object is
// worth (27 generator / 26 satellite); the handler owns the catalog, the sector
// package stays free of it, and it must match Target.Kind — a mismatch would pay
// the wrong good.
type DismantleStaticCommand struct {
	PlayerID  domain.PlayerID
	ShipID    domain.ShipID
	Target    domain.EntityRef
	GoodsType domain.GoodsTypeID
	Reply     chan<- CmdResult
}

func (c DismantleStaticCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case ship.Docked != nil:
		res.Err = ErrShipDocked
	case !isDismantlableKind(c.Target.Kind):
		res.Err = ErrNotDismantlable
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}

	target, ok := resolveDeployed(s, c.Target)
	switch {
	case !ok:
		res.Err = ErrDeployedNotFound
	case target.owner == nil || *target.owner != c.PlayerID:
		// Someone else's object is not yours to fold up — it has to be shot.
		res.Err = ErrForbidden
	case ship.Pos.Sub(target.pos).Length() > w.cfg.PickupRange:
		res.Err = ErrDeployedOutOfRange
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}

	res.Err = w.dismantleStatic(s, ship, c.Target, c.GoodsType)
	replyOnce(c.Reply, res)
}

// dismantleStatic credits the goods unit and deletes the row in ONE transaction,
// then drops the object from RAM — the install path read backwards, with the same
// nil-installer doctrine (TASK-144): without a dismantler the command is refused
// rather than removing an object nobody can pay the player back for.
//
// Commit first, mutate RAM after: a rolled-back transaction leaves the object
// deployed on both sides. The residue of an ambiguous COMMIT-in-flight deadline is
// the same one logInstallError describes, mirrored — the row is gone and the unit
// paid, while RAM still renders the object until the next cold start, which finds
// no row. It stops jamming only then, so it is an ERROR, not a warning.
//
// The refund is capacity-checked (cargo.FitsIn in the dismantler): a hold with no
// room refuses the whole dismantle rather than overfilling it. The object stays
// where it is, so nothing is lost — the player makes room and tries again.
func (w *Worker) dismantleStatic(s *sectorState, ship *domain.Ship, ref domain.EntityRef, gtype domain.GoodsTypeID) error {
	if w.staticInstaller == nil {
		return ErrInstallerUnavailable
	}

	hold := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)}
	err := w.dbCall(context.Background(), func(ctx context.Context) error {
		switch ref.Kind {
		case domain.EntityKindJammer:
			return w.staticInstaller.DismantleJammer(ctx, hold, gtype, domain.JammerID(ref.ID))
		case domain.EntityKindSatellite:
			return w.staticInstaller.DismantleSatellite(ctx, hold, gtype, domain.SatelliteID(ref.ID))
		default:
			// isDismantlableKind gates apply, so this is unreachable through the
			// command; keeping it explicit means a new deployable kind fails loudly
			// here instead of silently doing nothing.
			return ErrNotDismantlable
		}
	})
	if err != nil {
		w.logDismantleError(err, ship, ref, gtype)
		return err
	}

	// Off the radar, out of the combat set, and — for a generator — the jump
	// suppression it projected is gone with it (jammerActive reads statics.Jammers).
	s.removeDestructible(ref)
	removeStaticFromLayout(&s.statics, ref)
	return nil
}

// logDismantleError records a failed take-down (TASK-146), mirroring
// logInstallError. A deadline is the one outcome the atomicity invariant cannot
// cover: if cfg.RepoTimeout fires while COMMIT is in flight, pgx tears the
// connection down and reports DeadlineExceeded while Postgres commits anyway. The
// unit is then in the hold and the row is gone, but the object is still in RAM —
// jamming, shootable, and drawn to clients until a restart's LoadAll drops it.
// Every other error means the transaction rolled back and nothing happened.
func (w *Worker) logDismantleError(err error, ship *domain.Ship, ref domain.EntityRef, gtype domain.GoodsTypeID) {
	if dbDeadline(err) {
		w.logger.Error("dismantle outcome in doubt: object may be paid for and deleted while still live in RAM",
			"err", err, "kind", ref.Kind, "object", ref.ID,
			"ship", int64(ship.ID), "player", int64(ship.PlayerID),
			"goods_type", int64(gtype), "sector", int64(ship.SectorID),
			"repo_timeout", w.cfg.RepoTimeout)
		return
	}
	w.logger.Error("dismantle failed",
		"err", err, "kind", ref.Kind, "object", ref.ID,
		"ship", int64(ship.ID), "sector", int64(ship.SectorID))
}

// isDismantlableKind reports whether ref.Kind names a player-deployed object that
// can be folded back into a cargo unit. Only the two install commands create
// such objects; stations, towers and gates are world fixtures, not equipment.
func isDismantlableKind(k domain.EntityKind) bool {
	return k == domain.EntityKindJammer || k == domain.EntityKindSatellite
}

// deployedTarget is the normalised view of a deployed object the command needs
// for its ownership and range gates.
type deployedTarget struct {
	pos   domain.Vec2
	owner *domain.PlayerID
}

// resolveDeployed finds a deployed object by ref in the sector's layout. ok is
// false when no object of that kind carries that id here.
func resolveDeployed(s *sectorState, ref domain.EntityRef) (deployedTarget, bool) {
	switch ref.Kind {
	case domain.EntityKindJammer:
		for i := range s.statics.Jammers {
			if j := s.statics.Jammers[i]; int64(j.ID) == ref.ID {
				return deployedTarget{pos: j.Pos, owner: j.OwnerID}, true
			}
		}
	case domain.EntityKindSatellite:
		for i := range s.statics.Satellites {
			if sat := s.statics.Satellites[i]; int64(sat.ID) == ref.ID {
				return deployedTarget{pos: sat.Pos, owner: sat.OwnerID}, true
			}
		}
	}
	return deployedTarget{}, false
}
