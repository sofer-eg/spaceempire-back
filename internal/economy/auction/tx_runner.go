package auction

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	auctionrepo "spaceempire/back/internal/persistence/auction"
	cargorepo "spaceempire/back/internal/persistence/cargo"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/database"
)

// RepoTxRunner wires auction + players + cargo persistence repositories
// under one pgx transaction. Test code can supply an in-memory TxRunner
// instead.
type RepoTxRunner struct {
	tm      *database.TxManager
	auction *auctionrepo.Repository
	players *playersrepo.Repository
	cargo   *cargorepo.Repository
}

// NewRepoTxRunner builds a RepoTxRunner. The pool-bound repos are used as
// templates for WithExecutor — their pool executors are never invoked here.
func NewRepoTxRunner(tm *database.TxManager, auction *auctionrepo.Repository, players *playersrepo.Repository, cargo *cargorepo.Repository) *RepoTxRunner {
	return &RepoTxRunner{tm: tm, auction: auction, players: players, cargo: cargo}
}

// Do executes fn inside one pgx transaction and hands fn a Repo bound to it.
func (r *RepoTxRunner) Do(ctx context.Context, fn func(ctx context.Context, txRepo Repo) error) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, &boundRepo{
			auction: r.auction.WithExecutor(tx),
			players: r.players.WithExecutor(tx),
			cargo:   r.cargo.WithExecutor(tx),
		})
	})
}

// boundRepo adapts three tx-bound persistence repos to the service Repo
// interface. Each method is a thin pass-through; the value of this type is
// the interface composition, not the logic.
type boundRepo struct {
	auction *auctionrepo.Repository
	players *playersrepo.Repository
	cargo   *cargorepo.Repository
}

func (b *boundRepo) CreateLot(ctx context.Context, p auctionrepo.CreateLotParams) (auctionrepo.Lot, error) {
	return b.auction.CreateLot(ctx, p)
}

func (b *boundRepo) GetLot(ctx context.Context, id int64) (auctionrepo.Lot, error) {
	return b.auction.GetLot(ctx, id)
}

func (b *boundRepo) LockLot(ctx context.Context, id int64) (auctionrepo.Lot, error) {
	return b.auction.LockLot(ctx, id)
}

func (b *boundRepo) ListActive(ctx context.Context) ([]auctionrepo.Lot, error) {
	return b.auction.ListActive(ctx)
}

func (b *boundRepo) ListByParticipant(ctx context.Context, player domain.PlayerID, limit int) ([]auctionrepo.Lot, error) {
	return b.auction.ListByParticipant(ctx, player, limit)
}

func (b *boundRepo) ListDue(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	return b.auction.ListDue(ctx, now, limit)
}

func (b *boundRepo) UpdateBid(ctx context.Context, lotID int64, newPrice int64, bidder domain.PlayerID) error {
	return b.auction.UpdateBid(ctx, lotID, newPrice, bidder)
}

func (b *boundRepo) InsertBid(ctx context.Context, lotID int64, bidder domain.PlayerID, amount int64) error {
	return b.auction.InsertBid(ctx, lotID, bidder, amount)
}

func (b *boundRepo) SetStatus(ctx context.Context, lotID int64, status auctionrepo.Status) error {
	return b.auction.SetStatus(ctx, lotID, status)
}

func (b *boundRepo) FindDeliveryShip(ctx context.Context, buyer domain.PlayerID, requiredSpace float64) (domain.ShipID, bool, error) {
	return b.auction.FindDeliveryShip(ctx, buyer, requiredSpace)
}

func (b *boundRepo) LoadShipDock(ctx context.Context, shipID domain.ShipID) (auctionrepo.ShipDock, error) {
	return b.auction.LoadShipDock(ctx, shipID)
}

func (b *boundRepo) AdjustCash(ctx context.Context, playerID domain.PlayerID, delta int64) (int64, error) {
	return b.players.AdjustCash(ctx, playerID, delta)
}

// AddCargo / SubtractCargo move goods to/from the player's ship hold (lot
// delivery, lot creation), which is unowned (goods_owner_id = 0). The
// per-depositor dimension (phase 10.22) only applies to station holds.
func (b *boundRepo) AddCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	return b.cargo.Add(ctx, owner, gtype, qty, 0)
}

func (b *boundRepo) SubtractCargo(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	return b.cargo.Subtract(ctx, owner, gtype, qty, 0)
}

func (b *boundRepo) GoodsType(ctx context.Context, id domain.GoodsTypeID) (domain.GoodsType, error) {
	return b.cargo.GoodsType(ctx, id)
}
