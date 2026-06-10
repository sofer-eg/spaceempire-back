package ships_test

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/ships"
	"spaceempire/back/internal/pkg/database/testdb"
)

const insertShipSQL = `
INSERT INTO ships (id, player_id, race, sector_id, pos_x, pos_y, vel_x, vel_y, target_x, target_y, hp, shield)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
`

func seedPlayer(t *testing.T, pool *pgxpool.Pool) domain.PlayerID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ('p', 'h') RETURNING id`).Scan(&id)
	require.NoError(t, err)
	return domain.PlayerID(id)
}

func insertShip(t *testing.T, pool *pgxpool.Pool, s domain.Ship) {
	t.Helper()
	var tx, ty *float64
	if s.Target != nil {
		x, y := s.Target.X, s.Target.Y
		tx, ty = &x, &y
	}
	_, err := pool.Exec(context.Background(), insertShipSQL,
		int64(s.ID), int64(s.PlayerID), int16(s.Race), int64(s.SectorID),
		s.Pos.X, s.Pos.Y, s.Vel.X, s.Vel.Y, tx, ty, s.HP, s.Shield,
	)
	require.NoError(t, err)
}

func TestIntegration_Ships_Create_RoundTripsSpacesuit(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	repo := ships.New(pool)
	ctx := context.Background()

	suitID, err := repo.Create(ctx, domain.Ship{
		PlayerID: pid, SectorID: domain.SectorID(7), Pos: domain.Vec2{X: 1, Y: 2},
		HP: 30, MaxHP: 30, LaserDamage: 5, IsSpacesuit: true,
	})
	require.NoError(t, err)
	normalID, err := repo.Create(ctx, domain.Ship{
		PlayerID: pid, Race: 1, Name: "Разведчик", SectorID: domain.SectorID(7), Pos: domain.Vec2{X: 3, Y: 4},
		HP: 100, MaxHP: 100,
	})
	require.NoError(t, err)

	got, err := repo.LoadAll(ctx, domain.SectorID(7))
	require.NoError(t, err)
	flags := map[domain.ShipID]bool{}
	names := map[domain.ShipID]string{}
	for _, s := range got {
		flags[s.ID] = s.IsSpacesuit
		names[s.ID] = s.Name
	}
	assert.True(t, flags[suitID], "spacesuit flag round-trips")
	assert.False(t, flags[normalID], "normal ship is not a spacesuit")
	// Phase 10.10: ships.name round-trips through Create→LoadAll.
	assert.Equal(t, "Разведчик", names[normalID], "ship name round-trips")
	assert.Equal(t, "", names[suitID], "unnamed ship loads empty name")
}

func TestIntegration_Ships_Create_RoundTripsIsOpen(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	repo := ships.New(pool)
	ctx := context.Background()

	openID, err := repo.Create(ctx, domain.Ship{
		PlayerID: pid, SectorID: domain.SectorID(8), Pos: domain.Vec2{X: 1, Y: 1}, IsOpen: true,
	})
	require.NoError(t, err)
	closedID, err := repo.Create(ctx, domain.Ship{
		PlayerID: pid, SectorID: domain.SectorID(8), Pos: domain.Vec2{X: 2, Y: 2},
	})
	require.NoError(t, err)

	got, err := repo.LoadAll(ctx, domain.SectorID(8))
	require.NoError(t, err)
	open := map[domain.ShipID]bool{}
	for _, s := range got {
		open[s.ID] = s.IsOpen
	}
	assert.True(t, open[openID], "is_open=true round-trips")
	assert.False(t, open[closedID], "default is_open=false")
}

func TestIntegration_Ships_LoadAll_FiltersBySector(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	target := domain.Vec2{X: 50, Y: 60}
	insertShip(t, pool, domain.Ship{
		ID: 1, PlayerID: pid, Race: 6, SectorID: 10,
		Pos: domain.Vec2{X: 1, Y: 2}, Vel: domain.Vec2{X: 3, Y: 4},
		Target: &target,
		HP:     90, Shield: 80,
	})
	insertShip(t, pool, domain.Ship{
		ID: 2, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 5, Y: 6},
		HP:  100, Shield: 100,
	})
	insertShip(t, pool, domain.Ship{
		ID: 3, PlayerID: pid, SectorID: 99,
		Pos: domain.Vec2{X: 7, Y: 8},
		HP:  100, Shield: 100,
	})

	repo := ships.New(pool)

	got, err := repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, got, 2)
	sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })

	assert.Equal(t, domain.ShipID(1), got[0].ID)
	assert.Equal(t, pid, got[0].PlayerID)
	assert.Equal(t, domain.RaceID(6), got[0].Race, "race round-trips (9.1)")
	assert.Equal(t, domain.SectorID(10), got[0].SectorID)
	assert.Equal(t, domain.Vec2{X: 1, Y: 2}, got[0].Pos)
	assert.Equal(t, domain.Vec2{X: 3, Y: 4}, got[0].Vel)
	require.NotNil(t, got[0].Target)
	assert.Equal(t, domain.Vec2{X: 50, Y: 60}, *got[0].Target)
	assert.Equal(t, 90, got[0].HP)
	assert.Equal(t, 80, got[0].Shield)

	assert.Equal(t, domain.ShipID(2), got[1].ID)
	assert.Nil(t, got[1].Target)
}

func TestIntegration_Ships_LoadAll_EmptySectorReturnsNil(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := ships.New(pool)

	got, err := repo.LoadAll(context.Background(), domain.SectorID(1))
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestIntegration_Ships_Save_UpdatesRow(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
	})

	repo := ships.New(pool)
	target := domain.Vec2{X: 99, Y: 99}
	err := repo.Save(context.Background(), domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 20,
		Pos: domain.Vec2{X: 5, Y: 7}, Vel: domain.Vec2{X: 1, Y: 0},
		Target: &target,
		HP:     50, Shield: 25,
	})
	require.NoError(t, err)

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(20))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, domain.SectorID(20), loaded[0].SectorID)
	assert.Equal(t, domain.Vec2{X: 5, Y: 7}, loaded[0].Pos)
	assert.Equal(t, 50, loaded[0].HP)
	require.NotNil(t, loaded[0].Target)
	assert.Equal(t, 99.0, loaded[0].Target.X)
}

func TestIntegration_Ships_Save_MissingReturnsErrShipNotFound(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := ships.New(pool)

	err := repo.Save(context.Background(), domain.Ship{
		ID: 42, SectorID: 1, HP: 100, Shield: 100,
	})
	assert.True(t, errors.Is(err, ships.ErrShipNotFound), "err = %v", err)
}

func TestIntegration_Ships_BatchUpdate_WritesSelectedFields(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{ID: 1, PlayerID: pid, SectorID: 10, Pos: domain.Vec2{X: 1, Y: 2}, HP: 100, Shield: 100})
	insertShip(t, pool, domain.Ship{ID: 2, PlayerID: pid, SectorID: 10, Pos: domain.Vec2{X: 3, Y: 4}, HP: 100, Shield: 100})
	insertShip(t, pool, domain.Ship{ID: 3, PlayerID: pid, SectorID: 10, Pos: domain.Vec2{X: 5, Y: 6}, HP: 100, Shield: 100})

	repo := ships.New(pool)
	target := domain.Vec2{X: 33, Y: 44}
	batch := []domain.Ship{
		{ID: 1, SectorID: 10, Pos: domain.Vec2{X: 10, Y: 11}, Vel: domain.Vec2{X: 1, Y: 0}, HP: 80, Shield: 70},
		{ID: 2, SectorID: 10, Pos: domain.Vec2{X: 20, Y: 22}, Vel: domain.Vec2{X: 0, Y: 1}, Target: &target, HP: 60, Shield: 50},
	}
	require.NoError(t, repo.BatchUpdate(context.Background(), batch))

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, loaded, 3)
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].ID < loaded[j].ID })

	// Phase 3.19 (approach B): hp/shield are written periodically, but
	// position/velocity/target are NOT — they stay at the seeded values
	// regardless of what the batch carried.
	assert.Equal(t, 80, loaded[0].HP)
	assert.Equal(t, 70, loaded[0].Shield)
	assert.Equal(t, domain.Vec2{X: 1, Y: 2}, loaded[0].Pos, "position must not be batched")

	assert.Equal(t, 60, loaded[1].HP)
	assert.Equal(t, 50, loaded[1].Shield)
	assert.Equal(t, domain.Vec2{X: 3, Y: 4}, loaded[1].Pos, "position must not be batched")
	assert.Nil(t, loaded[1].Target, "target must not be batched")

	// Ship 3 was not in the batch — must keep its original state.
	assert.Equal(t, domain.Vec2{X: 5, Y: 6}, loaded[2].Pos)
	assert.Equal(t, 100, loaded[2].HP)
}

func TestIntegration_Ships_BatchUpdate_EmptyNoop(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := ships.New(pool)

	require.NoError(t, repo.BatchUpdate(context.Background(), nil))
	require.NoError(t, repo.BatchUpdate(context.Background(), []domain.Ship{}))
}

func TestIntegration_Ships_Delete_RemovesRow(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{ID: 1, PlayerID: pid, SectorID: 5, HP: 100, Shield: 100})

	repo := ships.New(pool)
	require.NoError(t, repo.Delete(context.Background(), domain.ShipID(1)))

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(5))
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

func TestIntegration_Ships_Save_PersistsFinalTarget(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
	})

	repo := ships.New(pool)
	course := domain.Course{Sector: 42, Pos: domain.Vec2{X: 11, Y: 22}}
	require.NoError(t, repo.Save(context.Background(), domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
		FinalTarget: &course,
	}))

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.NotNil(t, loaded[0].FinalTarget)
	assert.Equal(t, domain.SectorID(42), loaded[0].FinalTarget.Sector)
	assert.Equal(t, domain.Vec2{X: 11, Y: 22}, loaded[0].FinalTarget.Pos)

	// Clearing FinalTarget round-trips as NULL.
	require.NoError(t, repo.Save(context.Background(), domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
		FinalTarget: nil,
	}))
	loaded, err = repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Nil(t, loaded[0].FinalTarget)
}

func TestIntegration_Ships_BatchUpdate_RoundtripsFinalTarget(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
	})
	insertShip(t, pool, domain.Ship{
		ID: 2, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
	})

	repo := ships.New(pool)
	require.NoError(t, repo.BatchUpdate(context.Background(), []domain.Ship{
		{ID: 1, SectorID: 10, HP: 100, Shield: 100,
			FinalTarget: &domain.Course{Sector: 5, Pos: domain.Vec2{X: 1, Y: 2}}},
		{ID: 2, SectorID: 10, HP: 100, Shield: 100, FinalTarget: nil},
	}))

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].ID < loaded[j].ID })
	require.Len(t, loaded, 2)
	require.NotNil(t, loaded[0].FinalTarget)
	assert.Equal(t, domain.SectorID(5), loaded[0].FinalTarget.Sector)
	assert.Nil(t, loaded[1].FinalTarget)
}

func TestIntegration_Ships_Save_PersistsDocked(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
	})

	repo := ships.New(pool)
	dock := domain.EntityRef{Kind: domain.EntityKindStation, ID: 42}
	require.NoError(t, repo.Save(context.Background(), domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
		Docked: &dock,
	}))

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.NotNil(t, loaded[0].Docked)
	assert.Equal(t, dock, *loaded[0].Docked)

	// Clearing Docked round-trips as NULL.
	require.NoError(t, repo.Save(context.Background(), domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
		Docked: nil,
	}))
	loaded, err = repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Nil(t, loaded[0].Docked)
}

func TestIntegration_Ships_Save_PersistsCourseApproach(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
	})

	repo := ships.New(pool)
	approach := domain.EntityRef{Kind: domain.EntityKindShipyard, ID: 99}
	require.NoError(t, repo.Save(context.Background(), domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
		FinalTarget: &domain.Course{
			Sector:   5,
			Pos:      domain.Vec2{X: 1, Y: 2},
			Approach: &approach,
		},
	}))

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.NotNil(t, loaded[0].FinalTarget)
	require.NotNil(t, loaded[0].FinalTarget.Approach)
	assert.Equal(t, approach, *loaded[0].FinalTarget.Approach)
}

func TestIntegration_Ships_BatchUpdate_RoundtripsCourseApproach(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	insertShip(t, pool, domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 10,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Shield: 100,
	})

	repo := ships.New(pool)
	approach := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 7}
	require.NoError(t, repo.BatchUpdate(context.Background(), []domain.Ship{
		{ID: 1, SectorID: 10, HP: 100, Shield: 100,
			FinalTarget: &domain.Course{
				Sector: 3, Pos: domain.Vec2{X: 1, Y: 1}, Approach: &approach,
			}},
	}))

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(10))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.NotNil(t, loaded[0].FinalTarget)
	require.NotNil(t, loaded[0].FinalTarget.Approach)
	assert.Equal(t, approach, *loaded[0].FinalTarget.Approach)
}

func TestIntegration_Ships_Equipment_RoundTrips(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayer(t, pool)
	repo := ships.New(pool)
	ctx := context.Background()

	// Create with an initial fit — it must survive Create→LoadAll.
	id, err := repo.Create(ctx, domain.Ship{
		PlayerID: pid, SectorID: domain.SectorID(7), Pos: domain.Vec2{X: 1, Y: 2},
		ShipClassID: 73, HP: 100, MaxHP: 100, MaxShield: 1000, MaxSpeed: 30, RadarRange: 3500,
		Equipment: []domain.InstalledEquipment{
			{EquipmentID: 11, Type: "up_generator", Level: 1},
			{EquipmentID: 10, Type: "up_accumulator", Level: 1},
		},
	})
	require.NoError(t, err)

	// A bare ship (no equipment) loads as a nil slice.
	bareID, err := repo.Create(ctx, domain.Ship{
		PlayerID: pid, SectorID: domain.SectorID(7), Pos: domain.Vec2{X: 3, Y: 4},
		HP: 100, MaxHP: 100,
	})
	require.NoError(t, err)

	loaded, err := repo.LoadAll(ctx, domain.SectorID(7))
	require.NoError(t, err)
	byID := map[domain.ShipID]domain.Ship{}
	for _, s := range loaded {
		byID[s.ID] = s
	}
	require.Len(t, byID[id].Equipment, 2, "create round-trips the fit")
	assert.Equal(t, "up_generator", byID[id].Equipment[0].Type)
	assert.Equal(t, domain.EquipmentID(10), byID[id].Equipment[1].EquipmentID)
	assert.InDelta(t, 3500.0, byID[id].RadarRange, 0.001, "create round-trips radar_range (10.20)")
	assert.Nil(t, byID[bareID].Equipment, "bare ship loads nil equipment")

	// SaveEquipment persists a new fit + folded stat columns and clamps the
	// current shield/energy down to the new maxima.
	require.NoError(t, repo.SaveEquipment(ctx, domain.Ship{
		ID: id,
		Equipment: []domain.InstalledEquipment{
			{EquipmentID: 11, Type: "up_generator", Level: 1},
			{EquipmentID: 10, Type: "up_accumulator", Level: 1},
			{EquipmentID: 12, Type: "up_shield", Level: 2},
		},
		MaxSpeed: 33, Acceleration: 5, MaxShield: 1300, ShieldRecharge: 120,
		MaxEnergy: 250, EnergyRecharge: 5, LaserDamage: 40, RadarRange: 4900,
	}))

	loaded, err = repo.LoadAll(ctx, domain.SectorID(7))
	require.NoError(t, err)
	for _, s := range loaded {
		if s.ID == id {
			require.Len(t, s.Equipment, 3, "save round-trips the new fit")
			assert.Equal(t, "up_shield", s.Equipment[2].Type)
			assert.Equal(t, 2, s.Equipment[2].Level)
			assert.Equal(t, 1300, s.MaxShield, "folded stat persisted")
			assert.Equal(t, 250, s.MaxEnergy)
			assert.InDelta(t, 33.0, s.MaxSpeed, 0.001)
			assert.InDelta(t, 4900.0, s.RadarRange, 0.001, "up_scanner radar persisted via SaveEquipment (10.20 L3)")
		}
	}

	// Removing all modules round-trips as a nil slice.
	require.NoError(t, repo.SaveEquipment(ctx, domain.Ship{
		ID: id, MaxSpeed: 30, MaxShield: 1000, MaxEnergy: 200,
	}))
	loaded, err = repo.LoadAll(ctx, domain.SectorID(7))
	require.NoError(t, err)
	for _, s := range loaded {
		if s.ID == id {
			assert.Nil(t, s.Equipment, "emptied fit loads as nil")
		}
	}
}

func TestIntegration_Ships_SaveEquipment_MissingReturnsErrShipNotFound(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := ships.New(pool)

	err := repo.SaveEquipment(context.Background(), domain.Ship{ID: 777})
	assert.True(t, errors.Is(err, ships.ErrShipNotFound), "err = %v", err)
}

func TestIntegration_Ships_Delete_MissingReturnsErrShipNotFound(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := ships.New(pool)

	err := repo.Delete(context.Background(), domain.ShipID(999))
	assert.True(t, errors.Is(err, ships.ErrShipNotFound), "err = %v", err)
}
