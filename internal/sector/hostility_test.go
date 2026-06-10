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

// fakeRelations is a sector.Relations oracle keyed by player-id pairs
// (direction-insensitive), defaulting to Neutral.
type fakeRelations struct {
	pairs map[[2]domain.PlayerID]domain.Relation
}

func (f fakeRelations) Get(a, b domain.EntityRef) domain.Relation {
	if a == b {
		return domain.RelationFriend
	}
	pa, pb := domain.PlayerID(a.ID), domain.PlayerID(b.ID)
	if r, ok := f.pairs[[2]domain.PlayerID{pa, pb}]; ok {
		return r
	}
	if r, ok := f.pairs[[2]domain.PlayerID{pb, pa}]; ok {
		return r
	}
	return domain.RelationNeutral
}

func (f fakeRelations) IsHostile(a, b domain.EntityRef) bool { return f.Get(a, b).IsHostile() }

func relationsWorker(t *testing.T, rel sector.Relations, ships []domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	opts = append(opts, sector.WithRelations(rel))
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		opts...,
	)
}

func sendAttack(t *testing.T, w *sector.Worker, player, ship, target int64) {
	t.Helper()
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AttackCommand{
		PlayerID: domain.PlayerID(player), ShipID: domain.ShipID(ship),
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: target},
		Reply:  reply,
	}))
	w.Tick(context.Background()) // drains the command and runs the first fire step
	require.NoError(t, (<-reply).Err)
}

// TestUnit_Worker_LaserFriendlyFireGated: a ship told to attack a clan/ally
// never damages it — fireLasers drops the engagement (criterion: «не атакует
// союзников»).
func TestUnit_Worker_LaserFriendlyFireGated(t *testing.T) {
	t.Parallel()
	a := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := droneShip(2, 200, domain.Vec2{X: 50, Y: 0}) // sturdy, in laser range
	rel := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{{100, 200}: domain.RelationFriend}}

	w := relationsWorker(t, rel, []domain.Ship{a, b})
	sendAttack(t, w, 100, 1, 2)
	for i := 0; i < 5; i++ {
		w.Tick(context.Background())
	}

	snap := w.Snapshot(testSector)
	bSnap, _ := snapshotShipByID(snap, 2)
	assert.Equal(t, b.Shield, bSnap.Shield, "ally must not be damaged")
	assert.Equal(t, b.HP, bSnap.HP)
	aSnap, _ := snapshotShipByID(snap, 1)
	assert.Nil(t, aSnap.AttackTarget, "engagement on an ally is dropped")
}

// TestUnit_Worker_LaserFiresOnNonFriend: gating is friend-specific — a
// neutral target is fired upon as before.
func TestUnit_Worker_LaserFiresOnNonFriend(t *testing.T) {
	t.Parallel()
	a := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	b := droneShip(2, 200, domain.Vec2{X: 50, Y: 0})
	rel := fakeRelations{} // all neutral

	w := relationsWorker(t, rel, []domain.Ship{a, b})
	sendAttack(t, w, 100, 1, 2)
	for i := 0; i < 5; i++ {
		w.Tick(context.Background())
	}

	snap := w.Snapshot(testSector)
	bSnap, _ := snapshotShipByID(snap, 2)
	assert.Less(t, bSnap.Shield+bSnap.HP, b.Shield+b.HP, "neutral target takes damage")
}

// TestUnit_Worker_DroneAutoAcquiresHostile: a drone whose explicit launch
// target is gone locks onto the nearest hostile ship and damages it
// (criterion: «дроны автозахватывают ближайшую враждебную цель»).
func TestUnit_Worker_DroneAutoAcquiresHostile(t *testing.T) {
	t.Parallel()
	owner := droneShip(1, 100, domain.Vec2{X: 0, Y: 0})
	enemy := droneShip(2, 200, domain.Vec2{X: 30, Y: 0})
	rel := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{{100, 200}: domain.RelationHostile}}

	d := domain.Drone{
		ID: 1, SectorID: testSector, OwnerShipID: 1, PlayerID: 100,
		Pos: domain.Vec2{X: 25, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		Target:    domain.EntityRef{Kind: domain.EntityKindShip, ID: 999}, // launch target gone
		HP:        20,
		Damage:    8,
		ExpiresAt: time.Now().Add(time.Hour),
	}

	w := relationsWorker(t, rel, []domain.Ship{owner, enemy},
		sector.WithDrones(nil, map[domain.SectorID][]domain.Drone{testSector: {d}}))
	for i := 0; i < 12; i++ {
		w.Tick(context.Background())
	}

	snap := w.Snapshot(testSector)
	enemySnap, _ := snapshotShipByID(snap, 2)
	assert.Less(t, enemySnap.Shield+enemySnap.HP, enemy.Shield+enemy.HP,
		"auto-acquired hostile should take drone damage")
}

// TestUnit_Tower_EmitsBeamOnFire: when a tower fires it pushes a LaserBeam
// into the shared effect channel so the SPA renders the beam (criterion:
// «луч виден на канвасе»). Reuses the tower test fixtures.
func TestUnit_Tower_EmitsBeamOnFire(t *testing.T) {
	t.Parallel()
	tower := domain.LaserTower{ID: 1, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0}}
	enemy := droneShip(2, 99, domain.Vec2{X: 40, Y: 0}) // in range, foreign

	w := towerWorker(t, []domain.Ship{enemy}, tower, sector.WithHostility(ownerBasedHostility))
	w.Tick(context.Background())

	snap := w.Snapshot(testSector)
	require.NotEmpty(t, snap.LaserEffects, "tower fired → a beam must be emitted")
	beam := snap.LaserEffects[0]
	assert.Equal(t, tower.Pos, beam.From, "beam starts at the tower")
	assert.Equal(t, int64(2), beam.Target.ID)
	assert.Greater(t, beam.DamageDealt, 0)
}
