package quests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// OfferRepository persists procedural quest offers (player_quest_offers,
// TASK-89): generated instances awaiting accept, each with a TTL. See
// docs/specs/quest.md and the TASK-89 SRS.
type OfferRepository struct {
	exec database.Executor
}

// NewOffers wires an OfferRepository to the given executor.
func NewOffers(exec database.Executor) *OfferRepository {
	return &OfferRepository{exec: exec}
}

// WithExecutor returns an OfferRepository bound to a different executor (a tx),
// so accept can delete an offer and write player_quests in one transaction.
func (r *OfferRepository) WithExecutor(exec database.Executor) *OfferRepository {
	return &OfferRepository{exec: exec}
}

const offerCols = `id, player_id, template_id, definition, source, created_at, expires_at`

const insertOfferSQL = `
INSERT INTO player_quest_offers (player_id, template_id, definition, source, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`

// InsertOffer stores a generated offer and returns its assigned id.
func (r *OfferRepository) InsertOffer(ctx context.Context, o domain.QuestOffer) (int64, error) {
	var id int64
	err := r.exec.QueryRow(ctx, insertOfferSQL,
		int64(o.Player), o.TemplateID, o.Definition, o.Source, o.CreatedAt, o.ExpiresAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert quest offer: %w", err)
	}
	return id, nil
}

const listActiveOffersSQL = `SELECT ` + offerCols + `
FROM player_quest_offers WHERE player_id = $1 AND expires_at > $2 ORDER BY created_at`

// ListActiveOffersByPlayer returns the player's un-expired offers oldest-first.
func (r *OfferRepository) ListActiveOffersByPlayer(ctx context.Context, player domain.PlayerID, now time.Time) ([]domain.QuestOffer, error) {
	rows, err := r.exec.Query(ctx, listActiveOffersSQL, int64(player), now)
	if err != nil {
		return nil, fmt.Errorf("list active offers: %w", err)
	}
	defer rows.Close()
	var out []domain.QuestOffer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan offer: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate offers: %w", err)
	}
	return out, nil
}

const getOfferSQL = `SELECT ` + offerCols + `
FROM player_quest_offers WHERE id = $1 AND player_id = $2`

// GetOffer returns an offer by id, scoped to its owner (ownership check).
// ok=false when the offer is absent or belongs to another player.
func (r *OfferRepository) GetOffer(ctx context.Context, id int64, player domain.PlayerID) (domain.QuestOffer, bool, error) {
	o, err := scanOffer(r.exec.QueryRow(ctx, getOfferSQL, id, int64(player)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.QuestOffer{}, false, nil
	}
	if err != nil {
		return domain.QuestOffer{}, false, fmt.Errorf("get offer: %w", err)
	}
	return o, true, nil
}

const deleteOfferSQL = `DELETE FROM player_quest_offers WHERE id = $1 AND player_id = $2`

// DeleteOffer removes an offer owned by the player, returning whether a row
// was deleted (false when absent or owned by another player).
func (r *OfferRepository) DeleteOffer(ctx context.Context, id int64, player domain.PlayerID) (bool, error) {
	tag, err := r.exec.Exec(ctx, deleteOfferSQL, id, int64(player))
	if err != nil {
		return false, fmt.Errorf("delete offer: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

const countActiveOffersSQL = `SELECT COUNT(*) FROM player_quest_offers WHERE player_id = $1 AND expires_at > $2`

// CountActiveOffers counts the player's un-expired offers (pacer live-limit).
func (r *OfferRepository) CountActiveOffers(ctx context.Context, player domain.PlayerID, now time.Time) (int, error) {
	var n int
	if err := r.exec.QueryRow(ctx, countActiveOffersSQL, int64(player), now).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active offers: %w", err)
	}
	return n, nil
}

const deleteExpiredOffersSQL = `DELETE FROM player_quest_offers WHERE expires_at <= $1`

// DeleteExpiredOffers purges every offer whose TTL has elapsed, returning how
// many rows were removed (for the Closer's periodic sweep).
func (r *OfferRepository) DeleteExpiredOffers(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.exec.Exec(ctx, deleteExpiredOffersSQL, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired offers: %w", err)
	}
	return tag.RowsAffected(), nil
}

func scanOffer(row pgx.Row) (domain.QuestOffer, error) {
	var (
		o        domain.QuestOffer
		playerID int64
	)
	if err := row.Scan(&o.ID, &playerID, &o.TemplateID, &o.Definition, &o.Source, &o.CreatedAt, &o.ExpiresAt); err != nil {
		return domain.QuestOffer{}, err
	}
	o.Player = domain.PlayerID(playerID)
	return o, nil
}
