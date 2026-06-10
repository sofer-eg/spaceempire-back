package combat

import (
	"math"

	"spaceempire/back/internal/domain"
)

// RNG is the randomness source PlanShipDrops needs. *math/rand.Rand
// satisfies it; tests inject a deterministic queue. Kept minimal (one
// method) per ISP so the drop logic stays a pure, table-testable
// function.
type RNG interface {
	Float64() float64
}

// Drop is one cargo stack that survived a ship's destruction and should
// be spawned into its own container. Port of one iteration of the SP
// KillObject cargo loops (see kill_object.md §3).
type Drop struct {
	GoodsType domain.GoodsTypeID
	Quantity  int64
}

// missileDestroyThreshold and missileMinThrow port the SP magic numbers:
// a missile stack survives only when round(16*rand()) >= 12, and the
// surviving count (count >> chance) must be at least 5 to drop.
const (
	missileDestroyThreshold = 12
	missileMinThrow         = 5
)

// PlanShipDrops decides which of a dead ship's cargo stacks drop into
// containers, porting KillObject's two loops:
//
//   - a missile stack (GoodsType == missileType) survives only on a high
//     roll, and then only a bit-shifted fraction of its count drops;
//   - every other stack drops in full.
//
// One returned Drop == one container the caller will spawn. rng.Float64
// is consumed exactly once per missile stack and never for other stacks.
func PlanShipDrops(items []domain.CargoItem, missileType domain.GoodsTypeID, rng RNG) []Drop {
	var drops []Drop
	for _, item := range items {
		if item.Quantity <= 0 {
			continue
		}
		if item.GoodsType == missileType {
			if qty, ok := missileThrow(item.Quantity, rng); ok {
				drops = append(drops, Drop{GoodsType: item.GoodsType, Quantity: qty})
			}
			continue
		}
		drops = append(drops, Drop{GoodsType: item.GoodsType, Quantity: item.Quantity})
	}
	return drops
}

// slavesPctMin / slavesPctMax bound the share of a killed passenger ship's
// passengers that spill as a Slaves container — the SP's
// floor(passengers * rand(20,50)/100).
const (
	slavesPctMin = 20
	slavesPctMax = 50
)

// PlanSlavesDrop computes how many "Slaves" units a destroyed passenger ship
// spills: floor(passengers * pct/100) for a random pct in [20,50]. Returns
// ok=false when the ship carried no passengers or the rounded count is zero
// (so the caller drops no container). rng.Float64 is consumed once.
func PlanSlavesDrop(passengers int, rng RNG) (int64, bool) {
	if passengers <= 0 {
		return 0, false
	}
	span := slavesPctMax - slavesPctMin + 1
	pct := slavesPctMin + int(rng.Float64()*float64(span))
	if pct > slavesPctMax {
		pct = slavesPctMax // guard the Float64()==~1.0 edge
	}
	count := int64(passengers) * int64(pct) / 100
	if count <= 0 {
		return 0, false
	}
	return count, true
}

// missileThrow ports the SP cargo_missiles loop: chance = round(16*rand);
// chance < 12 destroys the stack; otherwise throw = count >> chance, and
// a throw below 5 is also destroyed.
func missileThrow(count int64, rng RNG) (int64, bool) {
	chance := int(math.Round(16 * rng.Float64()))
	if chance < missileDestroyThreshold {
		return 0, false
	}
	throw := count >> uint(chance)
	if throw < missileMinThrow {
		return 0, false
	}
	return throw, true
}
