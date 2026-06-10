// Package insurance persists the insurance_policies table (phase 6.5): ship
// destruction cover. The economy/insurance Service owns the buy/payout logic;
// this repo does CRUD + the locked payout lookup. See
// back/docs/specs/insurance.md.
package insurance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

const pgUniqueViolation = "23505"

// ErrAlreadyInsured is returned by Create when the ship already has an active
// policy (the partial-unique index).
var ErrAlreadyInsured = errors.New("insurance: ship already has an active policy")

// ErrShipNotFound is returned by ShipOwnership when the ship row is missing.
var ErrShipNotFound = errors.New("insurance: ship not found")

// Repository talks to insurance_policies (and a read slice of ships) via an
// Executor.
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor (a tx).
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const createSQL = `
INSERT INTO insurance_policies (ship_id, player_id, premium_paid, coverage, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING id`

// Create inserts an active policy. A unique-violation (an active policy
// already covers the ship) maps to ErrAlreadyInsured.
func (r *Repository) Create(ctx context.Context, p domain.InsurancePolicy) (domain.PolicyID, error) {
	var id int64
	err := r.exec.QueryRow(ctx, createSQL,
		int64(p.ShipID), int64(p.PlayerID), p.PremiumPaid, p.Coverage, p.ExpiresAt).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return 0, ErrAlreadyInsured
		}
		return 0, fmt.Errorf("insert policy: %w", err)
	}
	return domain.PolicyID(id), nil
}

const expireActiveForShipSQL = `
UPDATE insurance_policies
SET status = 'expired'
WHERE ship_id = $1 AND status = 'active' AND expires_at <= $2`

// ExpireActiveForShip flips a time-lapsed active policy on the ship to
// expired. Called inside Buy so a fresh policy can be inserted without
// tripping the active-per-ship unique index (lazy expiry — no background
// sweep). A genuinely-active (unexpired) policy is left alone, so Create then
// reports ErrAlreadyInsured.
func (r *Repository) ExpireActiveForShip(ctx context.Context, shipID domain.ShipID, now time.Time) error {
	if _, err := r.exec.Exec(ctx, expireActiveForShipSQL, int64(shipID), now); err != nil {
		return fmt.Errorf("expire policy for ship: %w", err)
	}
	return nil
}

const activeForShipSQL = `
SELECT id, ship_id, player_id, premium_paid, coverage, status, created_at, expires_at
FROM insurance_policies
WHERE ship_id = $1 AND status = 'active' AND expires_at > $2
FOR UPDATE`

// ActiveForShip returns the ship's active, unexpired policy (locked FOR
// UPDATE), or ok=false when there is none. Called inside the payout
// transaction.
func (r *Repository) ActiveForShip(ctx context.Context, shipID domain.ShipID, now time.Time) (domain.InsurancePolicy, bool, error) {
	var (
		p      domain.InsurancePolicy
		status string
	)
	err := r.exec.QueryRow(ctx, activeForShipSQL, int64(shipID), now).Scan(
		&p.ID, &p.ShipID, &p.PlayerID, &p.PremiumPaid, &p.Coverage, &status, &p.CreatedAt, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InsurancePolicy{}, false, nil
	}
	if err != nil {
		return domain.InsurancePolicy{}, false, fmt.Errorf("query active policy: %w", err)
	}
	p.Status = domain.PolicyStatus(status)
	return p, true, nil
}

const claimSQL = `
UPDATE insurance_policies
SET status = 'claimed', claimed_at = $2
WHERE id = $1 AND status = 'active'`

// Claim flips an active policy to claimed (status-guarded).
func (r *Repository) Claim(ctx context.Context, id domain.PolicyID, claimedAt time.Time) error {
	if _, err := r.exec.Exec(ctx, claimSQL, int64(id), claimedAt); err != nil {
		return fmt.Errorf("claim policy: %w", err)
	}
	return nil
}

const listByPlayerSQL = `
SELECT id, ship_id, player_id, premium_paid, coverage, status, created_at, expires_at, claimed_at
FROM insurance_policies
WHERE player_id = $1
ORDER BY created_at DESC`

// ListByPlayer returns every policy a player holds, newest first.
func (r *Repository) ListByPlayer(ctx context.Context, player domain.PlayerID) ([]domain.InsurancePolicy, error) {
	rows, err := r.exec.Query(ctx, listByPlayerSQL, int64(player))
	if err != nil {
		return nil, fmt.Errorf("query policies by player: %w", err)
	}
	defer rows.Close()
	var out []domain.InsurancePolicy
	for rows.Next() {
		var (
			p         domain.InsurancePolicy
			status    string
			claimedAt *time.Time
		)
		if err := rows.Scan(&p.ID, &p.ShipID, &p.PlayerID, &p.PremiumPaid, &p.Coverage,
			&status, &p.CreatedAt, &p.ExpiresAt, &claimedAt); err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		p.Status = domain.PolicyStatus(status)
		if claimedAt != nil {
			p.ClaimedAt = *claimedAt
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate policies: %w", err)
	}
	return out, nil
}

const shipOwnershipSQL = `SELECT player_id, docked_kind, docked_id FROM ships WHERE id = $1`

// ShipOwnership returns who owns the ship and whether it is docked (nil =
// flying). Mirrors trade.LoadShipDock — used to authorize a Buy. Missing ship
// → ErrShipNotFound.
func (r *Repository) ShipOwnership(ctx context.Context, shipID domain.ShipID) (domain.PlayerID, *domain.EntityRef, error) {
	var (
		playerID   int64
		dockedKind *int16
		dockedID   *int64
	)
	err := r.exec.QueryRow(ctx, shipOwnershipSQL, int64(shipID)).Scan(&playerID, &dockedKind, &dockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, ErrShipNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("query ship ownership: %w", err)
	}
	var docked *domain.EntityRef
	if dockedKind != nil && dockedID != nil {
		docked = &domain.EntityRef{Kind: domain.EntityKind(*dockedKind), ID: *dockedID}
	}
	return domain.PlayerID(playerID), docked, nil
}
