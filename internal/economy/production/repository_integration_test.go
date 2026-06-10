package production_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/production"
	stationsrepo "spaceempire/back/internal/persistence/stations"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
)

// TestIntegration_Production_HundredTicksAgainstRealPostgres mirrors the
// task 3.6 acceptance criterion: 100 ticks at cycle_time = 1s burn through the
// input stock and replenish the output stock. Production now runs on the
// station market (station_goods), so inputs are buy rows the cycle drains and
// the output is a sell row it fills.
func TestIntegration_Production_HundredTicksAgainstRealPostgres(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	ctx := context.Background()

	stationID := domain.StationID(1)
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(stationID)}

	const (
		startIron    = 1000
		startSilicon = 200
		startChip    = 0
		ticks        = 100
		cycleSeconds = 1
	)

	// Turn station 1 into a type-1 factory and give it a clean market: buy rows
	// for the two inputs, a sell row for the output. Replaces whatever the
	// 0042 seed laid down for this station's original type.
	_, err := pool.Exec(ctx,
		`UPDATE stations SET type = 1, in_progress = false, next_cycle_at = NULL WHERE id = $1`,
		int64(stationID))
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`DELETE FROM station_goods WHERE owner_kind = $1 AND owner_id = $2`,
		int16(domain.EntityKindStation), int64(stationID))
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO station_goods (owner_kind, owner_id, goods_type_id, buy_price, sell_price, stock, max_stock) VALUES
		    ($1, $2, 2, 1,    NULL, $3, 1000000),
		    ($1, $2, 3, 1,    NULL, $4, 1000000),
		    ($1, $2, 7, NULL, 1,    $5, 100000)`,
		int16(domain.EntityKindStation), int64(stationID), int64(startIron), int64(startSilicon), int64(startChip))
	require.NoError(t, err)

	goods := []balance.Goods{
		{ID: 2, Name: "Iron", Space: 1},
		{ID: 3, Name: "Silicon", Space: 1},
		{ID: 7, Name: "Microchips", Space: 1},
	}
	recipes := []balance.Recipe{{
		StationType: 1,
		CycleTime:   time.Duration(cycleSeconds) * time.Second,
		Inputs: []balance.RecipeLine{
			{GoodsType: 2, Quantity: 5},
			{GoodsType: 3, Quantity: 1},
		},
		Outputs: []balance.RecipeLine{
			{GoodsType: 7, Quantity: 3, Max: 100_000},
		},
	}}
	bal, err := balance.New(goods, recipes)
	require.NoError(t, err)

	tm := database.NewTxManager(pool)
	tradeRepo := traderepo.New(pool)
	stationsRepoInst := stationsrepo.New(pool)
	svc, err := production.New(bal, production.NewRepoTxRunner(tm, tradeRepo, stationsRepoInst))
	require.NoError(t, err)

	work := []domain.Station{{ID: stationID, Type: 1, Built: true}}

	// Each iteration advances the clock by cycleSeconds, so every second tick
	// produces an output (one tick starts a cycle, the next finishes it). With
	// 100 ticks ⇒ ~50 cycles completed.
	totalCycles := 0
	base := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	for i := 0; i < ticks; i++ {
		now := base.Add(time.Duration(i*cycleSeconds) * time.Second)
		cycles, err := svc.Tick(ctx, work, now)
		require.NoError(t, err)
		totalCycles += cycles
	}

	expectedCycles := ticks / 2
	assert.Equal(t, expectedCycles, totalCycles, "expected ~%d cycles in %d ticks", expectedCycles, ticks)

	entries, err := tradeRepo.ListMarket(ctx, stationRef)
	require.NoError(t, err)
	got := make(map[domain.GoodsTypeID]int64, len(entries))
	for _, e := range entries {
		got[e.GoodsType] = e.Stock
	}
	assert.EqualValues(t, startIron-expectedCycles*5, got[2], "iron buy stock drained")
	assert.EqualValues(t, startSilicon-expectedCycles*1, got[3], "silicon buy stock drained")
	assert.EqualValues(t, startChip+expectedCycles*3, got[7], "microchip sell stock filled")

	var inProgress bool
	var nextCycleAt *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT in_progress, next_cycle_at FROM stations WHERE id = $1`, int64(stationID)).
		Scan(&inProgress, &nextCycleAt))
	// The last tick of an even tick count may start a cycle that has not
	// completed yet, so we accept either state.
	if inProgress {
		require.NotNil(t, nextCycleAt)
	}
}
