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

// missileStaticWorker builds a single-sector worker with a missile launcher
// fleet and a set of destructible statics (TASK-113). AOI is wide so the
// missile and the static stay in the snapshot for the whole flight.
func missileStaticWorker(t *testing.T, clk clock.Clock, ships []domain.Ship, statics domain.SectorStatics, opts ...sector.Option) *sector.Worker {
	t.Helper()
	opts = append([]sector.Option{
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{testSector: statics}),
	}, opts...)
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 100000},
		clk, nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		opts...,
	)
}

// sendMissile launches one missile and returns the worker reply after one tick.
func sendMissile(t *testing.T, w *sector.Worker, cmd sector.LaunchMissileCommand) sector.LaunchMissileResult {
	t.Helper()
	reply := make(chan sector.LaunchMissileResult, 1)
	cmd.Reply = reply
	require.NoError(t, w.Send(testSector, cmd))
	w.Tick(context.Background())
	return <-reply
}

// TestUnit_LaunchMissile_StaticTargetGate covers the command-level target
// resolve (TASK-113 FR-07, AC-7): a live destructible static is a valid missile
// target, while a launch at a missing/dead static is rejected BEFORE any energy
// is spent and no missile is created.
func TestUnit_LaunchMissile_StaticTargetGate(t *testing.T) {
	t.Parallel()

	t.Run("live static accepted", func(t *testing.T) {
		t.Parallel()
		station := stationStatic(5, nil, domain.Vec2{X: 100, Y: 0}, 1000, 0, 0, 0)
		a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
		a.Energy, a.MaxEnergy = 50, 1000
		w := missileStaticWorker(t, clock.NewRealClock(), []domain.Ship{a},
			domain.SectorStatics{Stations: []domain.Station{station}})

		res := sendMissile(t, w, sector.LaunchMissileCommand{
			PlayerID: 100, ShipID: 1, EnergyCost: 30,
			Target: stationRef(5),
		})
		require.NoError(t, res.Err)
		require.NotZero(t, res.MissileID)
		require.Equal(t, 20, shipEnergyByID(t, w, 1), "launch debits the action energy")
	})

	t.Run("missing static rejected, no energy spent", func(t *testing.T) {
		t.Parallel()
		a := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
		a.Energy, a.MaxEnergy = 50, 1000
		w := missileStaticWorker(t, clock.NewRealClock(), []domain.Ship{a},
			domain.SectorStatics{}) // no statics at all

		res := sendMissile(t, w, sector.LaunchMissileCommand{
			PlayerID: 100, ShipID: 1, EnergyCost: 30,
			Target: stationRef(5), // no such station
		})
		require.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
		require.Zero(t, res.MissileID)
		require.Empty(t, w.Snapshot(testSector).Missiles)
		require.Equal(t, 50, shipEnergyByID(t, w, 1), "rejected launch spends no energy")
	})
}

// TestUnit_TickMissiles_DamagesStatic: a missile launched at a tough station
// homes onto it and, at dist<=HitRadius, deals POINT damage (no splash) through
// the Damageable path — the station's HP drops, the missile is removed, and a
// non-expired impact is recorded (TASK-113 FR-08, AC-2).
func TestUnit_TickMissiles_DamagesStatic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const startHP = 100
	station := stationStatic(9, nil, domain.Vec2{X: 200, Y: 0}, startHP, 0, 0, 0)
	w := missileStaticWorker(t, clock.NewRealClock(),
		[]domain.Ship{missileShip(1, 100, domain.Vec2{X: 0, Y: 0})},
		domain.SectorStatics{Stations: []domain.Station{station}})

	require.NoError(t, sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: stationRef(9),
	}).Err)

	var hit bool
	for i := 0; i < 8 && !hit; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if !imp.Expired {
				hit = true
				require.False(t, imp.Killed, "the tough station survives the single hit")
				require.Equal(t, domain.EntityKindStation, imp.Target.Kind, "impact carries the static target")
				require.Greater(t, imp.Damage, 0)
			}
		}
	}
	require.True(t, hit, "missile must reach and damage the stationary station")

	snap := w.Snapshot(testSector)
	require.Empty(t, snap.Missiles, "missile removed after the hit")
	d, ok := findDestructible(snap, stationRef(9))
	require.True(t, ok, "damaged station appears in the combat snapshot")
	require.Less(t, d.HP, startHP, "the station took point damage")
}

// TestUnit_TickMissiles_KillsFragileStatic: a missile that drops a fragile
// static to HP<=0 reaps it inline (TASK-113 FR-08) — there is no static sweep,
// so the kill must happen in applyMissileHit: the static leaves the combat set
// and the rendered layout, and the impact reports Killed.
func TestUnit_TickMissiles_KillsFragileStatic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// 20 HP < 30 missile damage → one hit kills it.
	station := stationStatic(9, ownerPtr(7), domain.Vec2{X: 200, Y: 0}, 20, 0, 0, 0)
	w := missileStaticWorker(t, clock.NewRealClock(),
		[]domain.Ship{missileShip(1, 100, domain.Vec2{X: 0, Y: 0})},
		domain.SectorStatics{Stations: []domain.Station{station}})

	require.NoError(t, sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: stationRef(9),
	}).Err)

	var killed bool
	for i := 0; i < 8 && !killed; i++ {
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			if imp.Killed {
				killed = true
			}
		}
	}
	require.True(t, killed, "missile must kill the fragile station")

	snap := w.Snapshot(testSector)
	require.Empty(t, snap.Missiles)
	_, alive := findDestructible(snap, stationRef(9))
	require.False(t, alive, "killed station reaped from the combat set")
	require.Empty(t, snap.Statics.Stations, "killed station gone from the rendered layout")
}

// TestUnit_TickMissiles_LosesStaticTargetThenExpires: a static destroyed by a
// hostile laser while a missile is in flight is lost as a target — the missile
// falls back to its LastTargetPos and, never reaching it, dies on TTL with no
// further damage (TASK-113 FR-08 "потеря цели → fallback LastTargetPos + TTL").
func TestUnit_TickMissiles_LosesStaticTargetThenExpires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Fragile station far away so the missile cannot reach it before it is
	// destroyed by the co-located laser on the first tick.
	station := stationStatic(9, ownerPtr(7), domain.Vec2{X: 5000, Y: 0}, 10, 0, 0, 0)
	launcher := missileShip(1, 100, domain.Vec2{X: 0, Y: 0})
	// A hostile laser parked on the station one-shots it (player 100 != owner 7).
	laser := staticAttacker(3, 100, domain.Vec2{X: 5000, Y: 0}, 1000, stationRef(9))

	w := missileStaticWorker(t, clk, []domain.Ship{launcher, laser},
		domain.SectorStatics{Stations: []domain.Station{station}},
		sector.WithHostility(ownerBasedHostility))

	require.NoError(t, sendMissile(t, w, sector.LaunchMissileCommand{
		PlayerID: 100, ShipID: 1, Target: stationRef(9),
	}).Err)
	// The launch tick already fired the laser (fireLasers runs before
	// tickMissiles), so the station is dead and the missile is in flight.
	_, alive := findDestructible(w.Snapshot(testSector), stationRef(9))
	require.False(t, alive, "laser destroyed the station on the launch tick")
	require.Len(t, w.Snapshot(testSector).Missiles, 1, "missile is in flight with a now-dead target")

	var expired bool
	for i := 0; i < 30 && !expired; i++ {
		clk.Advance(time.Second)
		w.Tick(ctx)
		for _, imp := range w.Snapshot(testSector).MissileImpacts {
			require.False(t, imp.Killed, "a lost-target missile never lands a kill")
			if imp.Expired {
				expired = true
			}
		}
	}
	require.True(t, expired, "missile with a lost static target expires on TTL")
	require.Empty(t, w.Snapshot(testSector).Missiles)
}
