// Package world persists the static world topology (sectors and gates).
// The data is loaded once at server startup and exposed through an
// in-memory Topology — there are no writes from the running game.
package world

import (
	"context"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// Repository reads sectors and gates from Postgres.
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

const loadSectorsSQL = `
SELECT s.id, s.name, s.min_x, s.min_y, s.max_x, s.max_y, s.grid_x, s.grid_y,
    COALESCE(
        (SELECT ts.race FROM trade_stations ts WHERE ts.sector_id = s.id AND ts.built AND ts.race > 0 ORDER BY ts.id LIMIT 1),
        (SELECT sy.race FROM shipyards sy WHERE sy.sector_id = s.id AND sy.built AND sy.race > 0 ORDER BY sy.id LIMIT 1),
        (SELECT st.race FROM stations st WHERE st.sector_id = s.id AND st.built AND st.race > 0 ORDER BY st.id LIMIT 1),
        0
    ) AS race
FROM sectors s
ORDER BY s.id
`

const loadGatesSQL = `
SELECT id, sector_a, pos_a_x, pos_a_y, sector_b, pos_b_x, pos_b_y
FROM gates
ORDER BY id
`

// LoadAll returns every sector and gate. Called once at startup; both
// slices are empty (not nil) when the tables are empty.
func (r *Repository) LoadAll(ctx context.Context) ([]domain.Sector, []domain.Gate, error) {
	sectors, err := r.loadSectors(ctx)
	if err != nil {
		return nil, nil, err
	}
	gates, err := r.loadGates(ctx)
	if err != nil {
		return nil, nil, err
	}
	return sectors, gates, nil
}

func (r *Repository) loadSectors(ctx context.Context) ([]domain.Sector, error) {
	rows, err := r.exec.Query(ctx, loadSectorsSQL)
	if err != nil {
		return nil, fmt.Errorf("query sectors: %w", err)
	}
	defer rows.Close()

	out := []domain.Sector{}
	for rows.Next() {
		var (
			id                     int64
			name                   string
			minX, minY, maxX, maxY float64
			gridX, gridY, race     int
		)
		if err := rows.Scan(&id, &name, &minX, &minY, &maxX, &maxY, &gridX, &gridY, &race); err != nil {
			return nil, fmt.Errorf("scan sector: %w", err)
		}
		out = append(out, domain.Sector{
			ID:   domain.SectorID(id),
			Name: name,
			Bounds: domain.Rect{
				Min: domain.Vec2{X: minX, Y: minY},
				Max: domain.Vec2{X: maxX, Y: maxY},
			},
			GridX: gridX,
			GridY: gridY,
			Race:  race,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sectors: %w", err)
	}
	return out, nil
}

func (r *Repository) loadGates(ctx context.Context) ([]domain.Gate, error) {
	rows, err := r.exec.Query(ctx, loadGatesSQL)
	if err != nil {
		return nil, fmt.Errorf("query gates: %w", err)
	}
	defer rows.Close()

	out := []domain.Gate{}
	for rows.Next() {
		var (
			id, sectorA, sectorB       int64
			posAX, posAY, posBX, posBY float64
		)
		if err := rows.Scan(&id, &sectorA, &posAX, &posAY, &sectorB, &posBX, &posBY); err != nil {
			return nil, fmt.Errorf("scan gate: %w", err)
		}
		out = append(out, domain.Gate{
			ID:      domain.GateID(id),
			SectorA: domain.SectorID(sectorA),
			PosA:    domain.Vec2{X: posAX, Y: posAY},
			SectorB: domain.SectorID(sectorB),
			PosB:    domain.Vec2{X: posBX, Y: posBY},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate gates: %w", err)
	}
	return out, nil
}
