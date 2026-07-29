package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	jammersrepo "spaceempire/back/internal/persistence/jammers"
	satellitesrepo "spaceempire/back/internal/persistence/satellites"
	"spaceempire/back/internal/pkg/database"
)

// staticInstaller is the sector.StaticInstaller implementation (TASK-144): it
// debits the installing ship's hold and INSERTs the deployed object inside ONE
// transaction, so the two can never disagree.
//
// Before TASK-144 the HTTP handler consumed the goods before Send and refunded
// on the worker's reply. A lost ack (the handler timed out but the worker still
// applied the command) then produced a free object: refund done, object built.
// With both writes in one transaction the handler needs no cargo at all — its
// 504 simply means "outcome unknown", and whichever way the transaction went,
// goods and object agree.
//
// cargo.ConsumeIn (rather than cargo.Service.Consume) is used because Service
// opens its own transaction; here the transaction is ours and the object INSERT
// must ride along in it. Sentinel errors are preserved verbatim so the HTTP
// mapping (ErrInsufficientQuantity → 400, ErrGoodsTypeNotFound → 500) keeps
// working.
type staticInstaller struct {
	tx         *database.TxManager
	cargo      *cargorepo.Repository
	jammers    *jammersrepo.Repository
	satellites *satellitesrepo.Repository
}

func (i staticInstaller) InstallJammer(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, j domain.Jammer) (domain.JammerID, error) {
	var id domain.JammerID
	err := i.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := cargo.ConsumeIn(ctx, i.cargo.WithExecutor(tx), owner, gtype, 1); err != nil {
			return err
		}
		created, err := i.jammers.WithExecutor(tx).Create(ctx, j)
		if err != nil {
			return fmt.Errorf("create jammer: %w", err)
		}
		id = created
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// DismantleJammer is InstallJammer read backwards (TASK-146): it deletes the
// generator's row and credits its goods unit back to the hold in one transaction,
// so a lost ack cannot fold up a ≈1.13M cr object and pay nothing for it.
//
// Unlike the drone recall (TASK-156, which credits what fits), one object is
// indivisible: a hold without room for it is refused with cargo.ErrNoSpace and the
// generator stays deployed. Nothing is lost either way — the player makes room and
// tries again.
func (i staticInstaller) DismantleJammer(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, id domain.JammerID) error {
	return i.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := i.creditOne(ctx, tx, owner, gtype); err != nil {
			return err
		}
		if err := i.jammers.WithExecutor(tx).Delete(ctx, id); err != nil {
			return fmt.Errorf("delete jammer: %w", err)
		}
		return nil
	})
}

// DismantleSatellite mirrors DismantleJammer for the navigation satellite.
func (i staticInstaller) DismantleSatellite(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, id domain.SatelliteID) error {
	return i.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := i.creditOne(ctx, tx, owner, gtype); err != nil {
			return err
		}
		if err := i.satellites.WithExecutor(tx).Delete(ctx, id); err != nil {
			return fmt.Errorf("delete satellite: %w", err)
		}
		return nil
	})
}

// creditOne puts one unit of gtype back into the hold inside the caller's
// transaction, refusing when it does not fit. cargo.FitsIn sizes the credit and
// cargo.RefundIn performs it: RefundIn skips the capacity check by design (its
// usual caller has already destroyed what it is paying for), so the check has to
// be made here — a dismantle that overfilled the hold would be the TASK-156
// exploit under a different name.
func (i staticInstaller) creditOne(ctx context.Context, tx pgx.Tx, owner domain.EntityRef, gtype domain.GoodsTypeID) error {
	repo := i.cargo.WithExecutor(tx)
	fits, err := cargo.FitsIn(ctx, repo, owner, gtype, 1)
	if err != nil {
		return err
	}
	if fits < 1 {
		return cargo.ErrNoSpace
	}
	return cargo.RefundIn(ctx, repo, owner, gtype, 1)
}

func (i staticInstaller) InstallSatellite(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, s domain.Satellite) (domain.SatelliteID, error) {
	var id domain.SatelliteID
	err := i.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := cargo.ConsumeIn(ctx, i.cargo.WithExecutor(tx), owner, gtype, 1); err != nil {
			return err
		}
		created, err := i.satellites.WithExecutor(tx).Create(ctx, s)
		if err != nil {
			return fmt.Errorf("create satellite: %w", err)
		}
		id = created
		return nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}
