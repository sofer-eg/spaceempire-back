// Package ships persists the ship table for sector workers: cold-start
// LoadAll, immediate Save/Delete for critical events, and BatchUpdate for
// periodic snapshots driven by the worker's dirty-set.
package ships

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

// ErrShipNotFound is returned by Save/Delete when no row with the given id
// exists. LoadAll never returns it (empty result is just an empty slice).
var ErrShipNotFound = errors.New("ships: not found")

// Repository talks to the ships table via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// New wires a Repository to the given executor.
func New(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor (a pgx.Tx),
// so a caller can run Create / SaveEquipment inside a shared transaction with
// other repos (e.g. debit cash + insert ship atomically — phase 10.14).
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// loadAllSQL keeps the historical column names final_target_dock_kind /
// final_target_dock_id even though phase 3.12 renamed the in-Go field
// from Course.Dock to Course.Approach. The semantics changed (the column
// now records the parked-static reference, not an auto-dock target), but
// renaming the SQL columns would only churn migrations for no observable
// gain.
const loadAllSQL = `
SELECT id, player_id, race, sector_id, pos_x, pos_y, vel_x, vel_y,
       max_speed, acceleration, turn_rate, direction_x, direction_y,
       target_x, target_y,
       final_target_sector, final_target_x, final_target_y,
       final_target_dock_kind, final_target_dock_id,
       hp, max_hp, shield, max_shield, shield_recharge,
       energy, max_energy, energy_recharge, energy_delta,
       laser_damage, laser_range, laser_energy_cost,
       attack_kind, attack_id,
       docked_kind, docked_id,
       passengers, is_spacesuit, name, ship_class_id, equipment, radar_range,
       is_open
FROM ships
WHERE sector_id = $1
ORDER BY id
`

// LoadAll returns every ship that currently belongs to the given sector.
// Called once at worker startup to seed the in-memory state.
func (r *Repository) LoadAll(ctx context.Context, sectorID domain.SectorID) ([]domain.Ship, error) {
	rows, err := r.exec.Query(ctx, loadAllSQL, int64(sectorID))
	if err != nil {
		return nil, fmt.Errorf("query ships: %w", err)
	}
	defer rows.Close()

	var out []domain.Ship
	for rows.Next() {
		var (
			id, playerID, sectorIDRow         int64
			raceVal                           int16
			posX, posY, velX, velY            float64
			maxSpeed, accel, turnRate         float64
			dirX, dirY                        float64
			targetX, targetY                  *float64
			finalSector                       *int64
			finalX, finalY                    *float64
			finalApproachKind                 *int16
			finalApproachID                   *int64
			hp, maxHP, shield, maxShield, sch int
			energy, maxEnergy, energyRch      int
			energyDelta                       int
			laserDmg, laserCost               int
			laserRange                        float64
			attackKind                        *int16
			attackID                          *int64
			dockedKind                        *int16
			dockedID                          *int64
			passengers                        int
			isSpacesuit                       bool
			name                              string
			shipClassID                       int64
			equipmentRaw                      []byte
			radarRange                        float64
			isOpen                            bool
		)
		if err := rows.Scan(
			&id, &playerID, &raceVal, &sectorIDRow,
			&posX, &posY, &velX, &velY,
			&maxSpeed, &accel, &turnRate, &dirX, &dirY,
			&targetX, &targetY,
			&finalSector, &finalX, &finalY,
			&finalApproachKind, &finalApproachID,
			&hp, &maxHP, &shield, &maxShield, &sch,
			&energy, &maxEnergy, &energyRch, &energyDelta,
			&laserDmg, &laserRange, &laserCost,
			&attackKind, &attackID,
			&dockedKind, &dockedID,
			&passengers, &isSpacesuit, &name, &shipClassID, &equipmentRaw, &radarRange,
			&isOpen,
		); err != nil {
			return nil, fmt.Errorf("scan ship: %w", err)
		}
		equipment, err := equipmentFromJSON(equipmentRaw)
		if err != nil {
			return nil, fmt.Errorf("decode ship equipment (id=%d): %w", id, err)
		}
		s := domain.Ship{
			ID:              domain.ShipID(id),
			PlayerID:        domain.PlayerID(playerID),
			Race:            domain.RaceID(raceVal),
			Name:            name,
			ShipClassID:     domain.ShipClassID(shipClassID),
			SectorID:        domain.SectorID(sectorIDRow),
			Pos:             domain.Vec2{X: posX, Y: posY},
			Vel:             domain.Vec2{X: velX, Y: velY},
			MaxSpeed:        maxSpeed,
			Acceleration:    accel,
			TurnRate:        turnRate,
			Direction:       domain.Vec2{X: dirX, Y: dirY},
			HP:              hp,
			MaxHP:           maxHP,
			Shield:          shield,
			MaxShield:       maxShield,
			ShieldRecharge:  sch,
			Energy:          energy,
			MaxEnergy:       maxEnergy,
			EnergyRecharge:  energyRch,
			EnergyDelta:     energyDelta,
			LaserDamage:     laserDmg,
			LaserRange:      laserRange,
			LaserEnergyCost: laserCost,
			Passengers:      passengers,
			IsSpacesuit:     isSpacesuit,
			Equipment:       equipment,
			RadarRange:      radarRange,
			IsOpen:          isOpen,
		}
		if attackKind != nil && attackID != nil {
			s.AttackTarget = &domain.EntityRef{
				Kind: domain.EntityKind(*attackKind),
				ID:   *attackID,
			}
		}
		if targetX != nil && targetY != nil {
			s.Target = &domain.Vec2{X: *targetX, Y: *targetY}
		}
		if finalSector != nil && finalX != nil && finalY != nil {
			course := &domain.Course{
				Sector: domain.SectorID(*finalSector),
				Pos:    domain.Vec2{X: *finalX, Y: *finalY},
			}
			if finalApproachKind != nil && finalApproachID != nil {
				course.Approach = &domain.EntityRef{
					Kind: domain.EntityKind(*finalApproachKind),
					ID:   *finalApproachID,
				}
			}
			s.FinalTarget = course
		}
		if dockedKind != nil && dockedID != nil {
			s.Docked = &domain.EntityRef{
				Kind: domain.EntityKind(*dockedKind),
				ID:   *dockedID,
			}
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ships: %w", err)
	}
	return out, nil
}

const createSQL = `
INSERT INTO ships (
    player_id, race, sector_id, pos_x, pos_y, vel_x, vel_y,
    max_speed, acceleration, turn_rate, direction_x, direction_y,
    target_x, target_y,
    final_target_sector, final_target_x, final_target_y,
    final_target_dock_kind, final_target_dock_id,
    hp, max_hp, shield, max_shield, shield_recharge,
    energy, max_energy, energy_recharge,
    laser_damage, laser_range, laser_energy_cost,
    attack_kind, attack_id,
    docked_kind, docked_id,
    is_spacesuit, name, ship_class_id, equipment, radar_range, is_open
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40)
RETURNING id
`

// Create inserts a new ship row and returns its database-assigned id.
// Caller passes ID==0; non-zero ID is preserved for explicit ids (tests).
func (r *Repository) Create(ctx context.Context, s domain.Ship) (domain.ShipID, error) {
	targetX, targetY := vec2ToNullable(s.Target)
	finalSector, finalX, finalY, finalApproachKind, finalApproachID := courseToNullable(s.FinalTarget)
	attackKind, attackID := entityRefToNullable(s.AttackTarget)
	dockedKind, dockedID := entityRefToNullable(s.Docked)
	equipmentRaw, err := equipmentToJSON(s.Equipment)
	if err != nil {
		return 0, fmt.Errorf("encode ship equipment: %w", err)
	}
	var id int64
	err = r.exec.QueryRow(ctx, createSQL,
		int64(s.PlayerID), int16(s.Race), int64(s.SectorID),
		s.Pos.X, s.Pos.Y, s.Vel.X, s.Vel.Y,
		s.MaxSpeed, s.Acceleration, s.TurnRate, s.Direction.X, s.Direction.Y,
		targetX, targetY,
		finalSector, finalX, finalY,
		finalApproachKind, finalApproachID,
		s.HP, s.MaxHP, s.Shield, s.MaxShield, s.ShieldRecharge,
		s.Energy, s.MaxEnergy, s.EnergyRecharge,
		s.LaserDamage, s.LaserRange, s.LaserEnergyCost,
		attackKind, attackID,
		dockedKind, dockedID,
		s.IsSpacesuit, s.Name, int64(s.ShipClassID), equipmentRaw, s.RadarRange, s.IsOpen,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert ship: %w", err)
	}
	return domain.ShipID(id), nil
}

// equipmentRow is the JSONB shape stored in ships.equipment (phase 10.14).
// Kept repo-local so domain.InstalledEquipment stays tag-free.
type equipmentRow struct {
	EquipmentID int64  `json:"equipmentID"`
	Type        string `json:"type"`
	Level       int    `json:"level"`
}

// equipmentToJSON marshals the installed-equipment list for the JSONB column.
// An empty list becomes the literal '[]' (matching the column default).
func equipmentToJSON(eq []domain.InstalledEquipment) ([]byte, error) {
	if len(eq) == 0 {
		return []byte("[]"), nil
	}
	rows := make([]equipmentRow, len(eq))
	for i, m := range eq {
		rows[i] = equipmentRow{EquipmentID: int64(m.EquipmentID), Type: m.Type, Level: m.Level}
	}
	return json.Marshal(rows)
}

// equipmentFromJSON decodes the JSONB column back into the domain slice.
// NULL / empty / '[]' all yield a nil slice.
func equipmentFromJSON(raw []byte) ([]domain.InstalledEquipment, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []equipmentRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("unmarshal equipment: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]domain.InstalledEquipment, len(rows))
	for i, r := range rows {
		out[i] = domain.InstalledEquipment{
			EquipmentID: domain.EquipmentID(r.EquipmentID),
			Type:        r.Type,
			Level:       r.Level,
		}
	}
	return out, nil
}

const saveEquipmentSQL = `
UPDATE ships
SET
    equipment       = $2,
    max_speed       = $3,
    acceleration    = $4,
    max_shield      = $5,
    shield_recharge = $6,
    max_energy      = $7,
    energy_recharge = $8,
    laser_damage    = $9,
    radar_range     = $10,
    energy_delta    = $11,
    turn_rate       = $12,
    shield          = LEAST(shield, $5),
    energy          = LEAST(energy, $7),
    updated_at      = NOW()
WHERE id = $1
`

// SaveEquipment immediately persists a ship's installed-equipment list and the
// stat columns it folds into (phase 10.14/10.20): max_speed/acceleration,
// max_shield/shield_recharge, max_energy/energy_recharge, laser_damage,
// radar_range (up_scanner), energy_delta (per-tick equipment energy, 10.3.1),
// turn_rate (up_rudder manoeuvrability, 10.3.15).
// Current shield/energy are clamped down to the (possibly lowered) maxima so an
// uninstall cannot leave a pool above its cap. Caller passes a domain.Ship with
// those fields already recomputed (base class stats + equipment effects).
// Returns ErrShipNotFound when no row matches.
func (r *Repository) SaveEquipment(ctx context.Context, s domain.Ship) error {
	raw, err := equipmentToJSON(s.Equipment)
	if err != nil {
		return fmt.Errorf("encode ship equipment: %w", err)
	}
	tag, err := r.exec.Exec(ctx, saveEquipmentSQL,
		int64(s.ID), raw,
		s.MaxSpeed, s.Acceleration,
		s.MaxShield, s.ShieldRecharge,
		s.MaxEnergy, s.EnergyRecharge,
		s.LaserDamage, s.RadarRange,
		s.EnergyDelta, s.TurnRate,
	)
	if err != nil {
		return fmt.Errorf("update ship equipment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrShipNotFound
	}
	return nil
}

// vec2ToNullable splits an optional Vec2 into the two nullable doubles the
// schema wants. Returning (nil, nil) translates to SQL NULLs.
func vec2ToNullable(v *domain.Vec2) (*float64, *float64) {
	if v == nil {
		return nil, nil
	}
	x, y := v.X, v.Y
	return &x, &y
}

// courseToNullable splits an optional Course into the columns the schema
// stores: (sector_id, x, y, approach_kind, approach_id). All five are
// NULL when course is nil; approach_kind/approach_id are NULL when
// Course.Approach is nil. The two columns live under the historical
// names final_target_dock_kind / final_target_dock_id in SQL — phase
// 3.12 renamed only the Go field, see loadAllSQL.
func courseToNullable(c *domain.Course) (*int64, *float64, *float64, *int16, *int64) {
	if c == nil {
		return nil, nil, nil, nil, nil
	}
	sector := int64(c.Sector)
	x, y := c.Pos.X, c.Pos.Y
	approachKind, approachID := entityRefToNullable(c.Approach)
	return &sector, &x, &y, approachKind, approachID
}

// entityRefToNullable splits an optional EntityRef into (kind, id) nullable
// columns. Returning (nil, nil) translates to SQL NULLs and satisfies the
// all-or-none CHECK constraints.
func entityRefToNullable(ref *domain.EntityRef) (*int16, *int64) {
	if ref == nil {
		return nil, nil
	}
	kind := int16(ref.Kind)
	id := ref.ID
	return &kind, &id
}

const saveSQL = `
UPDATE ships
SET
    player_id              = $2,
    sector_id              = $3,
    pos_x                  = $4,
    pos_y                  = $5,
    vel_x                  = $6,
    vel_y                  = $7,
    direction_x            = $8,
    direction_y            = $9,
    target_x               = $10,
    target_y               = $11,
    final_target_sector    = $12,
    final_target_x         = $13,
    final_target_y         = $14,
    final_target_dock_kind = $15,
    final_target_dock_id   = $16,
    hp                     = $17,
    shield                 = $18,
    attack_kind            = $19,
    attack_id              = $20,
    docked_kind            = $21,
    docked_id              = $22,
    passengers             = $23,
    is_open                = $24,
    updated_at             = NOW()
WHERE id = $1
`

// Save writes a single ship row immediately. Used for critical events
// (creation, sector handoff, death, attack target change) that must be
// persisted before the next tick. Returns ErrShipNotFound if no row
// with that id exists.
//
// MaxSpeed / Acceleration / TurnRate / MaxHP / MaxShield /
// ShieldRecharge / MaxEnergy / EnergyRecharge / Laser* are class
// characteristics fixed at Create time and NOT updated here.
//
// AttackTarget is written: a player issuing /api/cmd/attack or
// /api/cmd/cease-fire is a critical event — losing the new value on a
// crash would surprise the player ("why did my ship stop shooting?").
// Energy is NOT written: it drifts every tick and the ±5s snapshot
// path is sufficient; an immediate write here would be wasted bandwidth.
func (r *Repository) Save(ctx context.Context, s domain.Ship) error {
	targetX, targetY := vec2ToNullable(s.Target)
	finalSector, finalX, finalY, finalApproachKind, finalApproachID := courseToNullable(s.FinalTarget)
	attackKind, attackID := entityRefToNullable(s.AttackTarget)
	dockedKind, dockedID := entityRefToNullable(s.Docked)
	tag, err := r.exec.Exec(ctx, saveSQL,
		int64(s.ID), int64(s.PlayerID), int64(s.SectorID),
		s.Pos.X, s.Pos.Y, s.Vel.X, s.Vel.Y, s.Direction.X, s.Direction.Y,
		targetX, targetY,
		finalSector, finalX, finalY,
		finalApproachKind, finalApproachID,
		s.HP, s.Shield,
		attackKind, attackID,
		dockedKind, dockedID,
		s.Passengers,
		s.IsOpen,
	)
	if err != nil {
		return fmt.Errorf("update ship: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrShipNotFound
	}
	return nil
}

const batchUpdateSQL = `
UPDATE ships AS s
SET
    final_target_sector    = u.final_sector,
    final_target_x         = u.final_x,
    final_target_y         = u.final_y,
    final_target_dock_kind = u.final_dock_kind,
    final_target_dock_id   = u.final_dock_id,
    hp                     = u.hp,
    shield                 = u.shield,
    updated_at             = NOW()
FROM unnest(
    $1::bigint[],
    $2::bigint[],
    $3::float8[],
    $4::float8[],
    $5::smallint[],
    $6::bigint[],
    $7::integer[],
    $8::integer[]
) AS u(id,
       final_sector, final_x, final_y, final_dock_kind, final_dock_id,
       hp, shield)
WHERE s.id = u.id
`

// BatchUpdate writes the periodic-snapshot fields (final target, hp,
// shield) of every passed ship in a single SQL statement. Empty input is
// a no-op. Missing rows are silently skipped — the worker can pass a
// dirty-set that races a delete; that is fine.
//
// Phase 3.19 (approach B): position/velocity/direction/target are NO
// longer written periodically. They are pure RAM physics that drift every
// tick, so a stale 0–5s snapshot is worthless and after a gate handoff it
// could clobber the new sector's coordinates. They are persisted only by
// immediate-event Save (create, dock, undock, jump, death) and by the
// worker's graceful-shutdown flush — see Worker.flushAll.
//
// docked_kind/docked_id are intentionally NOT batched either — docking
// transitions are immediate-write events handled by Save.
// max_speed/acceleration/turn_rate are class characteristics set at
// Create and never updated here.
func (r *Repository) BatchUpdate(ctx context.Context, ships []domain.Ship) error {
	if len(ships) == 0 {
		return nil
	}

	ids := make([]int64, len(ships))
	finalSector := make([]*int64, len(ships))
	finalX := make([]*float64, len(ships))
	finalY := make([]*float64, len(ships))
	finalApproachKind := make([]*int16, len(ships))
	finalApproachID := make([]*int64, len(ships))
	hp := make([]int32, len(ships))
	shield := make([]int32, len(ships))

	for i, s := range ships {
		ids[i] = int64(s.ID)
		finalSector[i], finalX[i], finalY[i], finalApproachKind[i], finalApproachID[i] = courseToNullable(s.FinalTarget)
		hp[i] = int32(s.HP)
		shield[i] = int32(s.Shield)
	}

	if _, err := r.exec.Exec(ctx, batchUpdateSQL,
		ids,
		finalSector, finalX, finalY,
		finalApproachKind, finalApproachID,
		hp, shield,
	); err != nil {
		return fmt.Errorf("batch update ships: %w", err)
	}
	return nil
}

const deleteSQL = `DELETE FROM ships WHERE id = $1`

// Delete removes a ship row. Missing rows return ErrShipNotFound so callers
// can distinguish a race (already gone) from a real success.
func (r *Repository) Delete(ctx context.Context, id domain.ShipID) error {
	tag, err := r.exec.Exec(ctx, deleteSQL, int64(id))
	if err != nil {
		return fmt.Errorf("delete ship: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrShipNotFound
	}
	return nil
}

const countByRaceSQL = `SELECT race, COUNT(*) FROM ships GROUP BY race`

// CountByRace returns how many ships currently belong to each race. The
// invasion spawner (phase 9.5) reads it to size active Xenon (7) / Kha'ak (8)
// waves against their caps — a killed wave deletes its rows, so the count
// drops and frees the limit without any extra bookkeeping.
func (r *Repository) CountByRace(ctx context.Context) (map[domain.RaceID]int, error) {
	rows, err := r.exec.Query(ctx, countByRaceSQL)
	if err != nil {
		return nil, fmt.Errorf("count ships by race: %w", err)
	}
	defer rows.Close()

	out := make(map[domain.RaceID]int)
	for rows.Next() {
		var (
			race  int16
			count int
		)
		if err := rows.Scan(&race, &count); err != nil {
			return nil, fmt.Errorf("scan ships-by-race count: %w", err)
		}
		out[domain.RaceID(race)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ships-by-race count: %w", err)
	}
	return out, nil
}
