// Package racestanding tracks each player's reputation with the NPC races
// (phase 9.4) and the "wanted" lookup the police/navy use to decide whether to
// open fire. The effective standing is resolved in RAM (Service) for ~1µs
// reads, mirroring social/relations (6.2); this Repository only persists the
// non-default standings. See back/docs/specs/police_contraband.md.
package racestanding

import (
	"context"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// Row is one persisted standing: a (player, race) pair and its value. Only
// non-zero standings are stored — absence means the neutral default (0).
type Row struct {
	Player   domain.PlayerID
	Race     domain.RaceID
	Standing int
}

// Repository persists the player_race_standing table via an Executor.
type Repository struct {
	exec database.Executor
}

// NewRepository wires a Repository to the given executor.
func NewRepository(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadAllSQL = `SELECT player_id, race, standing FROM player_race_standing`

// LoadAll returns every stored standing, for the Service to build its RAM
// lookup at Precount (and to refresh it after Decay).
func (r *Repository) LoadAll(ctx context.Context) ([]Row, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL)
	if err != nil {
		return nil, fmt.Errorf("query race standings: %w", err)
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var (
			player   int64
			race     int16
			standing int
		)
		if err := rows.Scan(&player, &race, &standing); err != nil {
			return nil, fmt.Errorf("scan race standing: %w", err)
		}
		out = append(out, Row{
			Player:   domain.PlayerID(player),
			Race:     domain.RaceID(race),
			Standing: standing,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate race standings: %w", err)
	}
	return out, nil
}

const upsertSQL = `
INSERT INTO player_race_standing (player_id, race, standing, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (player_id, race)
DO UPDATE SET standing = EXCLUDED.standing, updated_at = NOW()
`

// Upsert writes the player's standing with a race.
func (r *Repository) Upsert(ctx context.Context, player domain.PlayerID, race domain.RaceID, standing int) error {
	if _, err := r.exec.Exec(ctx, upsertSQL, int64(player), int16(race), standing); err != nil {
		return fmt.Errorf("upsert race standing: %w", err)
	}
	return nil
}

const decayAllSQL = `
UPDATE player_race_standing
SET standing = CASE
        WHEN standing > 0 THEN standing - LEAST($1, standing)
        WHEN standing < 0 THEN standing + LEAST($1, -standing)
        ELSE 0
    END,
    updated_at = NOW()
WHERE standing <> 0
`

// DecayAll nudges every non-zero standing one step toward 0 in a single
// statement (the slow recovery toward neutral, phase 9.4). step must be >= 0.
func (r *Repository) DecayAll(ctx context.Context, step int) error {
	if _, err := r.exec.Exec(ctx, decayAllSQL, step); err != nil {
		return fmt.Errorf("decay race standings: %w", err)
	}
	return nil
}
