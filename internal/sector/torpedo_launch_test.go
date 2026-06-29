package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// torpedoShip mirrors missileShip but installs up_torpedo_launcher: the
// phase 10.3.5.3 capability gate for a torpedo launch.
func torpedoShip(id, playerID int64, pos domain.Vec2) domain.Ship {
	return domain.Ship{
		ID:        domain.ShipID(id),
		PlayerID:  domain.PlayerID(playerID),
		SectorID:  testSector,
		Pos:       pos,
		Direction: domain.Vec2{X: 1, Y: 0},
		HP:        200,
		MaxHP:     200,
		Shield:    50,
		MaxShield: 50,
		Equipment: []domain.InstalledEquipment{{Type: "up_torpedo_launcher", Level: 1}},
	}
}

// sendTorpedo launches one torpedo and returns the worker reply after one tick.
func sendTorpedo(t *testing.T, w *sector.Worker, cmd sector.LaunchTorpedoCommand) sector.LaunchTorpedoResult {
	t.Helper()
	reply := make(chan sector.LaunchTorpedoResult, 1)
	cmd.Reply = reply
	require.NoError(t, w.Send(testSector, cmd))
	w.Tick(context.Background())
	return <-reply
}

// TestUnit_LaunchTorpedo_RequiresLauncher: a ship without up_torpedo_launcher
// is refused (ЧТЗ AC-1 gate). The handler maps this to 422 + ammo refund.
func TestUnit_LaunchTorpedo_RequiresLauncher(t *testing.T) {
	t.Parallel()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.Equipment = nil // strip the launcher
	b := torpedoShip(2, 200, domain.Vec2{X: 100, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.ErrorIs(t, res.Err, sector.ErrEquipmentRequired)
}

// TestUnit_LaunchTorpedo_Docked: a docked ship cannot fire (ЧТЗ FR-002).
func TestUnit_LaunchTorpedo_Docked(t *testing.T) {
	t.Parallel()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.Docked = &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	b := torpedoShip(2, 200, domain.Vec2{X: 50, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.ErrorIs(t, res.Err, sector.ErrShipDocked)
}

// TestUnit_LaunchTorpedo_NotOwner: another player cannot launch from somebody
// else's ship.
func TestUnit_LaunchTorpedo_NotOwner(t *testing.T) {
	t.Parallel()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := torpedoShip(2, 200, domain.Vec2{X: 50, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 999, ShipID: 1, Class: 2, // 999 is not the owner of ship 1
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.ErrorIs(t, res.Err, sector.ErrForbidden)
}

// TestUnit_LaunchTorpedo_RejectsSelfTarget: a self-aimed torpedo is rejected
// with ErrInvalidAttackTarget (ЧТЗ AC-4).
func TestUnit_LaunchTorpedo_RejectsSelfTarget(t *testing.T) {
	t.Parallel()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(), nil,
		[]domain.Ship{torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 1},
	})
	require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
}

// TestUnit_LaunchTorpedo_RejectsNonTargetableKind: a container is neither a
// ship nor a destructible static — aiming at one is rejected (ЧТЗ AC-4). Gates
// are likewise excluded in v1 (ЧТЗ C-04) but have no entity kind to target.
func TestUnit_LaunchTorpedo_RejectsNonTargetableKind(t *testing.T) {
	t.Parallel()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(), nil,
		[]domain.Ship{torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2,
		Target: domain.EntityRef{Kind: domain.EntityKindContainer, ID: 7},
	})
	require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
}

// TestUnit_LaunchTorpedo_AcceptsShipTarget: a ship target passes every gate.
func TestUnit_LaunchTorpedo_AcceptsShipTarget(t *testing.T) {
	t.Parallel()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := torpedoShip(2, 200, domain.Vec2{X: 100, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 3,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.NoError(t, res.Err)
}

// TestUnit_LaunchTorpedo_AcceptsStaticTarget: a live destructible static (a
// station resident in the sector) is a valid torpedo target (ЧТЗ AC-5, FR-006).
// The launch resolves the static's position from the live combat set, so the
// target must actually exist (a phantom static is rejected, see the dead-target
// gate test).
func TestUnit_LaunchTorpedo_AcceptsStaticTarget(t *testing.T) {
	t.Parallel()
	station := stationStatic(5, nil, domain.Vec2{X: 100, Y: 0}, 1000, 0, 0, 0)
	w := staticCombatWorker(t, []domain.Ship{torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})}, station)

	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2,
		Target: domain.EntityRef{Kind: domain.EntityKindStation, ID: 5},
	})
	require.NoError(t, res.Err)
}

// TestUnit_LaunchTorpedo_ActionEnergy: a launch is an "action" energy expense
// (ЧТЗ AC-3). The first shot debits EnergyCost; once the pool can no longer
// cover the cost the next shot is refused with ErrNotEnoughEnergy and no energy
// is spent.
func TestUnit_LaunchTorpedo_ActionEnergy(t *testing.T) {
	t.Parallel()
	a := torpedoShip(1, 100, domain.Vec2{X: 0, Y: 0})
	a.Energy = 50
	a.MaxEnergy = 1000
	b := torpedoShip(2, 200, domain.Vec2{X: 100, Y: 0})
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 1000},
		clock.NewRealClock(), nil, []domain.Ship{a, b})

	// First launch: 50 >= 30 → succeeds, energy debited to 20.
	res := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2, EnergyCost: 30,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.NoError(t, res.Err)
	require.Equal(t, 20, shipEnergyByID(t, w, 1), "first launch debits EnergyCost")

	// Second launch: 20 < 30 → rejected, energy unchanged.
	res2 := sendTorpedo(t, w, sector.LaunchTorpedoCommand{
		PlayerID: 100, ShipID: 1, Class: 2, EnergyCost: 30,
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	})
	require.ErrorIs(t, res2.Err, sector.ErrNotEnoughEnergy)
	require.Equal(t, 20, shipEnergyByID(t, w, 1), "rejected launch spends no energy")
}
