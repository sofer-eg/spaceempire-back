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

// stationStatic builds a destructible station with the given combat state.
func stationStatic(id int64, owner *domain.PlayerID, pos domain.Vec2, hp, shield, maxShield, recharge int) domain.Station {
	return domain.Station{
		ID: domain.StationID(id), OwnerID: owner, SectorID: testSector, Pos: pos,
		HP: hp, Shield: shield, MaxShield: maxShield, ShieldRecharge: recharge, Built: true,
	}
}

// staticAttacker is a ship rigged to one-or-few-shot a static target.
func staticAttacker(id, playerID int64, pos domain.Vec2, dmg int, target domain.EntityRef) domain.Ship {
	t := target
	return domain.Ship{
		ID: domain.ShipID(id), PlayerID: domain.PlayerID(playerID), SectorID: testSector, Pos: pos,
		HP: 100, MaxHP: 100, Energy: 1000, MaxEnergy: 1000,
		LaserDamage: dmg, LaserRange: 1000, LaserEnergyCost: 0,
		AttackTarget: &t,
	}
}

func staticCombatWorker(t *testing.T, ships []domain.Ship, station domain.Station, opts ...sector.Option) *sector.Worker {
	t.Helper()
	statics := map[domain.SectorID]domain.SectorStatics{testSector: {Stations: []domain.Station{station}}}
	opts = append(opts, sector.WithStatics(statics))
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		opts...,
	)
}

func stationRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindStation, ID: id}
}

func findDestructible(snap sector.Snapshot, ref domain.EntityRef) (domain.DestructibleStatic, bool) {
	for _, d := range snap.Destructibles {
		if d.Ref == ref {
			return d, true
		}
	}
	return domain.DestructibleStatic{}, false
}

// TestUnit_StaticCombat_DamagesHostileStation: a player ship attacking a
// hostile station drains its shield first, then HP.
func TestUnit_StaticCombat_DamagesHostileStation(t *testing.T) {
	t.Parallel()
	station := stationStatic(1, ownerPtr(7), domain.Vec2{X: 10, Y: 0}, 100, 50, 50, 0)
	attacker := staticAttacker(1, 100, domain.Vec2{X: 0, Y: 0}, 30, stationRef(1))

	w := staticCombatWorker(t, []domain.Ship{attacker}, station, sector.WithHostility(ownerBasedHostility))
	w.Tick(context.Background())

	d, ok := findDestructible(w.Snapshot(testSector), stationRef(1))
	require.True(t, ok, "station still present after partial damage")
	assert.Equal(t, 20, d.Shield, "30 dmg drains shield 50 → 20")
	assert.Equal(t, 100, d.HP, "HP untouched while shield holds")
	require.NotEmpty(t, w.Snapshot(testSector).LaserEffects, "a beam was emitted at the station")
}

// TestUnit_StaticCombat_DestroysHostileStation: enough damage removes the
// station from the sector (combat set + rendered layout).
func TestUnit_StaticCombat_DestroysHostileStation(t *testing.T) {
	t.Parallel()
	station := stationStatic(1, ownerPtr(7), domain.Vec2{X: 10, Y: 0}, 100, 50, 50, 0)
	attacker := staticAttacker(1, 100, domain.Vec2{X: 0, Y: 0}, 1000, stationRef(1))

	w := staticCombatWorker(t, []domain.Ship{attacker}, station, sector.WithHostility(ownerBasedHostility))
	w.Tick(context.Background())

	snap := w.Snapshot(testSector)
	_, ok := findDestructible(snap, stationRef(1))
	assert.False(t, ok, "destroyed station gone from combat set")
	assert.Empty(t, snap.Statics.Stations, "destroyed station gone from rendered layout")
}

// TestUnit_StaticCombat_FriendlyInvulnerable: a non-hostile (friendly/neutral)
// station takes no damage and the engagement is dropped.
func TestUnit_StaticCombat_FriendlyInvulnerable(t *testing.T) {
	t.Parallel()
	// Attacker shares the station's owner → ownerBasedHostility = false.
	station := stationStatic(1, ownerPtr(7), domain.Vec2{X: 10, Y: 0}, 100, 50, 50, 0)
	attacker := staticAttacker(1, 7, domain.Vec2{X: 0, Y: 0}, 1000, stationRef(1))

	w := staticCombatWorker(t, []domain.Ship{attacker}, station, sector.WithHostility(ownerBasedHostility))
	w.Tick(context.Background())

	snap := w.Snapshot(testSector)
	d, ok := findDestructible(snap, stationRef(1))
	require.True(t, ok, "friendly station is invulnerable, still present")
	assert.Equal(t, 50, d.Shield)
	assert.Equal(t, 100, d.HP)
	assert.Empty(t, snap.LaserEffects, "no shot fired at a friendly static")
	for _, s := range snap.Ships {
		if s.ID == 1 {
			assert.Nil(t, s.AttackTarget, "engagement dropped against a non-hostile static")
		}
	}
}

// TestUnit_StaticCombat_ShieldRecharges: a damaged static's shield refills by
// ShieldRecharge each tick, clamped at MaxShield.
func TestUnit_StaticCombat_ShieldRecharges(t *testing.T) {
	t.Parallel()
	station := stationStatic(1, ownerPtr(7), domain.Vec2{X: 500, Y: 0}, 100, 20, 50, 10)
	// No attacker — just let the charge tick run.
	w := staticCombatWorker(t, nil, station)
	w.Tick(context.Background())

	d, ok := findDestructible(w.Snapshot(testSector), stationRef(1))
	require.True(t, ok)
	assert.Equal(t, 30, d.Shield, "shield 20 + recharge 10 = 30")

	w.Tick(context.Background())
	w.Tick(context.Background())
	d, _ = findDestructible(w.Snapshot(testSector), stationRef(1))
	assert.Equal(t, 50, d.Shield, "recharge clamps at MaxShield 50")
}
