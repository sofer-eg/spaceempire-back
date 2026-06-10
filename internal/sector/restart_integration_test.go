package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	shipsrepo "spaceempire/back/internal/persistence/ships"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/sector"
)

func seedShipRow(t *testing.T, pool *pgxpool.Pool, id, sectorID int64) {
	t.Helper()
	var pid int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ('sofer', 'h') RETURNING id`).Scan(&pid)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(),
		`INSERT INTO ships (id, player_id, sector_id, pos_x, pos_y, hp, shield)
		 VALUES ($1, $2, $3, 0, 0, 100, 100)`, id, pid, sectorID)
	require.NoError(t, err)
}

// TestIntegration_Sector_RestartRecoversPosition is the acceptance test for
// phase 3.19 (approach B): a moving ship's position is NOT written by the
// periodic snapshot, but IS persisted by the worker's graceful-shutdown
// flush, so the next cold start restores the post-movement position.
func TestIntegration_Sector_RestartRecoversPosition(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	seedShipRow(t, pool, 1, 7)
	repo := shipsrepo.New(pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial, err := repo.LoadAll(ctx, domain.SectorID(7))
	require.NoError(t, err)
	require.Len(t, initial, 1)
	initial[0].MaxSpeed = 10

	// Short tick + snapshot intervals so the periodic BatchUpdate fires
	// several times while the ship is in flight — proving it does not write
	// position under approach B.
	w := sector.NewWorker(
		0,
		sector.Config{
			TickInterval:     time.Millisecond,
			SnapshotInterval: 5 * time.Millisecond,
			InboxCapacity:    1024,
			ShutdownTimeout:  5 * time.Second,
		},
		clock.NewRealClock(),
		repo,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(7): initial},
	)

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(ctx)
	}()

	require.NoError(t, w.Send(domain.SectorID(7), sector.MoveCommand{
		PlayerID: initial[0].PlayerID,
		ShipID:   1,
		Target:   domain.Vec2{X: 1e6, Y: 0},
	}))

	// Wait until the ship has actually moved in RAM.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Snapshot(domain.SectorID(7)).Ships[0].Pos.X > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	live := w.Snapshot(domain.SectorID(7)).Ships[0]
	require.Positive(t, live.Pos.X, "ship should have moved in RAM")

	// While the worker is still running, the periodic snapshot has fired but
	// must NOT have persisted the moving position (approach B).
	midRun, err := repo.LoadAll(ctx, domain.SectorID(7))
	require.NoError(t, err)
	require.Len(t, midRun, 1)
	assert.Equal(t, 0.0, midRun[0].Pos.X,
		"periodic snapshot must not persist position (approach B)")

	// Graceful shutdown: cancel triggers flushAll, which checkpoints the live
	// position. Cold-start LoadAll must now see the post-movement coordinate.
	cancel()
	<-runDone

	loaded, err := repo.LoadAll(context.Background(), domain.SectorID(7))
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Positive(t, loaded[0].Pos.X,
		"graceful shutdown flush must persist post-movement position")
	assert.Equal(t, domain.ShipID(1), loaded[0].ID)
}

func TestIntegration_Sector_RestartWithoutDirtyDoesNotWrite(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	seedShipRow(t, pool, 1, 8)
	repo := shipsrepo.New(pool)

	ctx := context.Background()
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	initial, err := repo.LoadAll(ctx, domain.SectorID(8))
	require.NoError(t, err)
	initial[0].Pos = domain.Vec2{X: 42, Y: 42}
	initial[0].MaxSpeed = 1

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, SnapshotInterval: time.Second},
		clk,
		repo,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(8): initial},
	)

	// No MoveCommand sent → no dirty → no DB write expected.
	for i := 0; i < 5; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
	}

	reloaded, err := repo.LoadAll(ctx, domain.SectorID(8))
	require.NoError(t, err)
	require.Len(t, reloaded, 1)
	// Stored pos must still be the original (0,0) — in-memory (42,42) was
	// never marked dirty so it has not made it to the DB.
	assert.Equal(t, 0.0, reloaded[0].Pos.X)
}
