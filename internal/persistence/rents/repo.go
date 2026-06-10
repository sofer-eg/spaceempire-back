// Package rents persists the rents table (phase 6.4): periodic ownership
// upkeep obligations. The economy/rent Service owns the billing logic; this
// repo does CRUD + the locked due-scan the billing transaction relies on.
// See back/docs/specs/rent.md.
package rents

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// Repository talks to the rents table via an Executor (pool or tx).
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

const ensureSQL = `
INSERT INTO rents (payer_id, station_kind, station_id, amount_per_period, next_due_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (station_kind, station_id) DO NOTHING`

// Ensure inserts a rent row for the object, or does nothing if one already
// exists (idempotent — the reconcile calls it for every player-owned object
// every cycle). next_due_at is only used on first insert.
func (r *Repository) Ensure(ctx context.Context, payer domain.PlayerID, station domain.EntityRef, amountPerPeriod int64, nextDue time.Time) error {
	if _, err := r.exec.Exec(ctx, ensureSQL,
		int64(payer), int16(station.Kind), station.ID, amountPerPeriod, nextDue); err != nil {
		return fmt.Errorf("ensure rent: %w", err)
	}
	return nil
}

const dueSQL = `
SELECT id, payer_id, station_kind, station_id, amount_per_period, unpaid_periods, last_paid_at, next_due_at, created_at
FROM rents
WHERE next_due_at <= $1
ORDER BY next_due_at
LIMIT $2
FOR UPDATE SKIP LOCKED`

// Due returns up to limit rents whose next charge is owed, locked FOR UPDATE
// SKIP LOCKED so a concurrent billing run never blocks. Called inside the
// billing transaction.
func (r *Repository) Due(ctx context.Context, now time.Time, limit int) ([]domain.Rent, error) {
	rows, err := r.exec.Query(ctx, dueSQL, now, limit)
	if err != nil {
		return nil, fmt.Errorf("query due rents: %w", err)
	}
	defer rows.Close()
	return scanRents(rows)
}

const markPaidSQL = `
UPDATE rents
SET unpaid_periods = 0, last_paid_at = $2, next_due_at = $3
WHERE id = $1`

// MarkPaid records a successful charge: clears the unpaid counter and advances
// the schedule.
func (r *Repository) MarkPaid(ctx context.Context, id domain.RentID, paidAt, nextDue time.Time) error {
	if _, err := r.exec.Exec(ctx, markPaidSQL, int64(id), paidAt, nextDue); err != nil {
		return fmt.Errorf("mark rent paid: %w", err)
	}
	return nil
}

const markUnpaidSQL = `
UPDATE rents
SET unpaid_periods = $2, next_due_at = $3
WHERE id = $1`

// MarkUnpaid records a missed charge: stores the new unpaid count and advances
// the schedule so the next cycle retries (rather than re-charging immediately).
func (r *Repository) MarkUnpaid(ctx context.Context, id domain.RentID, unpaidPeriods int, nextDue time.Time) error {
	if _, err := r.exec.Exec(ctx, markUnpaidSQL, int64(id), unpaidPeriods, nextDue); err != nil {
		return fmt.Errorf("mark rent unpaid: %w", err)
	}
	return nil
}

const deleteSQL = `DELETE FROM rents WHERE id = $1`

// Delete removes a rent row (on confiscation).
func (r *Repository) Delete(ctx context.Context, id domain.RentID) error {
	if _, err := r.exec.Exec(ctx, deleteSQL, int64(id)); err != nil {
		return fmt.Errorf("delete rent: %w", err)
	}
	return nil
}

const listByPayerSQL = `
SELECT id, payer_id, station_kind, station_id, amount_per_period, unpaid_periods, last_paid_at, next_due_at, created_at
FROM rents
WHERE payer_id = $1
ORDER BY next_due_at`

// ListByPayer returns every rent a player owes, for GET /api/my/rents.
func (r *Repository) ListByPayer(ctx context.Context, payer domain.PlayerID) ([]domain.Rent, error) {
	rows, err := r.exec.Query(ctx, listByPayerSQL, int64(payer))
	if err != nil {
		return nil, fmt.Errorf("query rents by payer: %w", err)
	}
	defer rows.Close()
	return scanRents(rows)
}

func scanRents(rows pgx.Rows) ([]domain.Rent, error) {
	var out []domain.Rent
	for rows.Next() {
		var (
			rent     domain.Rent
			kind     int16
			lastPaid *time.Time
			unpaid   int16
		)
		if err := rows.Scan(&rent.ID, &rent.Payer, &kind, &rent.Station.ID,
			&rent.AmountPerPeriod, &unpaid, &lastPaid, &rent.NextDueAt, &rent.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan rent: %w", err)
		}
		rent.Station.Kind = domain.EntityKind(kind)
		rent.UnpaidPeriods = int(unpaid)
		if lastPaid != nil {
			rent.LastPaidAt = *lastPaid
		}
		out = append(out, rent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rents: %w", err)
	}
	return out, nil
}
