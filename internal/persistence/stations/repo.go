// Package stations reads the four static dockable objects of a sector —
// stations, shipyards, trade_stations and pirbases — from Postgres. Used
// once per sector worker at cold start; static objects have no in-game
// mutation until later phases add building/destruction.
package stations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrStationNotFound is returned by GetStation when no station has the
// requested id.
var ErrStationNotFound = errors.New("stations: station not found")

// Repository talks to the four static-object tables via an Executor.
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor. Used
// by callers that need to run a stations write inside a shared tx.
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// LoadAll returns every static object inside the given sector. The four
// queries run sequentially over the same executor; the dataset is small
// (≤ tens of objects per sector) so we don't bother parallelising.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) (domain.SectorStatics, error) {
	stations, err := r.loadStations(ctx, sectorID)
	if err != nil {
		return domain.SectorStatics{}, err
	}
	shipyards, err := r.loadShipyards(ctx, sectorID)
	if err != nil {
		return domain.SectorStatics{}, err
	}
	tradeStations, err := r.loadTradeStations(ctx, sectorID)
	if err != nil {
		return domain.SectorStatics{}, err
	}
	pirbases, err := r.loadPirbases(ctx, sectorID)
	if err != nil {
		return domain.SectorStatics{}, err
	}
	return domain.SectorStatics{
		Stations:      stations,
		Shipyards:     shipyards,
		TradeStations: tradeStations,
		Pirbases:      pirbases,
	}, nil
}

const loadStationsSQL = `
SELECT id, owner_id, type, sector_id, pos_x, pos_y, hp, shield, race, built, in_progress, next_cycle_at, max_shield, shield_recharge
FROM stations
WHERE sector_id = $1
ORDER BY id
`

func (r *Repository) loadStations(ctx context.Context, sectorID domain.SectorID) ([]domain.Station, error) {
	rows, err := r.exec.Query(ctx, loadStationsSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query stations: %w", err)
	}
	defer rows.Close()

	var out []domain.Station
	for rows.Next() {
		s, err := scanStation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan station: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stations: %w", err)
	}
	return out, nil
}

const getStationSQL = `
SELECT id, owner_id, type, sector_id, pos_x, pos_y, hp, shield, race, built, in_progress, next_cycle_at, max_shield, shield_recharge
FROM stations
WHERE id = $1
`

// GetStation reads one station by id, including its production-cycle state
// (in_progress / next_cycle_at). Returns ErrStationNotFound when absent.
func (r *Repository) GetStation(ctx context.Context, id domain.StationID) (domain.Station, error) {
	s, err := scanStation(r.exec.QueryRow(ctx, getStationSQL, int64(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Station{}, ErrStationNotFound
	}
	if err != nil {
		return domain.Station{}, fmt.Errorf("query station: %w", err)
	}
	return s, nil
}

// rowScanner is the read surface shared by pgx.Rows and pgx.Row so the
// list loop (loadStations) and the single read (GetStation) decode a
// station row identically.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanStation(row rowScanner) (domain.Station, error) {
	var (
		id, sectorIDRow           int64
		ownerID                   *int64
		typ, hp, shield           int
		maxShield, shieldRecharge int
		posX, posY                float64
		race                      int
		built                     bool
		inProgress                bool
		nextCycleAt               *time.Time
	)
	if err := row.Scan(&id, &ownerID, &typ, &sectorIDRow, &posX, &posY, &hp, &shield, &race, &built, &inProgress, &nextCycleAt, &maxShield, &shieldRecharge); err != nil {
		return domain.Station{}, err
	}
	s := domain.Station{
		ID:             domain.StationID(id),
		Type:           typ,
		SectorID:       domain.SectorID(sectorIDRow),
		Pos:            domain.Vec2{X: posX, Y: posY},
		HP:             hp,
		Shield:         shield,
		MaxShield:      maxShield,
		ShieldRecharge: shieldRecharge,
		Race:           race,
		Built:          built,
		InProgress:     inProgress,
	}
	if nextCycleAt != nil {
		s.NextCycleAt = *nextCycleAt
	}
	if ownerID != nil {
		pid := domain.PlayerID(*ownerID)
		s.OwnerID = &pid
	}
	return s, nil
}

const updateProductionSQL = `
UPDATE stations
SET in_progress = $2, next_cycle_at = $3
WHERE id = $1
`

// UpdateProduction persists the production-cycle state of one station.
// Pass a zero time.Time to clear next_cycle_at (cycle finished or never
// started); a non-zero value is written as TIMESTAMPTZ.
func (r *Repository) UpdateProduction(ctx context.Context, id domain.StationID, inProgress bool, nextCycleAt time.Time) error {
	var nextArg any
	if !nextCycleAt.IsZero() {
		nextArg = nextCycleAt
	}
	if _, err := r.exec.Exec(ctx, updateProductionSQL, int64(id), inProgress, nextArg); err != nil {
		return fmt.Errorf("update station production: %w", err)
	}
	return nil
}

// OwnedStatic is a player-owned static object: its typed ref and current
// owner. Returned by PlayerOwned for the rent reconcile (6.4).
type OwnedStatic struct {
	Ref   domain.EntityRef
	Owner domain.PlayerID
}

const playerOwnedSQL = `
SELECT 2 AS kind, id, owner_id FROM stations        WHERE owner_id IS NOT NULL
UNION ALL
SELECT 3,        id, owner_id FROM shipyards       WHERE owner_id IS NOT NULL
UNION ALL
SELECT 4,        id, owner_id FROM trade_stations  WHERE owner_id IS NOT NULL
ORDER BY kind, id`

// PlayerOwned returns every station/shipyard/trade-station that has a non-null
// owner, across all sectors. The rent service reconciles these into rent rows.
func (r *Repository) PlayerOwned(ctx context.Context) ([]OwnedStatic, error) {
	rows, err := r.exec.Query(ctx, playerOwnedSQL)
	if err != nil {
		return nil, fmt.Errorf("query player-owned statics: %w", err)
	}
	defer rows.Close()
	var out []OwnedStatic
	for rows.Next() {
		var (
			kind  int16
			id    int64
			owner int64
		)
		if err := rows.Scan(&kind, &id, &owner); err != nil {
			return nil, fmt.Errorf("scan player-owned static: %w", err)
		}
		out = append(out, OwnedStatic{
			Ref:   domain.EntityRef{Kind: domain.EntityKind(kind), ID: id},
			Owner: domain.PlayerID(owner),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate player-owned statics: %w", err)
	}
	return out, nil
}

// ClaimUnowned sets owner_id = owner only if the object is currently unowned
// (owner_id IS NULL), returning claimed=true when it took it. Used by the
// player station-acquisition flow (8.7). MVP supports stations only.
func (r *Repository) ClaimUnowned(ctx context.Context, ref domain.EntityRef, owner domain.PlayerID) (bool, error) {
	if ref.Kind != domain.EntityKindStation {
		return false, fmt.Errorf("claim: unsupported kind %d", ref.Kind)
	}
	tag, err := r.exec.Exec(ctx,
		`UPDATE stations SET owner_id = $2 WHERE id = $1 AND owner_id IS NULL`, ref.ID, int64(owner))
	if err != nil {
		return false, fmt.Errorf("claim station: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClearOwner sets owner_id = NULL for the given object (rent confiscation,
// 6.4). The table is chosen by the ref's kind; an unsupported kind is an
// error. Persists only — the running sector worker's RAM copy keeps the old
// owner until the next restart (see rent.md deferred notes).
func (r *Repository) ClearOwner(ctx context.Context, ref domain.EntityRef) error {
	var sql string
	switch ref.Kind {
	case domain.EntityKindStation:
		sql = `UPDATE stations SET owner_id = NULL WHERE id = $1`
	case domain.EntityKindShipyard:
		sql = `UPDATE shipyards SET owner_id = NULL WHERE id = $1`
	case domain.EntityKindTradeStation:
		sql = `UPDATE trade_stations SET owner_id = NULL WHERE id = $1`
	default:
		return fmt.Errorf("clear owner: unsupported kind %d", ref.Kind)
	}
	if _, err := r.exec.Exec(ctx, sql, ref.ID); err != nil {
		return fmt.Errorf("clear owner: %w", err)
	}
	return nil
}

const loadShipyardsSQL = `
SELECT id, owner_id, sector_id, pos_x, pos_y, hp, shield, race, built, max_shield, shield_recharge
FROM shipyards
WHERE sector_id = $1
ORDER BY id
`

func (r *Repository) loadShipyards(ctx context.Context, sectorID domain.SectorID) ([]domain.Shipyard, error) {
	rows, err := r.exec.Query(ctx, loadShipyardsSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query shipyards: %w", err)
	}
	defer rows.Close()

	var out []domain.Shipyard
	for rows.Next() {
		var (
			id, sectorIDRow           int64
			ownerID                   *int64
			hp, shield                int
			maxShield, shieldRecharge int
			posX, posY                float64
			race                      int
			built                     bool
		)
		if err := rows.Scan(&id, &ownerID, &sectorIDRow, &posX, &posY, &hp, &shield, &race, &built, &maxShield, &shieldRecharge); err != nil {
			return nil, fmt.Errorf("scan shipyard: %w", err)
		}
		s := domain.Shipyard{
			ID:             domain.ShipyardID(id),
			SectorID:       domain.SectorID(sectorIDRow),
			Pos:            domain.Vec2{X: posX, Y: posY},
			HP:             hp,
			Shield:         shield,
			MaxShield:      maxShield,
			ShieldRecharge: shieldRecharge,
			Race:           race,
			Built:          built,
		}
		if ownerID != nil {
			pid := domain.PlayerID(*ownerID)
			s.OwnerID = &pid
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shipyards: %w", err)
	}
	return out, nil
}

const loadTradeStationsSQL = `
SELECT id, owner_id, type, sector_id, pos_x, pos_y, hp, shield, race, built, max_shield, shield_recharge
FROM trade_stations
WHERE sector_id = $1
ORDER BY id
`

func (r *Repository) loadTradeStations(ctx context.Context, sectorID domain.SectorID) ([]domain.TradeStation, error) {
	rows, err := r.exec.Query(ctx, loadTradeStationsSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query trade_stations: %w", err)
	}
	defer rows.Close()

	var out []domain.TradeStation
	for rows.Next() {
		var (
			id, sectorIDRow           int64
			ownerID                   *int64
			typ, hp, shield           int
			maxShield, shieldRecharge int
			posX, posY                float64
			race                      int
			built                     bool
		)
		if err := rows.Scan(&id, &ownerID, &typ, &sectorIDRow, &posX, &posY, &hp, &shield, &race, &built, &maxShield, &shieldRecharge); err != nil {
			return nil, fmt.Errorf("scan trade_station: %w", err)
		}
		t := domain.TradeStation{
			ID:             domain.TradeStationID(id),
			Type:           typ,
			SectorID:       domain.SectorID(sectorIDRow),
			Pos:            domain.Vec2{X: posX, Y: posY},
			HP:             hp,
			Shield:         shield,
			MaxShield:      maxShield,
			ShieldRecharge: shieldRecharge,
			Race:           race,
			Built:          built,
		}
		if ownerID != nil {
			pid := domain.PlayerID(*ownerID)
			t.OwnerID = &pid
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trade_stations: %w", err)
	}
	return out, nil
}

const loadPirbasesSQL = `
SELECT id, sector_id, pos_x, pos_y, hp, shield, angle, race, built, max_shield, shield_recharge
FROM pirbases
WHERE sector_id = $1
ORDER BY id
`

func (r *Repository) loadPirbases(ctx context.Context, sectorID domain.SectorID) ([]domain.Pirbase, error) {
	rows, err := r.exec.Query(ctx, loadPirbasesSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query pirbases: %w", err)
	}
	defer rows.Close()

	var out []domain.Pirbase
	for rows.Next() {
		var (
			id, sectorIDRow           int64
			hp, shield                int
			maxShield, shieldRecharge int
			posX, posY                float64
			angle                     float64
			race                      int
			built                     bool
		)
		if err := rows.Scan(&id, &sectorIDRow, &posX, &posY, &hp, &shield, &angle, &race, &built, &maxShield, &shieldRecharge); err != nil {
			return nil, fmt.Errorf("scan pirbase: %w", err)
		}
		out = append(out, domain.Pirbase{
			ID:             domain.PirbaseID(id),
			SectorID:       domain.SectorID(sectorIDRow),
			Pos:            domain.Vec2{X: posX, Y: posY},
			HP:             hp,
			Shield:         shield,
			MaxShield:      maxShield,
			ShieldRecharge: shieldRecharge,
			Angle:          angle,
			Race:           race,
			Built:          built,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pirbases: %w", err)
	}
	return out, nil
}
