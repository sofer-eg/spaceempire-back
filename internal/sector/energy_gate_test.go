package sector_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TestUnit_LaunchMissile_ScoutPoolAffordsCalibratedCost is the TASK-100.3.25
// fix-loop regression for the CRITICAL: the missile action cost (up_launcher
// energy_usage) must sit below the smallest per-class pool (scout MaxEnergy 40)
// so a freshly-spawned scout can actually fire. It drives the real gate at
// command.go:465 with the cost read from the shipped catalog. Before calibration
// the cost was 100 > 40 and the scout could never launch its 5 starting missiles.
func TestUnit_LaunchMissile_ScoutPoolAffordsCalibratedCost(t *testing.T) {
	t.Parallel()

	// Cost sourced from the shipped catalog, exactly as api.launchActionEnergyCost
	// derives launchEnergyCost for the LaunchMissileCommand.
	equipment, err := balance.LoadEquipmentFromFile(filepath.Join("..", "..", "configs", "equipment.yaml"))
	require.NoError(t, err)
	launchers := equipment.EquipmentByType("up_launcher")
	require.NotEmpty(t, launchers)
	cost := launchers[0].EnergyUsage
	require.Equal(t, 15, cost, "calibrated missile action cost")

	const scoutPool = 40 // ship_classes M5 Разведчик MaxEnergy — the smallest pool
	require.Less(t, cost, scoutPool, "calibrated cost must fit the smallest pool")

	ctx := context.Background()
	scout := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	scout.Energy = scoutPool
	scout.MaxEnergy = scoutPool
	target := missileShip(2, 200, domain.Vec2{X: 100, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{scout, target})

	// Calibrated cost 15 ≤ pool 40 → the gate passes; energy debited to 25.
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID:   100,
		ShipID:     1,
		Target:     domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		EnergyCost: cost,
		Reply:      reply,
	}))
	w.Tick(ctx)
	require.NoError(t, (<-reply).Err, "scout affords the calibrated shot")
	require.Equal(t, scoutPool-cost, shipEnergyByID(t, w, 1), "launch debits the calibrated cost")

	// The pre-calibration cost (100) exceeds the scout pool → the same gate refuses
	// it, reproducing the CRITICAL the calibration fixes.
	reply2 := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID:   100,
		ShipID:     1,
		Target:     domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
		EnergyCost: 100,
		Reply:      reply2,
	}))
	w.Tick(ctx)
	require.ErrorIs(t, (<-reply2).Err, sector.ErrNotEnoughEnergy,
		"un-calibrated 100 > scout pool 40 is refused")
}
