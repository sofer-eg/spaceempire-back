package app

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	containersrepo "spaceempire/back/internal/persistence/containers"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/trade"
)

// hackRaider is the market half of a station hack as ONE transaction
// (TASK-160): the stock deduction, the hold deposit (up_hack level 2) and — when
// the loot did not reach the hold — the loot container that carries it all
// commit together, or none of them do.
//
// Before TASK-160 the container was spawned by the worker AFTER Rob returned, as
// a separate transaction. Two ways to lose the goods followed from that: a
// successful rob whose SpawnContainer then failed, and (since TASK-148) a
// RepoTimeout firing while the rob's COMMIT was already in flight — pgx reports
// DeadlineExceeded, Postgres commits, and the stock is gone with nothing created
// to hold it. Same defect class TASK-144/147/152 closed for the installs, the
// launches and the drone recall, and the same fix: one commit.
//
// trade.RobIn (rather than trade.Service.Rob) is used because Service opens its
// own transaction; here the transaction is ours and the container INSERT must ride
// along in it. Sentinel errors are preserved verbatim so stationRobber keeps
// mapping trade.ErrTooLittleGoods to the sector sentinel.
type hackRaider struct {
	tx    *database.TxManager
	trade *trade.PoolRepo
}

// Rob raids the station and returns the market outcome plus the loot container it
// created, if any. A container is created exactly when goods were robbed and the
// hold did not take them — the level-1 case, and the level-2 full-hold fallback.
func (h hackRaider) Rob(ctx context.Context, station, hackerShip domain.EntityRef,
	robFrac, damageFrac, minFrac float64, depositToHold bool, loot sector.LootDrop,
) (trade.RobOutcome, *domain.Container, error) {
	var (
		out       trade.RobOutcome
		container *domain.Container
	)
	err := h.tx.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// Reset per attempt: TxManager.Do runs the closure once today, but state
		// carried over from a retry would report a container the rollback undid.
		out, container = trade.RobOutcome{}, nil

		var err error
		out, err = trade.RobIn(ctx, h.trade.WithExecutor(tx), station, hackerShip,
			robFrac, damageFrac, minFrac, depositToHold)
		if err != nil {
			return err
		}
		if out.Robbed == 0 || out.Delivered {
			return nil
		}
		created, err := containersrepo.SpawnIn(ctx, tx, loot.SectorID, domain.ContainerDrop{
			Pos:       loot.Pos,
			ExpiresAt: loot.ExpiresAt,
			GoodsType: out.GoodsType,
			Quantity:  out.Robbed,
		})
		if err != nil {
			return fmt.Errorf("spawn loot container: %w", err)
		}
		container = &created
		return nil
	})
	if err != nil {
		return trade.RobOutcome{}, nil, err
	}
	return out, container, nil
}
