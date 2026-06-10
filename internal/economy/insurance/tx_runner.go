package insurance

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	insurancerepo "spaceempire/back/internal/persistence/insurance"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/database"
)

// PoolRepo composes the insurance and players repositories. The base
// (pool-backed) instance serves as the Service's Reader; withExecutor binds a
// fresh set to a transaction for the TxRepo. Mirrors trade.PoolRepo.
type PoolRepo struct {
	insurance *insurancerepo.Repository
	players   *playersrepo.Repository
}

// NewPoolRepo wires a PoolRepo.
func NewPoolRepo(ins *insurancerepo.Repository, players *playersrepo.Repository) *PoolRepo {
	return &PoolRepo{insurance: ins, players: players}
}

func (r *PoolRepo) withExecutor(exec database.Executor) *PoolRepo {
	return &PoolRepo{
		insurance: r.insurance.WithExecutor(exec),
		players:   r.players.WithExecutor(exec),
	}
}

// --- TxRepo ---

func (r *PoolRepo) ExpireActiveForShip(ctx context.Context, shipID domain.ShipID, now time.Time) error {
	return r.insurance.ExpireActiveForShip(ctx, shipID, now)
}

func (r *PoolRepo) Create(ctx context.Context, p domain.InsurancePolicy) (domain.PolicyID, error) {
	return r.insurance.Create(ctx, p)
}

func (r *PoolRepo) ActiveForShip(ctx context.Context, shipID domain.ShipID, now time.Time) (domain.InsurancePolicy, bool, error) {
	return r.insurance.ActiveForShip(ctx, shipID, now)
}

func (r *PoolRepo) Claim(ctx context.Context, id domain.PolicyID, claimedAt time.Time) error {
	return r.insurance.Claim(ctx, id, claimedAt)
}

func (r *PoolRepo) AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error) {
	return r.players.AdjustCash(ctx, p, delta)
}

// --- Reader (pool) ---

func (r *PoolRepo) ListByPlayer(ctx context.Context, player domain.PlayerID) ([]domain.InsurancePolicy, error) {
	return r.insurance.ListByPlayer(ctx, player)
}

func (r *PoolRepo) ShipOwnership(ctx context.Context, shipID domain.ShipID) (domain.PlayerID, *domain.EntityRef, error) {
	return r.insurance.ShipOwnership(ctx, shipID)
}

// RepoTxRunner is the production TxRunner.
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
