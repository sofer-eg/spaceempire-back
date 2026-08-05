package sector

import (
	"context"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
)

// The four helpers below are the worker's side of the Ordnance contract
// (TASK-147; recallDrones added by TASK-152). They share one shape, mirroring
// installJammer/installSatellite:
//
//   - a nil Ordnance refuses the launch (ErrOrdnanceUnavailable) rather than
//     firing for free — the ordnance is what charges the player, and for the
//     recall what pays them back;
//   - the command apply path carries no context, so the DB call goes through
//     Worker.dbCall, which bounds it by cfg.RepoTimeout instead of letting it run
//     under an uninterruptible background context (which is what the pre-TASK-147
//     torpedo/drone INSERTs did): a hung Postgres stalls the tick for at most that
//     long. These four helpers spelled the deadline out inline until TASK-148 made
//     dbCall the single place it is attached;
//   - the call's real cost is charged to the drain's DB budget, which caps ONE
//     drain at ~2 × RepoTimeout so a queue of launches cannot park Run without a
//     tick in between (see Worker.dbBudget; it does not shorten the queue's total
//     stall);
//   - sentinel errors come back verbatim so the HTTP mapping
//     (cargo.ErrInsufficientQuantity → 400, cargo.ErrGoodsTypeNotFound → 500)
//     keeps working.

// spendMissile charges one missile from the ship's hold. A missile has no DB row
// of its own (RAM-only, reconstructable), so the "transaction" is the debit
// alone — but it still has to happen inside the tick, or a lost ack refunds
// ammunition for a missile that flew.
func (w *Worker) spendMissile(ship *domain.Ship, gtype domain.GoodsTypeID) error {
	if w.ordnance == nil {
		return ErrOrdnanceUnavailable
	}
	err := w.dbCall(context.Background(), func(ctx context.Context) error {
		return w.ordnance.SpendMissile(ctx, shipHold(ship), gtype)
	})
	if err != nil {
		w.logOrdnanceError(err, "missile", ship, gtype, 1)
		return err
	}
	return nil
}

// launchTorpedo charges one torpedo and creates its row, returning the DB id the
// live torpedo is keyed by.
func (w *Worker) launchTorpedo(ship *domain.Ship, gtype domain.GoodsTypeID, t domain.Torpedo) (domain.TorpedoID, error) {
	if w.ordnance == nil {
		return 0, ErrOrdnanceUnavailable
	}
	var id domain.TorpedoID
	err := w.dbCall(context.Background(), func(ctx context.Context) error {
		var err error
		id, err = w.ordnance.LaunchTorpedo(ctx, shipHold(ship), gtype, t)
		return err
	})
	if err != nil {
		w.logOrdnanceError(err, "torpedo", ship, gtype, 1)
		return 0, err
	}
	return id, nil
}

// errOrdnanceIDCount reports that Ordnance broke its own contract: it must never
// return MORE ids than the drones it was handed.
var errOrdnanceIDCount = errors.New("sector: ordnance returned more drone ids than drones")

// launchDrones charges the salvo and creates its rows, returning one id per drone
// ACTUALLY launched, in order — a prefix of ds, so ids[i] belongs to ds[i].
//
// Since TASK-176 the ordnance sizes the salvo by what the hold carries, so
// len(ids) < len(ds) is a normal answer ("the hold could pay for this many"): at
// space 290 a drone-capable hull carries single digits of them, and refusing the
// whole salvo instead — as the all-or-nothing debit of TASK-147 did — turned the
// common case into a 400. Zero launched for an empty magazine is reported as
// cargo.ErrInsufficientQuantity by the ordnance itself, so the player still gets
// that 400 when there is genuinely nothing aboard.
//
// Still all-or-nothing about the drones it DOES launch: on error nothing was
// charged and nothing created.
func (w *Worker) launchDrones(ship *domain.Ship, gtype domain.GoodsTypeID, ds []domain.Drone) ([]domain.DroneID, error) {
	if w.ordnance == nil {
		return nil, ErrOrdnanceUnavailable
	}
	var ids []domain.DroneID
	err := w.dbCall(context.Background(), func(ctx context.Context) error {
		var err error
		ids, err = w.ordnance.LaunchDrones(ctx, shipHold(ship), gtype, ds)
		return err
	})
	if err != nil {
		w.logOrdnanceError(err, "drone", ship, gtype, len(ds))
		return nil, err
	}
	if len(ids) > len(ds) {
		// A broken contract, not a game situation: an implementation that batches
		// the INSERTs or retries inside the transaction could return more ids than
		// drones, and the caller pairs ids[i] with ds[i] — so this would index past
		// the salvo and panic the tick goroutine (there is no recover() in this
		// package, so it takes every sector this worker owns with it). Refuse instead
		// of half-applying: the transaction is already committed and we cannot tell
		// which row belongs to which drone, so this is logged at ERROR with the counts
		// for hand reconciliation, like the deadline case in logOrdnanceError.
		w.logger.Error("launch outcome in doubt: ordnance broke its id contract, drones charged but not in RAM",
			"err", errOrdnanceIDCount, "requested", len(ds), "returned", len(ids),
			"ship", int64(ship.ID), "player", int64(ship.PlayerID),
			"sector", int64(ship.SectorID), "goods_type", int64(gtype))
		return nil, fmt.Errorf("%w: requested %d, got %d", errOrdnanceIDCount, len(ds), len(ids))
	}
	return ids, nil
}

// recallDrones deletes the drone rows the hold can pay for and credits one cargo
// unit per row actually deleted, in ONE transaction (TASK-152/156). It returns the
// outcome: which drones are settled (the caller clears exactly those from RAM) and
// how many units were credited, which is what the player is told was recalled.
//
// Same shape as the launch helpers, and the same nil-Ordnance doctrine read
// backwards: without an ordnance the recall is refused rather than deleting the
// drones with nobody to pay the player back.
//
// Two independent reasons the outcome can fall short of len(ids):
//
//   - No room. The hold is sized inside the transaction and only what fits is
//     recalled (TASK-156); the rest keeps flying until the player frees space.
//     Nothing is stranded permanently — a recall never refuses outright.
//   - A row already gone. The DB, not RAM, is the ledger here: such a drone
//     deletes as a no-op inside the transaction and is worth nothing, because its
//     unit was paid out once already (see logRecallError for how that residue
//     arises). Crediting it again would mint consumables out of a stale RAM entry;
//     it is still cleared from RAM so the ghost leaves the radar.
//
// Ordering, and why it is this way: the caller collects the ids WITHOUT touching
// RAM, and only removes them once this call has returned successfully. The tick
// goroutine is the sole writer of sectorState and it is parked here for the
// duration, so nothing can change s.drones underneath — which makes
// "commit first, then mutate RAM" free of races and, unlike the previous
// delete-then-write order, unable to leave the drones deleted in RAM after a
// rolled-back transaction.
func (w *Worker) recallDrones(ship *domain.Ship, gtype domain.GoodsTypeID, ids []domain.DroneID) (RecallOutcome, error) {
	if w.ordnance == nil {
		return RecallOutcome{}, ErrOrdnanceUnavailable
	}
	if len(ids) == 0 {
		// Nothing to delete and nothing to credit: no transaction, no round trip.
		// Deliberately AFTER the nil gate, so a misconfigured worker answers the
		// same way whether or not the ship has drones out — an admin poking the
		// endpoint after a deploy is exactly who must not get a cheerful 200.
		return RecallOutcome{}, nil
	}
	var out RecallOutcome
	err := w.dbCall(context.Background(), func(ctx context.Context) error {
		var err error
		out, err = w.ordnance.RecallDrones(ctx, shipHold(ship), gtype, ids)
		return err
	})
	if err != nil {
		w.logRecallError(err, ship, gtype, len(ids))
		return RecallOutcome{}, err
	}
	if out.Credited < len(out.Removed) {
		// The one event that confirms (or refutes) a past "recall outcome in doubt"
		// ERROR: those rows were deleted and paid for by an earlier attempt whose
		// COMMIT landed after the deadline fired. Worth a line either way — if rows
		// ever start vanishing for a real reason (an out-of-band DELETE, a cascade
		// from the owning ship), players quietly lose consumables and this is the
		// only place that would show it. Gated on Removed, not on len(ids): a drone
		// left flying for want of hold space is expected, not a lost unit.
		w.logger.Warn("recall credited fewer units than drones removed: rows already gone",
			"removed", len(out.Removed), "credited", out.Credited, "ship", int64(ship.ID),
			"player", int64(ship.PlayerID), "sector", int64(ship.SectorID),
			"goods_type", int64(gtype))
	}
	return out, nil
}

// shipHold is the cargo owner a launch debits: the launching ship itself.
func shipHold(ship *domain.Ship) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(ship.ID)}
}

// logOrdnanceError records a failed ammunition charge (TASK-147). A
// deadline/cancellation is logged at ERROR with everything needed to reconcile it
// by hand, because it is the one outcome the atomicity invariant cannot cover: if
// cfg.RepoTimeout fires while COMMIT is already in flight, pgx tears the
// connection down and reports DeadlineExceeded while Postgres commits anyway. The
// ammunition is then gone and — for torpedoes/drones — the row exists, but the
// projectile was never added to RAM: it flies for nobody until a restart's
// LoadAll picks it up (a missile, being RAM-only, is simply lost). Every other
// error means the transaction rolled back and nothing happened, so it is logged
// at WARN. Mirrors logInstallError.
func (w *Worker) logOrdnanceError(err error, kind string, ship *domain.Ship, gtype domain.GoodsTypeID, qty int) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		w.logger.Error("launch outcome in doubt: ammunition may be charged for a projectile missing from RAM",
			"err", err, "projectile", kind, "qty", qty, "ship", int64(ship.ID),
			"player", int64(ship.PlayerID), "sector", int64(ship.SectorID),
			"goods_type", int64(gtype), "repo_timeout", w.cfg.RepoTimeout)
		return
	}
	w.logger.Warn("launch failed",
		"err", err, "projectile", kind, "qty", qty, "ship", int64(ship.ID),
		"player", int64(ship.PlayerID), "sector", int64(ship.SectorID),
		"goods_type", int64(gtype))
}

// logRecallError is logOrdnanceError's mirror for the recall (TASK-152). The
// reasoning inverts with the operation: here the deadline case means the rows may
// be DELETED and the units CREDITED while the drones are still in RAM, because
// the caller only clears RAM on a confirmed success. That residue is bounded and
// self-correcting — the drones fly on until their TTL or the next restart, and a
// retried recall clears them while crediting nothing (their rows are gone, so
// there is nothing left to pay for). Self-correcting in the LEDGER, not on the
// battlefield: until that TTL the player holds both the credited units and N
// still-firing ghosts, so he can field up to 2N drones for the price of N. That
// is why this is an ERROR and not an accounting footnote. The opposite choice —
// clearing RAM on an ambiguous outcome — is still worse: if the transaction
// actually rolled back, the
// drones vanish from the sector with their rows intact and come back from the
// dead at the next cold start, having been paid for once. ERROR carries the
// counts for hand reconciliation; every other error rolled the transaction back
// and left both sides untouched, so it is a WARN.
func (w *Worker) logRecallError(err error, ship *domain.Ship, gtype domain.GoodsTypeID, qty int) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		w.logger.Error("recall outcome in doubt: drones may be deleted and credited while still flying in RAM",
			"err", err, "qty", qty, "ship", int64(ship.ID),
			"player", int64(ship.PlayerID), "sector", int64(ship.SectorID),
			"goods_type", int64(gtype), "repo_timeout", w.cfg.RepoTimeout)
		return
	}
	w.logger.Warn("recall failed",
		"err", err, "qty", qty, "ship", int64(ship.ID),
		"player", int64(ship.PlayerID), "sector", int64(ship.SectorID),
		"goods_type", int64(gtype))
}
