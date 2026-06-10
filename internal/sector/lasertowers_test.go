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

// ownerBasedHostility is the owner-keyed stand-in for relations (6.2):
// any ship not owned by the tower owner is hostile.
func ownerBasedHostility(towerOwner *domain.PlayerID, ship *domain.Ship) bool {
	if towerOwner == nil {
		return ship.PlayerID != 0
	}
	return ship.PlayerID != *towerOwner
}

func towerWorker(t *testing.T, ships []domain.Ship, tower domain.LaserTower, opts ...sector.Option) *sector.Worker {
	t.Helper()
	statics := map[domain.SectorID]domain.SectorStatics{
		testSector: {LaserTowers: []domain.LaserTower{tower}},
	}
	opts = append(opts, sector.WithStatics(statics))
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		opts...,
	)
}

func ownerPtr(id int64) *domain.PlayerID {
	p := domain.PlayerID(id)
	return &p
}

// TestUnit_Tower_DamagesForeignShip: a tower with an injected owner-based
// predicate damages an enemy ship in range and leaves its owner's ship
// untouched.
func TestUnit_Tower_DamagesForeignShip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tower := domain.LaserTower{ID: 1, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{}}
	enemy := droneShip(2, 99, domain.Vec2{X: 40, Y: 0}) // in range, foreign
	friend := droneShip(3, 7, domain.Vec2{X: 30, Y: 0}) // in range, own owner

	w := towerWorker(t, []domain.Ship{enemy, friend}, tower,
		sector.WithHostility(ownerBasedHostility))
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	got := map[domain.ShipID]domain.Ship{}
	for _, s := range snap.Ships {
		got[s.ID] = s
	}
	require.Less(t, got[2].Shield, friend.Shield, "enemy ship took damage")
	require.Equal(t, friend.Shield, got[3].Shield, "own ship untouched")
	require.Equal(t, friend.HP, got[3].HP, "own ship untouched")
}

// TestUnit_Tower_OutOfRangeNotAttacked: a foreign ship beyond the tower's
// range is not damaged.
func TestUnit_Tower_OutOfRangeNotAttacked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tower := domain.LaserTower{ID: 1, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{}}
	enemy := droneShip(2, 99, domain.Vec2{X: 1000, Y: 0}) // far out of range

	w := towerWorker(t, []domain.Ship{enemy}, tower,
		sector.WithHostility(ownerBasedHostility))
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	require.Equal(t, enemy.Shield, snap.Ships[0].Shield)
	require.Equal(t, enemy.HP, snap.Ships[0].HP)
}

// raceHostilePirate marks race 6 (pirate) hostile to every ship except the
// NPC system player (id 500 in these tests).
func raceHostilePirate(race int, ship *domain.Ship) bool {
	return race == 6 && ship.PlayerID != domain.PlayerID(500)
}

// TestUnit_Tower_RaceOwnedFiresAtPlayers: a race-owned (owner==nil) tower of a
// hostile race (pirate) fires at a real-player ship but spares NPC ships
// (phase 8.3).
func TestUnit_Tower_RaceOwnedFiresAtPlayers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tower := domain.LaserTower{ID: 1, OwnerID: nil, Race: 6, SectorID: testSector, Pos: domain.Vec2{}}
	player := droneShip(2, 99, domain.Vec2{X: 40, Y: 0})   // real player, in range
	npcShip := droneShip(3, 500, domain.Vec2{X: 30, Y: 0}) // NPC ship, in range

	w := towerWorker(t, []domain.Ship{player, npcShip}, tower,
		sector.WithRaceHostility(raceHostilePirate))
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	got := map[domain.ShipID]domain.Ship{}
	for _, s := range snap.Ships {
		got[s.ID] = s
	}
	require.Less(t, got[2].Shield, player.Shield, "pirate tower hits the real player")
	require.Equal(t, npcShip.Shield, got[3].Shield, "NPC ship spared")
}

// TestUnit_Tower_NonHostileRacePassive: a race-owned tower of a non-hostile
// race (Argon = 1) does not fire even with the race predicate wired.
func TestUnit_Tower_NonHostileRacePassive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tower := domain.LaserTower{ID: 1, OwnerID: nil, Race: 1, SectorID: testSector, Pos: domain.Vec2{}}
	player := droneShip(2, 99, domain.Vec2{X: 20, Y: 0})

	w := towerWorker(t, []domain.Ship{player}, tower,
		sector.WithRaceHostility(raceHostilePirate))
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	require.Equal(t, player.Shield, snap.Ships[0].Shield, "Argon tower stays passive")
	require.Equal(t, player.HP, snap.Ships[0].HP)
}

// TestUnit_Tower_NoHostilityStubLeavesShipsUntouched: the default
// production predicate (NoHostility) never fires, even on a foreign ship
// parked next to the tower.
func TestUnit_Tower_NoHostilityStubLeavesShipsUntouched(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tower := domain.LaserTower{ID: 1, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{}}
	enemy := droneShip(2, 99, domain.Vec2{X: 10, Y: 0})

	w := towerWorker(t, []domain.Ship{enemy}, tower) // no WithHostility → stub
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	require.Equal(t, enemy.Shield, snap.Ships[0].Shield)
	require.Equal(t, enemy.HP, snap.Ships[0].HP)
}
