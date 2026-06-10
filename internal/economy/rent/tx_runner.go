package rent

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
	rentsrepo "spaceempire/back/internal/persistence/rents"
	stationsrepo "spaceempire/back/internal/persistence/stations"
	"spaceempire/back/internal/pkg/database"
)

// PoolRepo composes the rents, players, and stations repositories. The base
// (pool-backed) instance serves as the Service's RentStore + Stations;
// withExecutor binds a fresh set to a transaction for the billing TxRepo.
// Mirrors trade.PoolRepo / bounties.PoolRepo.
type PoolRepo struct {
	rents    *rentsrepo.Repository
	players  *playersrepo.Repository
	stations *stationsrepo.Repository
}

// NewPoolRepo wires a PoolRepo.
func NewPoolRepo(rents *rentsrepo.Repository, players *playersrepo.Repository, stations *stationsrepo.Repository) *PoolRepo {
	return &PoolRepo{rents: rents, players: players, stations: stations}
}

func (r *PoolRepo) withExecutor(exec database.Executor) *PoolRepo {
	return &PoolRepo{
		rents:    r.rents.WithExecutor(exec),
		players:  r.players.WithExecutor(exec),
		stations: r.stations.WithExecutor(exec),
	}
}

// --- TxRepo (billing mutations) ---

func (r *PoolRepo) Due(ctx context.Context, now time.Time, limit int) ([]domain.Rent, error) {
	return r.rents.Due(ctx, now, limit)
}

func (r *PoolRepo) MarkPaid(ctx context.Context, id domain.RentID, paidAt, nextDue time.Time) error {
	return r.rents.MarkPaid(ctx, id, paidAt, nextDue)
}

func (r *PoolRepo) MarkUnpaid(ctx context.Context, id domain.RentID, unpaidPeriods int, nextDue time.Time) error {
	return r.rents.MarkUnpaid(ctx, id, unpaidPeriods, nextDue)
}

func (r *PoolRepo) Delete(ctx context.Context, id domain.RentID) error {
	return r.rents.Delete(ctx, id)
}

func (r *PoolRepo) AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error) {
	return r.players.AdjustCash(ctx, p, delta)
}

func (r *PoolRepo) ClearOwner(ctx context.Context, station domain.EntityRef) error {
	return r.stations.ClearOwner(ctx, station)
}

func (r *PoolRepo) ClaimStation(ctx context.Context, station domain.EntityRef, owner domain.PlayerID) (bool, error) {
	return r.stations.ClaimUnowned(ctx, station, owner)
}

// --- RentStore + Stations (pool reads / idempotent ensure) ---

func (r *PoolRepo) Ensure(ctx context.Context, payer domain.PlayerID, station domain.EntityRef, amountPerPeriod int64, nextDue time.Time) error {
	return r.rents.Ensure(ctx, payer, station, amountPerPeriod, nextDue)
}

func (r *PoolRepo) ListByPayer(ctx context.Context, payer domain.PlayerID) ([]domain.Rent, error) {
	return r.rents.ListByPayer(ctx, payer)
}

func (r *PoolRepo) PlayerOwned(ctx context.Context) ([]stationsrepo.OwnedStatic, error) {
	return r.stations.PlayerOwned(ctx)
}

// RepoTxRunner is the production TxRunner: it opens a pgx transaction and
// binds a fresh PoolRepo to it for the duration of fn.
type RepoTxRunner struct {
	tm   *database.TxManager
	base *PoolRepo
}

// NewRepoTxRunner wires a RepoTxRunner. base is used only as a template for
// withExecutor.
func NewRepoTxRunner(tm *database.TxManager, base *PoolRepo) *RepoTxRunner {
	return &RepoTxRunner{tm: tm, base: base}
}

// Do implements TxRunner.
func (r *RepoTxRunner) Do(ctx context.Context, fn func(ctx context.Context, repo TxRepo) error) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, r.base.withExecutor(tx))
	})
}
