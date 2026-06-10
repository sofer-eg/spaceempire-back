package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	shipsrepo "spaceempire/back/internal/persistence/ships"
	worldrepo "spaceempire/back/internal/persistence/world"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// jumpSectorA / jumpSectorB are a neighbouring sector pair linked by a gate
// in the seeded map. The gate's exit coordinates are read from the DB at
// runtime (loadTopologyFromDB) rather than hard-coded: the StarWind map import
// (migrations 0014/0034/0036) moved the gates off the old (±900,0) stubs.
const (
	jumpSectorA = domain.SectorID(1)
	jumpSectorB = domain.SectorID(2)
)

func seedPlayerForJump(t *testing.T, pool *pgxpool.Pool) domain.PlayerID {
	t.Helper()
	var pid int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ('jumper', 'h') RETURNING id`).Scan(&pid)
	require.NoError(t, err)
	return domain.PlayerID(pid)
}

func seedShipAtGate(t *testing.T, pool *pgxpool.Pool, id domain.ShipID, pid domain.PlayerID) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO ships (id, player_id, sector_id, pos_x, pos_y, hp, shield)
		 VALUES ($1, $2, $3, 900, 0, 100, 100)`,
		int64(id), int64(pid), int64(jumpSectorA))
	require.NoError(t, err)
}

// loadTopologyFromDB returns the topology, the id of the gate linking
// jumpSectorA↔jumpSectorB, and that gate's exit coordinates on each sector's
// side (exitA on A's side, exitB on B's side). The exits are read from the DB
// rather than hard-coded because the StarWind map import moved the gates off
// the old (±900,0) stubs.
func loadTopologyFromDB(t *testing.T, pool *pgxpool.Pool) (*world.Topology, domain.GateID, domain.Vec2, domain.Vec2) {
	t.Helper()
	sectors, gates, err := worldrepo.New(pool).LoadAll(context.Background())
	require.NoError(t, err)
	topo := world.New(sectors, gates)
	for _, g := range gates {
		switch {
		case g.SectorA == jumpSectorA && g.SectorB == jumpSectorB:
			return topo, g.ID, g.PosA, g.PosB
		case g.SectorA == jumpSectorB && g.SectorB == jumpSectorA:
			return topo, g.ID, g.PosB, g.PosA
		}
	}
	t.Fatalf("gate %d↔%d not found in seed", jumpSectorA, jumpSectorB)
	return nil, 0, domain.Vec2{}, domain.Vec2{}
}

// TestIntegration_Sector_Handoff_PlayerSeesShipMoveAcrossSectors is the
// acceptance test for phase 2.4: a player's ship jumps A→B; one sector
// snapshot loses it, the next gains it, and the DB ends up with the new
// sector_id.
func TestIntegration_Sector_Handoff_PlayerSeesShipMoveAcrossSectors(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	pid := seedPlayerForJump(t, pool)
	seedShipAtGate(t, pool, 1, pid)

	topo, gateID, exitA, exitB := loadTopologyFromDB(t, pool)
	shipRepo := shipsrepo.New(pool)
	b := bus.NewInMemory(64)

	// Pool with 2 workers, so A and B end up on different goroutines —
	// exactly the cross-process boundary the bus simulates.
	p := sector.NewPool(
		sector.PoolConfig{WorkersCount: 2, Worker: sector.Config{
			TickInterval: 50 * time.Millisecond,
			GateRange:    50,
		}},
		[]domain.SectorID{jumpSectorA, jumpSectorB},
		clock.NewRealClock(),
		shipRepo,
		nil,
		map[domain.SectorID][]domain.Ship{
			jumpSectorA: {{
				ID: 1, PlayerID: pid, SectorID: jumpSectorA,
				Pos: exitA, HP: 100, Shield: 100,
			}},
			jumpSectorB: {},
		},
		sector.WithHandoff(topo, b),
	)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p.Start(ctx)
	go func() { _ = p.Run(ctx) }()

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, p.Send(jumpSectorA, sector.JumpCommand{
		PlayerID: pid, ShipID: 1, GateID: gateID, Reply: reply,
	}))

	select {
	case res := <-reply:
		require.NoError(t, res.Err)
	case <-time.After(2 * time.Second):
		t.Fatal("JumpCommand reply timeout")
	}

	require.Eventually(t, func() bool {
		return len(p.Snapshot(jumpSectorA).Ships) == 0 &&
			len(p.Snapshot(jumpSectorB).Ships) == 1
	}, 3*time.Second, 20*time.Millisecond, "handoff did not complete in time")

	got := p.Snapshot(jumpSectorB).Ships[0]
	assert.Equal(t, jumpSectorB, got.SectorID)
	assert.Equal(t, exitB, got.Pos)

	// Verify DB state matches RAM.
	rows, err := shipRepo.LoadAll(ctx, jumpSectorB)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, jumpSectorB, rows[0].SectorID)
	assert.Equal(t, exitB, rows[0].Pos)

	emptyA, err := shipRepo.LoadAll(ctx, jumpSectorA)
	require.NoError(t, err)
	assert.Empty(t, emptyA, "no ship rows must remain in sector A in the DB")

	stats := p.Stats().HandoffsTotal()
	assert.Equal(t, uint64(1), stats[sector.HandoffEdge{From: jumpSectorA, To: jumpSectorB}])
}
