package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TASK-112 closes the gap TASK-100.3.5.6 left: a torpedo was made
// shoot-downable, but nothing could be aimed at one. These tests cover the three
// ways fire now reaches a torpedo — an ordered laser, a drone screen, a laser
// tower — and the hostility rule that separates the first from the other two.

func torpedoRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindTorpedo, ID: id}
}

// inFlightTorpedo is a live torpedo parked at pos, owned by player/ship, with
// enough HP to survive a shot or two so a test can see damage rather than only a
// kill.
func inFlightTorpedo(id int64, player, ownerShip int64, pos domain.Vec2, hp int) domain.Torpedo {
	return domain.Torpedo{
		ID: domain.TorpedoID(id), SectorID: testSector,
		OwnerShipID: domain.ShipID(ownerShip), PlayerID: domain.PlayerID(player),
		Pos: pos, Direction: domain.Vec2{X: 1, Y: 0},
		Target:    domain.EntityRef{Kind: domain.EntityKindShip, ID: 1},
		Class:     2,
		Damage:    150,
		Speed:     0, // parked: the test is about who shoots it, not about flight
		HitRadius: 14, SplashRadius: 40,
		HP:        hp,
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

// torpedoLauncher is the ship a test torpedo belongs to, parked far outside every
// weapon's reach: a torpedo whose owner has left the sector self-destructs (ЧТЗ
// FR-009), so the launcher must exist, but it must not become a target itself.
func torpedoLauncher(id, player int64) domain.Ship {
	return domain.Ship{
		ID: domain.ShipID(id), PlayerID: domain.PlayerID(player), SectorID: testSector,
		Pos: domain.Vec2{X: 50000, Y: 0}, HP: 300, MaxHP: 300,
	}
}

func liveTorpedoByID(t *testing.T, w *sector.Worker, id domain.TorpedoID) (domain.Torpedo, bool) {
	t.Helper()
	for _, tp := range w.Snapshot(testSector).Torpedos {
		if tp.ID == id {
			return tp, true
		}
	}
	return domain.Torpedo{}, false
}

// AC#1: AttackCommand accepts a torpedo, which is what finally makes
// fireLaserAtProjectile reachable. Before this the command answered 400 for every
// non-ship kind, so the shoot-down mechanism was structurally present and unusable.
func TestUnit_PointDefense_AttackCommandAcceptsTorpedo(t *testing.T) {
	t.Parallel()
	repo := newFakeTorpedoRepo()
	shooter := domain.Ship{
		ID: 1, PlayerID: 100, SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0},
		HP: 200, MaxHP: 200, Energy: 1000, MaxEnergy: 1000,
		LaserDamage: 40, LaserRange: 1000, LaserEnergyCost: 0,
	}
	tp := inFlightTorpedo(1, 200, 2, domain.Vec2{X: 100, Y: 0}, 500)
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), nil, nil,
		// The launching ship has to be here: a torpedo whose owner is gone dies
		// with it on the same tick (ЧТЗ FR-009), which would end the test early.
		map[domain.SectorID][]domain.Ship{testSector: {shooter, torpedoLauncher(2, 200)}},
		sector.WithTorpedos(repo, map[domain.SectorID][]domain.Torpedo{testSector: {tp}}))

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AttackCommand{
		PlayerID: 100, ShipID: 1, Target: torpedoRef(1), Reply: reply,
	}))
	w.Tick(context.Background())
	require.NoError(t, (<-reply).Err, "a torpedo is a legal attack target")

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok, "the torpedo is still in flight after one shot")
	assert.Less(t, live.HP, 500, "the ordered laser damaged it")
	assert.NotEmpty(t, w.Snapshot(testSector).LaserEffects, "and the beam is drawn")
}

// A static is still refused: statics are player-unattackable by design
// (TASK-53.2), and widening the projectile set must not widen that.
func TestUnit_PointDefense_AttackCommandStillRefusesStatics(t *testing.T) {
	t.Parallel()
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 100, SectorID: testSector, HP: 100},
		}})

	for _, ref := range []domain.EntityRef{
		stationRef(3),
		gateRef(10),
		containerRef(4),
		{Kind: domain.EntityKindShip, ID: 1}, // self
	} {
		reply := make(chan sector.CmdResult, 1)
		require.NoError(t, w.Send(testSector, sector.AttackCommand{
			PlayerID: 100, ShipID: 1, Target: ref, Reply: reply,
		}))
		w.Tick(context.Background())
		assert.ErrorIs(t, (<-reply).Err, sector.ErrInvalidAttackTarget, "kind %d", ref.Kind)
	}
}

// AC#3: ORDERED fire is deliberately unselective — a player may shoot their own
// torpedo down (aborting it is a legitimate move, and splash friendly-fire is
// already unselective per ЧТЗ R-02). The relations oracle here says the shooter is
// friendly with everyone, including themselves.
func TestUnit_PointDefense_OrderedFireIsNotHostilityGated(t *testing.T) {
	t.Parallel()
	repo := newFakeTorpedoRepo()
	shooter := domain.Ship{
		ID: 1, PlayerID: 100, SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0},
		HP: 200, MaxHP: 200, Energy: 1000, MaxEnergy: 1000,
		LaserDamage: 40, LaserRange: 1000, LaserEnergyCost: 0,
	}
	own := inFlightTorpedo(1, 100, 1, domain.Vec2{X: 100, Y: 0}, 500) // the shooter's OWN
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {shooter}},
		sector.WithTorpedos(repo, map[domain.SectorID][]domain.Torpedo{testSector: {own}}),
		sector.WithRelations(fakeRelations{}))

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AttackCommand{
		PlayerID: 100, ShipID: 1, Target: torpedoRef(1), Reply: reply,
	}))
	w.Tick(context.Background())
	require.NoError(t, (<-reply).Err)

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok)
	assert.Less(t, live.HP, 500, "an explicit order fires even at your own torpedo")
}

// droneScreenWorker builds a worker whose ship 1 (player 100) has a drone screen
// out and a torpedo in the sector, with the given relations oracle.
func droneScreenWorker(t *testing.T, rel fakeRelations, tp domain.Torpedo, dronePos domain.Vec2) *sector.Worker {
	t.Helper()
	owner := domain.Ship{
		ID: 1, PlayerID: 100, SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0},
		HP: 200, MaxHP: 200,
	}
	drone := domain.Drone{
		ID: 1, SectorID: testSector, OwnerShipID: 1, PlayerID: 100,
		Pos: dronePos, Direction: domain.Vec2{X: 1, Y: 0},
		Target:    domain.EntityRef{Kind: domain.EntityKindShip, ID: 4242}, // long gone
		HP:        20,
		Damage:    8,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	ships := []domain.Ship{owner}
	if tp.OwnerShipID != owner.ID {
		ships = append(ships, torpedoLauncher(int64(tp.OwnerShipID), int64(tp.PlayerID)))
	}
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		sector.WithDrones(nil, map[domain.SectorID][]domain.Drone{testSector: {drone}}),
		sector.WithTorpedos(newFakeTorpedoRepo(), map[domain.SectorID][]domain.Torpedo{testSector: {tp}}),
		sector.WithRelations(rel))
}

// AC#2 (drone half): a drone whose ship target is gone engages an incoming
// HOSTILE torpedo. The drone sits inside its own fire range of the torpedo so the
// interception lands on the first tick.
func TestUnit_PointDefense_DroneInterceptsHostileTorpedo(t *testing.T) {
	t.Parallel()
	hostile := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{
		{100, 200}: domain.RelationHostile,
	}}
	tp := inFlightTorpedo(1, 200, 2, domain.Vec2{X: 20, Y: 0}, 500)
	w := droneScreenWorker(t, hostile, tp, domain.Vec2{X: 0, Y: 0})

	w.Tick(context.Background())

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok, "one drone tick does not kill a 500 HP torpedo")
	assert.Less(t, live.HP, 500, "the drone screen is shooting the incoming torpedo")

	impacts := w.Snapshot(testSector).DroneImpacts
	require.NotEmpty(t, impacts, "the interception is reported to the client")
	assert.Equal(t, domain.EntityKindTorpedo, impacts[0].Target.Kind)
	assert.Greater(t, impacts[0].Damage, 0)
}

// AC#3 (the other half of the rule): AUTOMATIC fire IS hostility-gated. A drone
// must never shoot its own side's torpedoes — point defence that did would be
// worse than none.
func TestUnit_PointDefense_DroneIgnoresFriendlyTorpedo(t *testing.T) {
	t.Parallel()
	friendly := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{
		{100, 200}: domain.RelationFriend,
	}}
	tp := inFlightTorpedo(1, 200, 2, domain.Vec2{X: 20, Y: 0}, 500)
	w := droneScreenWorker(t, friendly, tp, domain.Vec2{X: 0, Y: 0})

	w.Tick(context.Background())

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok)
	assert.Equal(t, 500, live.HP, "an allied torpedo passes through the screen untouched")
	assert.Empty(t, w.Snapshot(testSector).DroneImpacts)
}

// A drone's own torpedo is never a target either: the oracle reports no player as
// hostile to themselves, so this needs no special case — but it must hold.
func TestUnit_PointDefense_DroneIgnoresItsOwnersTorpedo(t *testing.T) {
	t.Parallel()
	tp := inFlightTorpedo(1, 100, 1, domain.Vec2{X: 20, Y: 0}, 500)
	w := droneScreenWorker(t, fakeRelations{}, tp, domain.Vec2{X: 0, Y: 0})

	w.Tick(context.Background())

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok)
	assert.Equal(t, 500, live.HP, "a drone does not shoot its own side's ordnance")
}

// Ships keep priority: with a hostile ship in range the drone fights the ship and
// leaves the torpedo alone. Interception fills the idle ticks, it does not replace
// the drone's job.
func TestUnit_PointDefense_DronePrefersShipOverTorpedo(t *testing.T) {
	t.Parallel()
	hostile := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{
		{100, 200}: domain.RelationHostile,
	}}
	enemy := domain.Ship{
		ID: 2, PlayerID: 200, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0},
		HP: 300, MaxHP: 300,
	}
	tp := inFlightTorpedo(1, 200, 2, domain.Vec2{X: 20, Y: 0}, 500)
	owner := domain.Ship{
		ID: 1, PlayerID: 100, SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0}, HP: 200, MaxHP: 200,
	}
	drone := domain.Drone{
		ID: 1, SectorID: testSector, OwnerShipID: 1, PlayerID: 100,
		Pos: domain.Vec2{X: 0, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 4242},
		HP:     20, Damage: 8, ExpiresAt: time.Now().Add(time.Hour),
	}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {owner, enemy}},
		sector.WithDrones(nil, map[domain.SectorID][]domain.Drone{testSector: {drone}}),
		sector.WithTorpedos(newFakeTorpedoRepo(), map[domain.SectorID][]domain.Torpedo{testSector: {tp}}),
		sector.WithRelations(hostile))

	w.Tick(context.Background())

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok)
	assert.Equal(t, 500, live.HP, "the drone is busy with the enemy ship")
	impacts := w.Snapshot(testSector).DroneImpacts
	require.NotEmpty(t, impacts)
	assert.Equal(t, domain.EntityKindShip, impacts[0].Target.Kind)
}

// AC#2 (tower half): a player-owned laser tower with no hostile ship in range
// shoots an incoming hostile torpedo. The tower's hostility comes from the same
// oracle it uses for ships, resolved through the torpedo's launching ship.
func TestUnit_PointDefense_TowerInterceptsHostileTorpedo(t *testing.T) {
	t.Parallel()
	owner := domain.PlayerID(100)
	tower := domain.LaserTower{
		ID: 1, OwnerID: &owner, SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0},
		HP: 5000, Shield: 100, MaxShield: 100,
	}
	launcher := domain.Ship{
		// Far outside towerSpec.Range (150) so the tower has no ship to shoot,
		// while its torpedo is right on top of the tower.
		ID: 2, PlayerID: 200, SectorID: testSector, Pos: domain.Vec2{X: 5000, Y: 0},
		HP: 300, MaxHP: 300,
	}
	tp := inFlightTorpedo(1, 200, 2, domain.Vec2{X: 40, Y: 0}, 500)

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {launcher}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			testSector: {LaserTowers: []domain.LaserTower{tower}},
		}),
		sector.WithTorpedos(newFakeTorpedoRepo(), map[domain.SectorID][]domain.Torpedo{testSector: {tp}}),
		sector.WithHostility(func(o *domain.PlayerID, ship *domain.Ship) bool {
			return o != nil && *o == 100 && ship.PlayerID == 200
		}))

	w.Tick(context.Background())

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok, "one tower shot does not kill a 500 HP torpedo")
	assert.Less(t, live.HP, 500, "the tower engaged the torpedo")

	effects := w.Snapshot(testSector).LaserEffects
	require.NotEmpty(t, effects, "the tower's beam is drawn")
	assert.Equal(t, domain.EntityKindTorpedo, effects[0].Target.Kind)
}

// A tower leaves a friendly torpedo alone — the automatic-fire gate again.
//
// (The nil-launcher branch in acquireTowerTorpedo is belt-and-braces rather than a
// live case: a torpedo whose owner has left the sector self-destructs on the same
// tick, ЧТЗ FR-009, so a truly orphaned torpedo never survives to be acquired.)
func TestUnit_PointDefense_TowerIgnoresFriendlyTorpedo(t *testing.T) {
	t.Parallel()
	owner := domain.PlayerID(100)
	tower := domain.LaserTower{
		ID: 1, OwnerID: &owner, SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0},
		HP: 5000, Shield: 100, MaxShield: 100,
	}
	friendlyLauncher := domain.Ship{
		ID: 2, PlayerID: 300, SectorID: testSector, Pos: domain.Vec2{X: 5000, Y: 0}, HP: 300, MaxHP: 300,
	}
	friendlyTp := inFlightTorpedo(1, 300, 2, domain.Vec2{X: 40, Y: 0}, 500)

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {friendlyLauncher}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			testSector: {LaserTowers: []domain.LaserTower{tower}},
		}),
		sector.WithTorpedos(newFakeTorpedoRepo(),
			map[domain.SectorID][]domain.Torpedo{testSector: {friendlyTp}}),
		// Hostile to player 200 only — the friendly torpedo's owner is 300.
		sector.WithHostility(func(o *domain.PlayerID, ship *domain.Ship) bool {
			return o != nil && *o == 100 && ship.PlayerID == 200
		}))

	w.Tick(context.Background())

	live, ok := liveTorpedoByID(t, w, 1)
	require.True(t, ok)
	assert.Equal(t, 500, live.HP, "an allied torpedo is not intercepted")
	assert.Empty(t, w.Snapshot(testSector).LaserEffects, "the tower held its fire")
}
