// Package npcships persists the npc_ships table (phase 5.3): the link from
// an NPC ship to the home station it serves, plus the lookup of the reserved
// system player that owns every NPC ship. The route/phase/goods of a trader
// live in ai_state, not here — npc_ships only records identity, so the
// cold-start NPC spawner stays idempotent (it skips a home already served).
package npcships

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// SystemPlayerLogin is the reserved login of the player that owns every NPC
// ship. Seeded by migration 0024 with an unusable password hash.
const SystemPlayerLogin = "__npc__"

// ErrSystemPlayerMissing is returned by SystemPlayerID when the reserved NPC
// player row is absent — a wiring/migration bug, surfaced rather than
// silently spawning ownerless ships.
var ErrSystemPlayerMissing = errors.New("npcships: system player __npc__ not found")

// Repository talks to the npc_ships table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const systemPlayerSQL = `SELECT id FROM players WHERE login = $1`

// SystemPlayerID returns the id of the reserved __npc__ player. Returns
// ErrSystemPlayerMissing when the seed row is absent.
func (r *Repository) SystemPlayerID(ctx context.Context) (domain.PlayerID, error) {
	var id int64
	err := r.exec.QueryRow(ctx, systemPlayerSQL, SystemPlayerLogin).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrSystemPlayerMissing
	}
	if err != nil {
		return 0, fmt.Errorf("query system player: %w", err)
	}
	return domain.PlayerID(id), nil
}

// HomeKind keys a CountByHome result: how many NPC ships of a given
// controller kind serve a given home station. The spawner uses it to count
// traders and miners on the same home independently (a factory can have both).
type HomeKind struct {
	Home domain.EntityRef
	Kind string
}

const createSQL = `
INSERT INTO npc_ships (ship_id, home_kind, home_id, controller_kind)
VALUES ($1, $2, $3, $4)
`

// Create records a freshly spawned NPC ship, its home station, and the
// controller kind that drives it ("trader" / "miner").
func (r *Repository) Create(ctx context.Context, shipID domain.ShipID, home domain.EntityRef, kind string) error {
	if _, err := r.exec.Exec(ctx, createSQL, int64(shipID), int16(home.Kind), home.ID, kind); err != nil {
		return fmt.Errorf("insert npc_ship: %w", err)
	}
	return nil
}

const countByHomeSQL = `
SELECT home_kind, home_id, controller_kind, COUNT(*)
FROM npc_ships
GROUP BY home_kind, home_id, controller_kind
`

// CountByHome returns how many NPC ships currently serve each (home, kind)
// pair. The spawner uses it to decide which homes still need a trader / miner
// (idempotent across restarts).
func (r *Repository) CountByHome(ctx context.Context) (map[HomeKind]int, error) {
	rows, err := r.exec.Query(ctx, countByHomeSQL)
	if err != nil {
		return nil, fmt.Errorf("query npc_ships by home: %w", err)
	}
	defer rows.Close()

	out := make(map[HomeKind]int)
	for rows.Next() {
		var (
			kind           int16
			id             int64
			controllerKind string
			count          int
		)
		if err := rows.Scan(&kind, &id, &controllerKind, &count); err != nil {
			return nil, fmt.Errorf("scan npc_ships count: %w", err)
		}
		out[HomeKind{Home: domain.EntityRef{Kind: domain.EntityKind(kind), ID: id}, Kind: controllerKind}] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate npc_ships count: %w", err)
	}
	return out, nil
}
