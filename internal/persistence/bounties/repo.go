// Package bounties persists the bounties table (phase 6.3): funded contracts
// on a player's or clan's head. The Repository handles raw CRUD + the locked
// reads the payout/expiry transactions rely on; the social/bounties Service
// owns the escrow logic on top. See back/docs/specs/bounties.md.
package bounties

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// Repository talks to the bounties table via an Executor (pool or tx).
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
INSERT INTO bounties (target_kind, target_id, sponsor_kind, sponsor_id, amount, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`

// Create inserts an active bounty and returns its id.
func (r *Repository) Create(ctx context.Context, b domain.Bounty) (domain.BountyID, error) {
	var id int64
	err := r.exec.QueryRow(ctx, createSQL,
		int16(b.Target.Kind), b.Target.ID,
		int16(b.Sponsor.Kind), b.Sponsor.ID,
		b.Amount, b.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert bounty: %w", err)
	}
	return domain.BountyID(id), nil
}

const activeForTargetsPrefix = `
SELECT id, target_kind, target_id, sponsor_kind, sponsor_id, amount, status, created_at, expires_at
FROM bounties
WHERE status = 'active' AND expires_at > $1 AND (`

// ActiveForTargets returns, locked FOR UPDATE, every active non-expired bounty
// whose (kind,id) target is in targets. Called inside the payout transaction.
// An empty targets slice returns no rows. Each target becomes a paired
// `(target_kind=$k AND target_id=$i)` OR-group, so a bounty matches only on
// the exact (kind,id) tuple — never a cross of player-id with clan-kind. The
// WHERE is built dynamically (targets is at most player+clan, all int args —
// no injection surface); the per-target groups let the active-target partial
// index apply.
func (r *Repository) ActiveForTargets(ctx context.Context, now time.Time, targets []domain.EntityRef) ([]domain.Bounty, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	args := []any{now}
	groups := make([]string, len(targets))
	for i, t := range targets {
		args = append(args, int16(t.Kind), t.ID)
		groups[i] = fmt.Sprintf("(target_kind = $%d AND target_id = $%d)", len(args)-1, len(args))
	}
	sql := activeForTargetsPrefix + strings.Join(groups, " OR ") + ") FOR UPDATE"
	rows, err := r.exec.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query active bounties: %w", err)
	}
	defer rows.Close()
	return scanBounties(rows)
}

const dueExpiredSQL = `
SELECT id, target_kind, target_id, sponsor_kind, sponsor_id, amount, status, created_at, expires_at
FROM bounties
WHERE status = 'active' AND expires_at <= $1
ORDER BY expires_at
LIMIT $2
FOR UPDATE SKIP LOCKED`

// DueExpired returns up to limit active bounties past their deadline, locked
// FOR UPDATE SKIP LOCKED so a concurrent payout tx (which takes a plain
// FOR UPDATE) is never blocked — a row being claimed is simply skipped this
// sweep. Called inside the expiry transaction.
func (r *Repository) DueExpired(ctx context.Context, now time.Time, limit int) ([]domain.Bounty, error) {
	rows, err := r.exec.Query(ctx, dueExpiredSQL, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query due bounties: %w", err)
	}
	defer rows.Close()
	return scanBounties(rows)
}

const markPaidSQL = `
UPDATE bounties
SET status = 'paid', paid_to = $2, paid_at = $3
WHERE id = $1 AND status = 'active'`

// MarkPaid flips an active bounty to paid. The status guard makes it a no-op
// if another tx already settled the row (expiry race); errors only on the DB.
func (r *Repository) MarkPaid(ctx context.Context, id domain.BountyID, paidTo domain.PlayerID, at time.Time) error {
	if _, err := r.exec.Exec(ctx, markPaidSQL, int64(id), int64(paidTo), at); err != nil {
		return fmt.Errorf("mark bounty paid: %w", err)
	}
	return nil
}

const markExpiredSQL = `UPDATE bounties SET status = 'expired' WHERE id = $1 AND status = 'active'`

// MarkExpired flips an active bounty to expired. Status-guarded like MarkPaid.
func (r *Repository) MarkExpired(ctx context.Context, id domain.BountyID) error {
	if _, err := r.exec.Exec(ctx, markExpiredSQL, int64(id)); err != nil {
		return fmt.Errorf("mark bounty expired: %w", err)
	}
	return nil
}

// View is a bounty enriched with resolved display names, for the read
// endpoints. Names are nil-safe: a deleted target/sponsor reads as "".
type View struct {
	domain.Bounty
	TargetName  string
	SponsorName string
}

const listActiveSQL = `
SELECT b.id, b.target_kind, b.target_id, b.sponsor_kind, b.sponsor_id, b.amount, b.status, b.created_at, b.expires_at,
  COALESCE(CASE b.target_kind WHEN 9 THEN tp.login WHEN 10 THEN tc.name END, '') AS target_name,
  COALESCE(CASE b.sponsor_kind WHEN 9 THEN sp.login WHEN 10 THEN sc.name END, '') AS sponsor_name
FROM bounties b
LEFT JOIN players tp ON b.target_kind = 9 AND tp.id = b.target_id
LEFT JOIN clans tc ON b.target_kind = 10 AND tc.id = b.target_id
LEFT JOIN players sp ON b.sponsor_kind = 9 AND sp.id = b.sponsor_id
LEFT JOIN clans sc ON b.sponsor_kind = 10 AND sc.id = b.sponsor_id
WHERE b.status = 'active' AND b.expires_at > $1
ORDER BY b.amount DESC, b.created_at
LIMIT $2`

// ListActive returns the top active bounties (highest amount first) with
// resolved names, for GET /api/bounties.
func (r *Repository) ListActive(ctx context.Context, now time.Time, limit int) ([]View, error) {
	rows, err := r.exec.Query(ctx, listActiveSQL, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query active bounty views: %w", err)
	}
	defer rows.Close()
	return scanViews(rows)
}

const historyForTargetSQL = `
SELECT b.id, b.target_kind, b.target_id, b.sponsor_kind, b.sponsor_id, b.amount, b.status, b.created_at, b.expires_at,
  COALESCE(CASE b.target_kind WHEN 9 THEN tp.login WHEN 10 THEN tc.name END, '') AS target_name,
  COALESCE(CASE b.sponsor_kind WHEN 9 THEN sp.login WHEN 10 THEN sc.name END, '') AS sponsor_name
FROM bounties b
LEFT JOIN players tp ON b.target_kind = 9 AND tp.id = b.target_id
LEFT JOIN clans tc ON b.target_kind = 10 AND tc.id = b.target_id
LEFT JOIN players sp ON b.sponsor_kind = 9 AND sp.id = b.sponsor_id
LEFT JOIN clans sc ON b.sponsor_kind = 10 AND sc.id = b.sponsor_id
WHERE b.target_kind = $1 AND b.target_id = $2
ORDER BY b.created_at DESC
LIMIT $3`

// HistoryForTarget returns every bounty ever targeting the given entity, any
// status, newest first, for GET /api/players/{id}/bounty-history.
func (r *Repository) HistoryForTarget(ctx context.Context, target domain.EntityRef, limit int) ([]View, error) {
	rows, err := r.exec.Query(ctx, historyForTargetSQL, int16(target.Kind), target.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("query bounty history: %w", err)
	}
	defer rows.Close()
	return scanViews(rows)
}

// scanBounty reads the nine core columns shared by every query into a Bounty.
func scanBounty(rows pgx.Rows, b *domain.Bounty) error {
	var (
		targetKind, sponsorKind int16
		status                  string
	)
	if err := rows.Scan(&b.ID, &targetKind, &b.Target.ID, &sponsorKind, &b.Sponsor.ID,
		&b.Amount, &status, &b.CreatedAt, &b.ExpiresAt); err != nil {
		return err
	}
	b.Target.Kind = domain.EntityKind(targetKind)
	b.Sponsor.Kind = domain.EntityKind(sponsorKind)
	b.Status = domain.BountyStatus(status)
	return nil
}

func scanBounties(rows pgx.Rows) ([]domain.Bounty, error) {
	var out []domain.Bounty
	for rows.Next() {
		var b domain.Bounty
		if err := scanBounty(rows, &b); err != nil {
			return nil, fmt.Errorf("scan bounty: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bounties: %w", err)
	}
	return out, nil
}

func scanViews(rows pgx.Rows) ([]View, error) {
	out := []View{}
	for rows.Next() {
		var (
			v                       View
			targetKind, sponsorKind int16
			status                  string
		)
		if err := rows.Scan(&v.ID, &targetKind, &v.Target.ID, &sponsorKind, &v.Sponsor.ID,
			&v.Amount, &status, &v.CreatedAt, &v.ExpiresAt, &v.TargetName, &v.SponsorName); err != nil {
			return nil, fmt.Errorf("scan bounty view: %w", err)
		}
		v.Target.Kind = domain.EntityKind(targetKind)
		v.Sponsor.Kind = domain.EntityKind(sponsorKind)
		v.Status = domain.BountyStatus(status)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bounty views: %w", err)
	}
	return out, nil
}
