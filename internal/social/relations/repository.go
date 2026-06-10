// Package relations implements declared relations between entities and the
// hostility lookup (phase 6.2): player↔player and clan↔clan standings that
// decide who may attack whom. The effective relation is resolved in RAM
// (Service) for ≤1µs lookups; this Repository only persists the declared
// pairs. Combat wiring (gating fire by hostility) is a separate task (6.2a).
package relations

import (
	"context"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// Row is one declared relation as stored: a directed (from → to) pair with
// a status. Only non-neutral statuses are persisted.
type Row struct {
	From   domain.EntityRef
	To     domain.EntityRef
	Status domain.Relation
}

// Repository persists the relations table via an Executor.
type Repository struct {
	exec database.Executor
}

// NewRepository wires a Repository to the given executor.
func NewRepository(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `SELECT from_kind, from_id, to_kind, to_id, status FROM relations`

// LoadAll returns every declared relation, for the Service to build its RAM
// lookup at Precount.
func (r *Repository) LoadAll(ctx context.Context) ([]Row, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL)
	if err != nil {
		return nil, fmt.Errorf("query relations: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var (
			fromKind, toKind int16
			fromID, toID     int64
			status           string
		)
		if err := rows.Scan(&fromKind, &fromID, &toKind, &toID, &status); err != nil {
			return nil, fmt.Errorf("scan relation: %w", err)
		}
		rel, ok := parseStatus(status)
		if !ok {
			// Unknown status text (manual edit / future kind) — skip rather
			// than poison the whole load.
			continue
		}
		out = append(out, Row{
			From:   domain.EntityRef{Kind: domain.EntityKind(fromKind), ID: fromID},
			To:     domain.EntityRef{Kind: domain.EntityKind(toKind), ID: toID},
			Status: rel,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relations: %w", err)
	}
	return out, nil
}

const upsertSQL = `
INSERT INTO relations (from_kind, from_id, to_kind, to_id, status, declared_at)
VALUES ($1, $2, $3, $4, $5, NOW())
ON CONFLICT (from_kind, from_id, to_kind, to_id)
DO UPDATE SET status = EXCLUDED.status, declared_at = NOW()
`

// Upsert writes (or updates) a declared relation.
func (r *Repository) Upsert(ctx context.Context, from, to domain.EntityRef, status domain.Relation) error {
	_, err := r.exec.Exec(ctx, upsertSQL,
		int16(from.Kind), from.ID, int16(to.Kind), to.ID, status.String())
	if err != nil {
		return fmt.Errorf("upsert relation: %w", err)
	}
	return nil
}

const deleteSQL = `DELETE FROM relations WHERE from_kind=$1 AND from_id=$2 AND to_kind=$3 AND to_id=$4`

// Delete removes a declared relation (used when a relation is reset to
// neutral — the default needs no row). Missing rows are a no-op.
func (r *Repository) Delete(ctx context.Context, from, to domain.EntityRef) error {
	if _, err := r.exec.Exec(ctx, deleteSQL,
		int16(from.Kind), from.ID, int16(to.Kind), to.ID); err != nil {
		return fmt.Errorf("delete relation: %w", err)
	}
	return nil
}

// parseStatus maps the stored status text back to a Relation. Neutral is
// never stored (absence means neutral), so it is not a valid stored value.
func parseStatus(s string) (domain.Relation, bool) {
	switch s {
	case "friend":
		return domain.RelationFriend, true
	case "hostile":
		return domain.RelationHostile, true
	case "at_war":
		return domain.RelationAtWar, true
	default:
		return domain.RelationNeutral, false
	}
}
