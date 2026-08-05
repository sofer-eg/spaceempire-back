package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	dronesrepo "spaceempire/back/internal/persistence/drones"
	torpedosrepo "spaceempire/back/internal/persistence/torpedos"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/sector"
)

// ordnance is the sector.Ordnance implementation (TASK-147): it debits the
// launching ship's magazine and INSERTs the projectile rows inside ONE
// transaction, so the two can never disagree. Same shape and same reasoning as
// staticInstaller (TASK-144), for the launch commands instead of the installs.
//
// Before TASK-147 the HTTP handlers consumed the ammunition before Send and
// refunded on timeout. AckTimeout is only TickInterval + 1s, so a delayed tick
// made the handler refund and answer 504 while the command was still queued and
// applied normally a moment later: ammunition returned, shot fired, repeatable.
// With both writes in one transaction the handlers need no cargo at all — their
// 504 simply means "outcome unknown", and whichever way the transaction went,
// ammunition and projectile agree.
//
// cargo.ConsumeIn (rather than cargo.Service.Consume) is used because Service
// opens its own transaction; here the transaction is ours and the projectile
// INSERTs must ride along in it. Sentinel errors are preserved verbatim so the
// HTTP mapping (ErrInsufficientQuantity → 400, ErrGoodsTypeNotFound → 500) keeps
// working.
type ordnance struct {
	tx       *database.TxManager
	cargo    *cargorepo.Repository
	drones   *dronesrepo.Repository
	torpedos *torpedosrepo.Repository
}

// SpendMissile charges one missile. A missile has no row of its own (RAM-only,
// reconstructable — see missiles.md §3), so the transaction holds the debit
// alone; it still has to be the worker that runs it, or a lost ack refunds
// ammunition for a missile that flew.
func (o ordnance) SpendMissile(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID) error {
	return o.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return cargo.ConsumeIn(ctx, o.cargo.WithExecutor(tx), owner, gtype, 1)
	})
}

func (o ordnance) LaunchTorpedo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, t domain.Torpedo) (domain.TorpedoID, error) {
	var id domain.TorpedoID
	err := o.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := cargo.ConsumeIn(ctx, o.cargo.WithExecutor(tx), owner, gtype, 1); err != nil {
			return err
		}
		created, err := o.torpedos.WithExecutor(tx).Create(ctx, t)
		if err != nil {
			return fmt.Errorf("create torpedo: %w", err)
		}
		id = created
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// LaunchDrones launches as many of ds as the hold can pay for, charging one unit
// each and creating one row per launched drone, and returns their ids in order — a
// prefix of ds, so ids[i] belongs to ds[i].
//
// The salvo is SIZED inside the transaction (TASK-176), by cargo.AvailableIn: the
// mirror of what RecallDrones does with cargo.FitsIn. Until then the whole clamped
// salvo was one all-or-nothing debit, so a hold shorter than the salvo failed the
// launch outright with ErrInsufficientQuantity. At the drone's real space of 290
// (TASK-167) a drone-capable hull carries single digits of them, which made "fewer
// aboard than the salvo" the ordinary case rather than an edge — and the SPA entry
// point that did not pre-clamp the count (the canvas menu) answered 400 for a launch
// the player could plainly see they could make.
//
// Sizing and debiting in one transaction is what keeps the count honest: a quantity
// read outside it could be spent by another command before the debit lands, which
// is the same reason the recall sizes its credit inside its own.
//
// An EMPTY hold is still ErrInsufficientQuantity, not an empty success: the handler
// owes the player a 400 "not enough drones in cargo" when there is nothing to
// launch. What it does launch stays all-or-nothing — the debit and every INSERT
// share the transaction, so a failing insert rolls the whole salvo back.
func (o ordnance) LaunchDrones(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, ds []domain.Drone) ([]domain.DroneID, error) {
	ids := make([]domain.DroneID, 0, len(ds))
	err := o.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// Reset per attempt: TxManager.Do runs the closure once today, but an
		// accumulator that lives outside it would silently double on the first
		// retry anyone adds.
		ids = ids[:0]
		cargoRepo := o.cargo.WithExecutor(tx)

		available, err := cargo.AvailableIn(ctx, cargoRepo, owner, gtype)
		if err != nil {
			return err
		}
		launch := int64(len(ds))
		if available < launch {
			launch = available
		}
		if launch == 0 {
			return cargo.ErrInsufficientQuantity
		}

		if err := cargo.ConsumeIn(ctx, cargoRepo, owner, gtype, launch); err != nil {
			return err
		}
		repo := o.drones.WithExecutor(tx)
		for _, d := range ds[:launch] {
			created, err := repo.Create(ctx, d)
			if err != nil {
				return fmt.Errorf("create drone: %w", err)
			}
			ids = append(ids, created)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// RecallDrones is LaunchDrones read backwards (TASK-152): it deletes the rows
// and credits one unit per row actually deleted, in one transaction. Before it,
// the worker deleted the drones and the HTTP handler credited the cargo after the
// ack — so a lost ack (AckTimeout is only TickInterval + 1s) deleted the drones
// and paid nothing back.
//
// It recalls only as many drones as the hold can take (TASK-156). The launch
// side's "the units fitted here a moment ago" premise does not hold in this
// direction: a drone's whole TTL can pass between launch and recall, and the ship
// can dock, sell, buy and fill the hold in between. Crediting unconditionally let
// a player launch drones, refill the freed space and recall — repeatably carrying
// more than cargobay. Refusing the whole recall instead would strand the drones
// until their TTL killed them, so the transaction credits what fits and leaves the
// rest flying: the player frees space and recalls again (AC#1, AC#3).
//
// A row that is already gone (ErrDroneNotFound) is NOT an error and is NOT
// credited, but IS reported as removed. That is the residue of an ambiguous
// COMMIT-in-flight deadline: the deletes and the credit landed, the worker treated
// the timeout as a failure and kept the drones in RAM. The retry then finds no
// rows — crediting them again would pay twice for the same drones, and leaving
// them in RAM would keep a ghost on the radar forever. Every other delete error
// rolls the whole transaction back: nothing deleted, nothing credited.
//
// cargo.RefundIn (not cargo.Add) because the transaction is ours and the DELETEs
// must ride along in it. Its skipped capacity check is now sized by cargo.FitsIn
// above it instead of being relied upon.
func (o ordnance) RecallDrones(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, ids []domain.DroneID) (sector.RecallOutcome, error) {
	var out sector.RecallOutcome
	err := o.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		out = sector.RecallOutcome{} // see LaunchDrones: an accumulator outside the closure
		cargoRepo := o.cargo.WithExecutor(tx)

		fits, err := cargo.FitsIn(ctx, cargoRepo, owner, gtype, int64(len(ids)))
		if err != nil {
			return err
		}
		if fits == 0 {
			return nil
		}

		repo := o.drones.WithExecutor(tx)
		for _, id := range ids[:fits] {
			switch err := repo.Delete(ctx, id); {
			case err == nil:
				out.Removed = append(out.Removed, id)
				out.Credited++
			case errors.Is(err, dronesrepo.ErrDroneNotFound):
				out.Removed = append(out.Removed, id)
			default:
				return fmt.Errorf("delete drone: %w", err)
			}
		}
		if out.Credited == 0 {
			return nil
		}
		return cargo.RefundIn(ctx, cargoRepo, owner, gtype, int64(out.Credited))
	})
	if err != nil {
		return sector.RecallOutcome{}, err
	}
	return out, nil
}
