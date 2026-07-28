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

// LaunchDrones charges len(ds) units and creates one row per drone, returning
// the ids in the same order. All-or-nothing by construction: the debit and every
// INSERT share the transaction, so a short magazine or a failing insert rolls the
// whole salvo back. That is what makes a partial spawn — and the remainder refund
// the handler used to do — impossible.
func (o ordnance) LaunchDrones(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, ds []domain.Drone) ([]domain.DroneID, error) {
	ids := make([]domain.DroneID, 0, len(ds))
	err := o.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := cargo.ConsumeIn(ctx, o.cargo.WithExecutor(tx), owner, gtype, int64(len(ds))); err != nil {
			return err
		}
		repo := o.drones.WithExecutor(tx)
		for i := range ds {
			created, err := repo.Create(ctx, ds[i])
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
// A row that is already gone (ErrDroneNotFound) is NOT an error and is NOT
// credited. That is the residue of an ambiguous COMMIT-in-flight deadline: the
// deletes and the credit landed, the worker treated the timeout as a failure and
// kept the drones in RAM. The retry then finds no rows — crediting them again
// would pay twice for the same drones, and failing outright would leave the ship
// permanently unable to recall. Every other delete error rolls the whole
// transaction back: nothing deleted, nothing credited.
//
// cargo.RefundIn (not Service.Refund) because the transaction is ours and the
// DELETEs must ride along in it; Refund semantics — no capacity check — are what
// the pre-TASK-152 handler used, and are right here: the units fitted in this
// hold a moment ago.
func (o ordnance) RecallDrones(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, ids []domain.DroneID) (int, error) {
	var credited int
	err := o.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		repo := o.drones.WithExecutor(tx)
		for _, id := range ids {
			switch err := repo.Delete(ctx, id); {
			case err == nil:
				credited++
			case errors.Is(err, dronesrepo.ErrDroneNotFound):
			default:
				return fmt.Errorf("delete drone: %w", err)
			}
		}
		if credited == 0 {
			return nil
		}
		return cargo.RefundIn(ctx, o.cargo.WithExecutor(tx), owner, gtype, int64(credited))
	})
	if err != nil {
		return 0, err
	}
	return credited, nil
}
