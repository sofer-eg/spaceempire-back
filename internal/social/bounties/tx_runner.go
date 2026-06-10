package bounties

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	bountyrepo "spaceempire/back/internal/persistence/bounties"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/database"
	clansrepo "spaceempire/back/internal/social/clans"
)

// PoolRepo composes the bounty, players, and clans repositories. The base
// instance (pool-backed) serves as the Service's Reader; withExecutor binds a
// fresh set to a transaction, and that is what Service receives as a TxRepo
// inside Do. Mirrors trade.PoolRepo.
type PoolRepo struct {
	bounty  *bountyrepo.Repository
	players *playersrepo.Repository
	clans   *clansrepo.Repository
}

// NewPoolRepo wires a PoolRepo.
func NewPoolRepo(bounty *bountyrepo.Repository, players *playersrepo.Repository, clans *clansrepo.Repository) *PoolRepo {
	return &PoolRepo{bounty: bounty, players: players, clans: clans}
}

func (r *PoolRepo) withExecutor(exec database.Executor) *PoolRepo {
	return &PoolRepo{
		bounty:  r.bounty.WithExecutor(exec),
		players: r.players.WithExecutor(exec),
		clans:   r.clans.WithExecutor(exec),
	}
}

// --- TxRepo (bounty mutations + wallets) ---

func (r *PoolRepo) CreateBounty(ctx context.Context, b domain.Bounty) (domain.BountyID, error) {
	return r.bounty.Create(ctx, b)
}

func (r *PoolRepo) ActiveForTargets(ctx context.Context, now time.Time, targets []domain.EntityRef) ([]domain.Bounty, error) {
	return r.bounty.ActiveForTargets(ctx, now, targets)
}

func (r *PoolRepo) DueExpired(ctx context.Context, now time.Time, limit int) ([]domain.Bounty, error) {
	return r.bounty.DueExpired(ctx, now, limit)
}

func (r *PoolRepo) MarkPaid(ctx context.Context, id domain.BountyID, paidTo domain.PlayerID, at time.Time) error {
	return r.bounty.MarkPaid(ctx, id, paidTo, at)
}

func (r *PoolRepo) MarkExpired(ctx context.Context, id domain.BountyID) error {
	return r.bounty.MarkExpired(ctx, id)
}

func (r *PoolRepo) AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error) {
	return r.players.AdjustCash(ctx, p, delta)
}

func (r *PoolRepo) AdjustTreasury(ctx context.Context, c domain.ClanID, delta int64) (int64, error) {
	return r.clans.AdjustTreasury(ctx, c, delta)
}

// --- Reader (pool reads) ---

func (r *PoolRepo) ListActive(ctx context.Context, now time.Time, limit int) ([]bountyrepo.View, error) {
	return r.bounty.ListActive(ctx, now, limit)
}

func (r *PoolRepo) HistoryForTarget(ctx context.Context, target domain.EntityRef, limit int) ([]bountyrepo.View, error) {
	return r.bounty.HistoryForTarget(ctx, target, limit)
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

// ClansAdapter bridges the Service's Clans dependency to clans.Repository
// (pool-backed reads). Membership/leader rarely change mid-operation, so
// these reads are not part of the escrow transaction.
type ClansAdapter struct {
	repo *clansrepo.Repository
}

// NewClansAdapter wires a ClansAdapter.
func NewClansAdapter(repo *clansrepo.Repository) *ClansAdapter {
	return &ClansAdapter{repo: repo}
}

// ClanOf returns the player's clan, ok=false when they belong to none.
func (a *ClansAdapter) ClanOf(ctx context.Context, player domain.PlayerID) (domain.ClanID, bool, error) {
	m, err := a.repo.GetMembership(ctx, player)
	if errors.Is(err, clansrepo.ErrNotMember) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return m.ClanID, true, nil
}

// LeaderOf returns the leader of a clan.
func (a *ClansAdapter) LeaderOf(ctx context.Context, clan domain.ClanID) (domain.PlayerID, error) {
	c, err := a.repo.GetClan(ctx, clan)
	if err != nil {
		return 0, err
	}
	return c.LeaderID, nil
}
