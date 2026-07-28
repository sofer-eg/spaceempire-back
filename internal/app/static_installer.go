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
