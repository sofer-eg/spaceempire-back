package quest

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
	questsrepo "spaceempire/back/internal/persistence/quests"
	"spaceempire/back/internal/pkg/database"
)

// PoolRepo composes the quests, players and offers repositories. The base
// (pool-backed) instance is the Service's Store + OfferStore; withExecutor binds
// a fresh set to a transaction for the step-advance / accept TxRepo. Mirrors
// rent.PoolRepo.
type PoolRepo struct {
	quests  *questsrepo.Repository
	players *playersrepo.Repository
	offers  *questsrepo.OfferRepository
}

// NewPoolRepo wires a PoolRepo.
func NewPoolRepo(quests *questsrepo.Repository, players *playersrepo.Repository, offers *questsrepo.OfferRepository) *PoolRepo {
	return &PoolRepo{quests: quests, players: players, offers: offers}
}

func (r *PoolRepo) withExecutor(exec database.Executor) *PoolRepo {
	return &PoolRepo{
		quests:  r.quests.WithExecutor(exec),
		players: r.players.WithExecutor(exec),
		offers:  r.offers.WithExecutor(exec),
	}
}

// --- Store (pool) ---

func (r *PoolRepo) Get(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error) {
	return r.quests.Get(ctx, player, questID)
}

func (r *PoolRepo) Ensure(ctx context.Context, player domain.PlayerID, questID string, deadlineAt *time.Time) error {
	return r.quests.Ensure(ctx, player, questID, deadlineAt)
}

func (r *PoolRepo) EnsureWithDefinition(ctx context.Context, player domain.PlayerID, questID string, deadlineAt *time.Time, definition []byte) error {
	return r.quests.EnsureWithDefinition(ctx, player, questID, deadlineAt, definition)
}

// --- OfferStore (pool) ---

func (r *PoolRepo) GetOffer(ctx context.Context, id int64, player domain.PlayerID) (domain.QuestOffer, bool, error) {
	return r.offers.GetOffer(ctx, id, player)
}

func (r *PoolRepo) ListActiveOffersByPlayer(ctx context.Context, player domain.PlayerID, now time.Time) ([]domain.QuestOffer, error) {
	return r.offers.ListActiveOffersByPlayer(ctx, player, now)
}

// DeleteOffer consumes an offer in the accept tx (part of TxRepo).
func (r *PoolRepo) DeleteOffer(ctx context.Context, id int64, player domain.PlayerID) (bool, error) {
	return r.offers.DeleteOffer(ctx, id, player)
}

func (r *PoolRepo) Abandon(ctx context.Context, player domain.PlayerID, questID string) error {
	return r.quests.Abandon(ctx, player, questID)
}

func (r *PoolRepo) ListActive(ctx context.Context, limit int) ([]domain.QuestProgress, error) {
	return r.quests.ListActive(ctx, limit)
}

func (r *PoolRepo) ListActiveByPlayer(ctx context.Context, player domain.PlayerID) ([]domain.QuestProgress, error) {
	return r.quests.ListActiveByPlayer(ctx, player)
}

func (r *PoolRepo) PlayerState(ctx context.Context, player domain.PlayerID) (Snapshot, error) {
	docked, cargo, cash, sector, dockedKind, dockedID, err := r.quests.PlayerState(ctx, player)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{
		Docked:        docked,
		CargoUnits:    cargo,
		Cash:          cash,
		CurrentSector: domain.SectorID(sector),
	}
	if docked {
		snap.DockedTarget = domain.EntityRef{Kind: domain.EntityKind(dockedKind), ID: dockedID}
	}
	return snap, nil
}

// --- TxRepo ---

func (r *PoolRepo) Lock(ctx context.Context, player domain.PlayerID, questID string) (domain.QuestProgress, bool, error) {
	return r.quests.Lock(ctx, player, questID)
}

func (r *PoolRepo) SetStep(ctx context.Context, player domain.PlayerID, questID string, step int) error {
	return r.quests.SetStep(ctx, player, questID, step)
}

func (r *PoolRepo) SetState(ctx context.Context, player domain.PlayerID, questID string, state []byte) error {
	return r.quests.SetState(ctx, player, questID, state)
}

func (r *PoolRepo) Complete(ctx context.Context, player domain.PlayerID, questID string, finalStep int, at time.Time) error {
	return r.quests.Complete(ctx, player, questID, finalStep, at)
}

func (r *PoolRepo) Fail(ctx context.Context, player domain.PlayerID, questID string, at time.Time) error {
	return r.quests.Fail(ctx, player, questID, at)
}

func (r *PoolRepo) AdjustCash(ctx context.Context, p domain.PlayerID, delta int64) (int64, error) {
	return r.players.AdjustCash(ctx, p, delta)
}

// RepoTxRunner is the production TxRunner.
type RepoTxRunner struct {
	tm   *database.TxManager
	base *PoolRepo
}

// NewRepoTxRunner wires a RepoTxRunner. base is the withExecutor template.
func NewRepoTxRunner(tm *database.TxManager, base *PoolRepo) *RepoTxRunner {
	return &RepoTxRunner{tm: tm, base: base}
}

// Do implements TxRunner.
func (r *RepoTxRunner) Do(ctx context.Context, fn func(ctx context.Context, repo TxRepo) error) error {
	return r.tm.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, r.base.withExecutor(tx))
	})
}
