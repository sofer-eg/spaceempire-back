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
	"spaceempire/back/internal/world"
)

// TASK-111 covers the missile target kinds that are NOT destructible statics
// (those are TASK-113's): a spacesuit, a gate, and a loot container.

func containerRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindContainer, ID: id}
}

// AC#1: a spacesuit is a ships row (TASK-87), so it rides the ordinary ship
// branch — this is the regression guard that the TASK-113 static work did not
// narrow the ship path. The suit here has no equipment and 1 HP, which is what an
// EVA row looks like.
func TestUnit_Missile_HitsSpacesuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	suit := domain.Ship{
		ID: 2, PlayerID: 200, SectorID: testSector,
		Pos: domain.Vec2{X: 200, Y: 0}, HP: 1, MaxHP: 1,
	}
	w := missileStaticWorker(t, clock.NewRealClock(),
		[]domain.Ship{missileShip(1, 100, domain.Vec2{X: 0, Y: 0}), suit},
		domain.SectorStatics{})

	require.NoError(t, sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	}).Err, "a spacesuit is a valid missile target")

	var killed bool
	for i := 0; i < 8 && !killed; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if !imp.Expired && imp.Killed {
				killed = true
				assert.Equal(t, domain.EntityKindShip, imp.Target.Kind)
			}
		}
	}
	require.True(t, killed, "the missile reaches and destroys the suit")
}

// AC#2: a gate is a valid missile target now that TASK-110 gave it combat state,
// and the damage runs through the same Damageable path as any static.
func TestUnit_Missile_DamagesGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sectors := []domain.Sector{{ID: testSector, Name: "A"}, {ID: 2, Name: "B"}}
	gates := []domain.Gate{{
		ID:      10,
		SectorA: testSector, PosA: domain.Vec2{X: 200, Y: 0},
		SectorB: 2, PosB: domain.Vec2{X: -200, Y: 0},
		HP: 100000, Shield: 0, MaxShield: 0,
	}}
	topo := world.New(sectors, gates)

	w := missileStaticWorker(t, clock.NewRealClock(),
		[]domain.Ship{missileShip(1, 100, domain.Vec2{X: 0, Y: 0})},
		domain.SectorStatics{},
		sector.WithHandoff(topo, &fakeBus{}))

	require.NoError(t, sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: gateRef(10),
	}).Err, "a gate is a valid missile target since TASK-110")

	var hit bool
	for i := 0; i < 8 && !hit; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if !imp.Expired {
				hit = true
				assert.Equal(t, domain.EntityKindGate, imp.Target.Kind)
				assert.Greater(t, imp.Damage, 0)
				assert.False(t, imp.Killed, "one missile does not fell a 100k-hull gate")
			}
		}
	}
	require.True(t, hit, "the missile reaches the gate")

	d, ok := findDestructible(w.Snapshot(testSector), gateRef(10))
	require.True(t, ok)
	assert.Less(t, d.HP, 100000, "the gate took point damage")
	assert.NotNil(t, topo.Gate(10), "and survived, so the link stands")
}

// containerMissileWorker is missileStaticWorker plus one loot container in the
// sector, seeded through the initial-containers option so it goes through the
// same cold-start path production uses.
func containerMissileWorker(t *testing.T, hp int, repo sector.ContainerRepo) (*sector.Worker, domain.ContainerID) {
	t.Helper()
	const id = domain.ContainerID(4)
	c := domain.Container{
		ID: id, SectorID: testSector, Pos: domain.Vec2{X: 200, Y: 0},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000, ContainerHP: hp},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {missileShip(1, 100, domain.Vec2{X: 0, Y: 0})}},
		sector.WithOrdnance(unlimitedOrdnance()),
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{testSector: {c}}),
	)
	return w, id
}

// AC#3: a missile destroys a loot container along with its cargo. Denying an
// enemy their loot is the point, so nothing is salvaged — the crate leaves the
// sector and its row is deleted, the same reap the TTL sweep runs.
func TestUnit_Missile_DestroysContainer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := &fakeContainerRepo{}
	w, id := containerMissileWorker(t, 10, repo) // 10 HP < one missile's damage

	require.NoError(t, sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: containerRef(int64(id)),
	}).Err, "a loot container is a valid missile target")

	var killed bool
	for i := 0; i < 8 && !killed; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if !imp.Expired && imp.Killed {
				killed = true
				assert.Equal(t, domain.EntityKindContainer, imp.Target.Kind)
				assert.Greater(t, imp.Damage, 0)
			}
		}
	}
	require.True(t, killed, "the crate is destroyed by one hit")

	assert.Empty(t, w.Snapshot(testSector).Containers, "gone from the sector")
	assert.Equal(t, []domain.ContainerID{id}, repo.deleted, "and its row (with its cargo) is deleted")
}

// A tough crate survives a hit: the damage lands, the container stays pickable,
// and no row is deleted. This is what makes the container a real Damageable rather
// than a one-shot special case.
func TestUnit_Missile_ContainerSurvivesAHit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := &fakeContainerRepo{}
	w, id := containerMissileWorker(t, 100000, repo)

	require.NoError(t, sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: containerRef(int64(id)),
	}).Err)

	var hit bool
	for i := 0; i < 8 && !hit; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if !imp.Expired {
				hit = true
				assert.False(t, imp.Killed)
			}
		}
	}
	require.True(t, hit)

	assert.Len(t, w.Snapshot(testSector).Containers, 1, "the crate is still there to pick up")
	assert.Empty(t, repo.deleted, "nothing was deleted")
}

// A launch at a container that is already gone (picked up or expired between the
// click and the tick) is refused before the hold is touched — the same gate a
// missing static gets.
func TestUnit_Missile_UnknownContainerRejected(t *testing.T) {
	t.Parallel()
	repo := &fakeContainerRepo{}
	w, _ := containerMissileWorker(t, 50, repo)

	res := sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, EnergyCost: 30, Target: containerRef(4242),
	})
	require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
	assert.Zero(t, res.MissileID)
	assert.Empty(t, w.Snapshot(testSector).Missiles)
}

// The public target set: containers and gates are missile targets, but a container
// is NOT a "static" — that set gates lasers and torpedoes, which stay off crates.
func TestUnit_Missile_TargetKindSets(t *testing.T) {
	t.Parallel()
	assert.True(t, sector.IsMissileTargetKind(domain.EntityKindContainer))
	assert.True(t, sector.IsMissileTargetKind(domain.EntityKindGate))
	assert.True(t, sector.IsMissileTargetKind(domain.EntityKindStation))
	assert.False(t, sector.IsStaticTargetKind(domain.EntityKindContainer),
		"a container is loot with a TTL, not sector layout")
	assert.False(t, sector.IsMissileTargetKind(domain.EntityKindShip),
		"the ship branch is handled separately (self-target check)")
	assert.False(t, sector.IsMissileTargetKind(domain.EntityKindTorpedo),
		"projectiles are TASK-112's business, not a missile's")
}
