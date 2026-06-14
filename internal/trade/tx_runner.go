package trade

import (
	"context"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/pkg/database"
)

// PoolRepo composes the three persistence repositories into the single
// Repo interface Service depends on. It is also used as a "base" template
// for RepoTxRunner: WithExecutor returns a new PoolRepo bound to a
// transaction, and that is what Service receives inside Buy/Sell.
type PoolRepo struct {
	trade   *traderepo.Repository
	players *playersrepo.Repository
	cargo   *cargorepo.Repository
}

// NewPoolRepo wires a PoolRepo. Used both as the read-only Repo Service
// uses for Market(), and as the base passed to NewRepoTxRunner.
func NewPoolRepo(trade *traderepo.Repository, players *playersrepo.Repository, cargo *cargorepo.Repository) *PoolRepo {
	return &PoolRepo{trade: trade, players: players, cargo: cargo}
}

func (r *PoolRepo) withExecutor(exec database.Executor) *PoolRepo {
	return &PoolRepo{
		trade:   r.trade.WithExecutor(exec),
		players: r.players.WithExecutor(exec),
		cargo:   r.cargo.WithExecutor(exec),
	}
}

// LoadShipDock proxies to persistence/trade.
func (r *PoolRepo) LoadShipDock(ctx context.Context, shipID domain.ShipID) (traderepo.ShipDock, error) {
	return r.trade.LoadShipDock(ctx, shipID)
}

// GetMarketEntry proxies to persistence/trade.
func (r *PoolRepo) GetMarketEntry(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID) (traderepo.MarketEntry, error) {
	return r.trade.GetMarketEntry(ctx, owner, gtype)
}

// ListMarket proxies to persistence/trade.
func (r *PoolRepo) ListMarket(ctx context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	return r.trade.ListMarket(ctx, owner)
}

// AdjustStock proxies to persistence/trade.
func (r *PoolRepo) AdjustStock(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, delta int64) (int64, error) {
	return r.trade.AdjustStock(ctx, owner, gtype, delta)
}

// GetCash proxies to persistence/players.
func (r *PoolRepo) GetCash(ctx context.Context, playerID domain.PlayerID) (int64, error) {
	return r.players.GetCash(ctx, playerID)
}

// AdjustCash proxies to persistence/players.
func (r *PoolRepo) AdjustCash(ctx context.Context, playerID domain.PlayerID, delta int64) (int64, error) {
	return r.players.AdjustCash(ctx, playerID, delta)
}

// AddReputation proxies to persistence/players (phase 10.3.13).
func (r *PoolRepo) AddReputation(ctx context.Context, playerID domain.PlayerID, delta playersrepo.Reputation) (playersrepo.Reputation, error) {
	return r.players.AddReputation(ctx, playerID, delta)
}

// GoodsType proxies to persistence/cargo.
func (r *PoolRepo) GoodsType(ctx context.Context, id domain.GoodsTypeID) (domain.GoodsType, error) {
	return r.cargo.GoodsType(ctx, id)
}

// Capacity proxies to persistence/cargo.
func (r *PoolRepo) Capacity(ctx context.Context, owner domain.EntityRef) (float64, error) {
	return r.cargo.Capacity(ctx, owner)
}

// UsedSpace proxies to persistence/cargo.
func (r *PoolRepo) UsedSpace(ctx context.Context, owner domain.EntityRef) (float64, error) {
	return r.cargo.UsedSpace(ctx, owner)
}

// AddCargo proxies to persistence/cargo. Trade always moves goods to/from the
// player's ship hold, which is unowned (goods_owner_id = 0); the per-depositor
// dimension (phase 10.22) only applies to station holds.
func (r *PoolRepo) AddCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	return r.cargo.Add(ctx, owner, gtype, qty, 0)
}

// SubtractCargo proxies to persistence/cargo. See AddCargo on the unowned key.
func (r *PoolRepo) SubtractCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	return r.cargo.Subtract(ctx, owner, gtype, qty, 0)
}

// RepoTxRunner is the production TxRunner. It opens a pgx transaction via
// database.TxManager and binds a fresh PoolRepo to it for the duration
// of fn.
type RepoTxRunner struct {
	tm   *database.TxManager
	base *PoolRepo
}

// NewRepoTxRunner wires a RepoTxRunner. base is used only as a template
// for WithExecutor; its own executor is never invoked by Do.
func NewRepoTxRunner(tm *database.TxManager, base *PoolRepo) *RepoTxRunner {
	return &RepoTxRunner{tm: tm, base: base}
}

// Do implements TxRunner.
func (r *RepoTxRunner) Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, r.base.withExecutor(tx))
	})
}
