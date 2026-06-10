package combat_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

const testMissileType domain.GoodsTypeID = 50

// queueRNG returns the queued values in order; one Float64 call per
// missile stack. Panics if drained — a regular-cargo stack must never
// consume a roll.
type queueRNG struct {
	vals []float64
	i    int
}

func (r *queueRNG) Float64() float64 {
	if r.i >= len(r.vals) {
		panic("queueRNG drained: rolled more times than expected")
	}
	v := r.vals[r.i]
	r.i++
	return v
}

func TestUnit_PlanSlavesDrop_FloorOfRandomShare(t *testing.T) {
	t.Parallel()
	// pct = 20 (Float64 0) → floor(10 * 20 / 100) = 2.
	got, ok := combat.PlanSlavesDrop(10, &queueRNG{vals: []float64{0}})
	assert.True(t, ok)
	assert.EqualValues(t, 2, got)

	// pct = 50 (Float64 ~1) → floor(10 * 50 / 100) = 5.
	got, ok = combat.PlanSlavesDrop(10, &queueRNG{vals: []float64{0.999}})
	assert.True(t, ok)
	assert.EqualValues(t, 5, got)
}

func TestUnit_PlanSlavesDrop_ZeroCountNotDropped(t *testing.T) {
	t.Parallel()
	// floor(2 * 20 / 100) = 0 → no container.
	_, ok := combat.PlanSlavesDrop(2, &queueRNG{vals: []float64{0}})
	assert.False(t, ok)
}

func TestUnit_PlanSlavesDrop_NoPassengersNoRoll(t *testing.T) {
	t.Parallel()
	// passengers 0 → ok=false and the RNG must never be consumed.
	_, ok := combat.PlanSlavesDrop(0, &queueRNG{})
	assert.False(t, ok)
}

func TestUnit_PlanShipDrops_RegularCargoDropsFull(t *testing.T) {
	t.Parallel()

	items := []domain.CargoItem{
		{GoodsType: 7, Quantity: 100},
		{GoodsType: 8, Quantity: 50},
	}
	// No missiles → rng must never be called.
	drops := combat.PlanShipDrops(items, testMissileType, &queueRNG{})

	assert.ElementsMatch(t, []combat.Drop{
		{GoodsType: 7, Quantity: 100},
		{GoodsType: 8, Quantity: 50},
	}, drops)
}

func TestUnit_PlanShipDrops_SkipsNonPositive(t *testing.T) {
	t.Parallel()

	items := []domain.CargoItem{{GoodsType: 7, Quantity: 0}}
	drops := combat.PlanShipDrops(items, testMissileType, &queueRNG{})

	assert.Empty(t, drops)
}

func TestUnit_PlanShipDrops_NilItems(t *testing.T) {
	t.Parallel()

	assert.Empty(t, combat.PlanShipDrops(nil, testMissileType, &queueRNG{}))
}

func TestUnit_PlanShipDrops_MissileKeptOnHighRoll(t *testing.T) {
	t.Parallel()

	// round(16*0.8125) = 13 (>= 12 → survives the destroy roll).
	// throw = 100000 >> 13 = 12 (>= 5 → drops).
	items := []domain.CargoItem{{GoodsType: testMissileType, Quantity: 100000}}
	drops := combat.PlanShipDrops(items, testMissileType, &queueRNG{vals: []float64{0.8125}})

	assert.Equal(t, []combat.Drop{{GoodsType: testMissileType, Quantity: 12}}, drops)
}

func TestUnit_PlanShipDrops_MissileDestroyedOnLowRoll(t *testing.T) {
	t.Parallel()

	// round(16*0.5) = 8 (< 12 → destroyed, no drop).
	items := []domain.CargoItem{{GoodsType: testMissileType, Quantity: 100000}}
	drops := combat.PlanShipDrops(items, testMissileType, &queueRNG{vals: []float64{0.5}})

	assert.Empty(t, drops)
}

func TestUnit_PlanShipDrops_MissileDestroyedWhenThrowTooSmall(t *testing.T) {
	t.Parallel()

	// chance 13, throw = 1000 >> 13 = 0 (< 5 → destroyed).
	items := []domain.CargoItem{{GoodsType: testMissileType, Quantity: 1000}}
	drops := combat.PlanShipDrops(items, testMissileType, &queueRNG{vals: []float64{0.8125}})

	assert.Empty(t, drops)
}

func TestUnit_PlanShipDrops_MixedStacks(t *testing.T) {
	t.Parallel()

	// Regular stack drops full; missile stack survives (chance 13, throw 12).
	items := []domain.CargoItem{
		{GoodsType: 7, Quantity: 100},
		{GoodsType: testMissileType, Quantity: 100000},
	}
	drops := combat.PlanShipDrops(items, testMissileType, &queueRNG{vals: []float64{0.8125}})

	assert.ElementsMatch(t, []combat.Drop{
		{GoodsType: 7, Quantity: 100},
		{GoodsType: testMissileType, Quantity: 12},
	}, drops)
}
