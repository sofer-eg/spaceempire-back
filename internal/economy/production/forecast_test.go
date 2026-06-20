package production_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/production"
	traderepo "spaceempire/back/internal/persistence/trade"
)

// microchip recipe (buildBalance): in iron×5 + silicon×1 → out microchip×3
// (max 100), one second per cycle. stationType==1.

func mkt(g domain.GoodsTypeID, stock, max int64) traderepo.MarketEntry {
	return traderepo.MarketEntry{GoodsType: g, Stock: stock, MaxStock: max}
}

// TestUnit_StationForecast_FullRun proves a station with ample inputs and output
// room runs every requested cycle and projects the resulting stock.
func TestUnit_StationForecast_FullRun(t *testing.T) {
	t.Parallel()
	reader := production.NewReader(buildBalance(t), stubStore{})
	st := domain.Station{ID: 7, Type: stationType}
	market := []traderepo.MarketEntry{
		mkt(iron, 100, 200), mkt(silicon, 100, 200), mkt(microchip, 0, 100),
	}

	projected, completed, ok := reader.StationForecast(st, market, 5)

	require.True(t, ok)
	assert.Equal(t, 5, completed, "all five cycles run")
	assert.EqualValues(t, 75, projected[iron], "iron 100 - 5*5")
	assert.EqualValues(t, 95, projected[silicon], "silicon 100 - 5*1")
	assert.EqualValues(t, 15, projected[microchip], "microchip 0 + 5*3")
}

// TestUnit_StationForecast_InputStarvation proves the simulation stops the
// moment an input runs short — the deterministic starvation guard (AC #2).
func TestUnit_StationForecast_InputStarvation(t *testing.T) {
	t.Parallel()
	reader := production.NewReader(buildBalance(t), stubStore{})
	st := domain.Station{ID: 7, Type: stationType}
	// Iron 12 covers only two cycles (5 each); the third starves.
	market := []traderepo.MarketEntry{
		mkt(iron, 12, 200), mkt(silicon, 100, 200), mkt(microchip, 0, 100),
	}

	projected, completed, ok := reader.StationForecast(st, market, 5)

	require.True(t, ok)
	assert.Equal(t, 2, completed, "iron runs out after two cycles")
	assert.EqualValues(t, 2, projected[iron], "12 - 2*5")
	assert.EqualValues(t, 6, projected[microchip], "2 cycles * 3")
}

// TestUnit_StationForecast_OutputCap proves a full output shelf halts production
// (the cycle would breach max_stock).
func TestUnit_StationForecast_OutputCap(t *testing.T) {
	t.Parallel()
	reader := production.NewReader(buildBalance(t), stubStore{})
	st := domain.Station{ID: 7, Type: stationType}
	// microchip 97/100 takes exactly one more cycle (→100); the next would be 103.
	market := []traderepo.MarketEntry{
		mkt(iron, 100, 200), mkt(silicon, 100, 200), mkt(microchip, 97, 100),
	}

	projected, completed, ok := reader.StationForecast(st, market, 5)

	require.True(t, ok)
	assert.Equal(t, 1, completed, "only one cycle fits before the shelf is full")
	assert.EqualValues(t, 100, projected[microchip])
}

// TestUnit_StationForecast_NoRecipe proves a station type without a recipe
// (trade station / pirbase) yields ok=false so the caller omits the forecast.
func TestUnit_StationForecast_NoRecipe(t *testing.T) {
	t.Parallel()
	reader := production.NewReader(buildBalance(t), stubStore{})
	st := domain.Station{ID: 7, Type: 999}

	_, _, ok := reader.StationForecast(st, nil, 5)

	assert.False(t, ok)
}
