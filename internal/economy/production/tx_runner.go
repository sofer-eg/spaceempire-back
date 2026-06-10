package production

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	stationsrepo "spaceempire/back/internal/persistence/stations"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/pkg/database"
)

// RepoTxRunner wires the trade (station_goods) + stations repositories under
// one pgx tx for a single production-cycle write. Used in production wiring;
// tests rely on an in-memory stub instead.
type RepoTxRunner struct {
	tm       *database.TxManager
	trade    *traderepo.Repository
	stations *stationsrepo.Repository
}

// NewRepoTxRunner builds a RepoTxRunner. The pool-bound repositories are
// kept only as templates for WithExecutor — their pool executors are
// never invoked by Do.
func NewRepoTxRunner(tm *database.TxManager, trade *traderepo.Repository, stations *stationsrepo.Repository) *RepoTxRunner {
	return &RepoTxRunner{tm: tm, trade: trade, stations: stations}
}

// Do opens one transaction and hands the bound repos to fn as a Repo.
func (r *RepoTxRunner) Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, &boundRepo{
			trade:    r.trade.WithExecutor(tx),
			stations: r.stations.WithExecutor(tx),
		})
	})
}

// boundRepo adapts the pair of tx-bound persistence repos to the
// production.Repo interface.
type boundRepo struct {
	trade    *traderepo.Repository
	stations *stationsrepo.Repository
}

func (b *boundRepo) ListMarket(ctx context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	return b.trade.ListMarket(ctx, owner)
}

func (b *boundRepo) AdjustStock(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, delta int64) (int64, error) {
	return b.trade.AdjustStock(ctx, owner, gtype, delta)
}

func (b *boundRepo) UpdateProduction(ctx context.Context, id domain.StationID, inProgress bool, nextCycleAt time.Time) error {
	return b.stations.UpdateProduction(ctx, id, inProgress, nextCycleAt)
}
