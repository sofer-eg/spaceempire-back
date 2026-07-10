package sector_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// knockPositions is the Type→slot lookup the worker's knock roll classifies with.
var knockTestPositions = map[string]int{"up_launcher": 2, "up_shield": 1}

// knockAttacker is a stationary ship whose laser deals 1 damage per tick to
// ship 2 — enough to trigger the DestroyModule roll on a hit without killing the
// target quickly.
func knockAttacker() domain.Ship {
	return domain.Ship{
		ID: 1, PlayerID: 7, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		LaserDamage: 1, LaserRange: 1000, LaserEnergyCost: 0,
		Energy: 100, MaxEnergy: 100,
		AttackTarget: &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	}
}

// knockWorker builds a worker with the knock config wired (Positions + faithful
// scalars via withDefaults) and a deterministic all-zero RNG, so every chance
// roll hits and every selection picks the first candidate.
func knockWorker(t *testing.T, ships []domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	cfg := sector.Config{
		TickInterval: time.Second, AOIRadius: 2000,
		Knock: combat.KnockConfig{Positions: knockTestPositions},
	}
	opts = append(opts, sector.WithRNG(staticRNG{v: 0.0}))
	return sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships}, opts...)
}

func snapByID(w *sector.Worker) map[domain.ShipID]domain.Ship {
	out := map[domain.ShipID]domain.Ship{}
	for _, s := range w.Snapshot(testSector).Ships {
		out[s.ID] = s
	}
	return out
}

// AC-1: a target whose shield is above the critical charge keeps every module,
// even under fire.
func TestUnit_Knock_ShieldUp_NoKnock(t *testing.T) {
	t.Parallel()
	target := domain.Ship{
		ID: 2, PlayerID: 99, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0},
		HP: 1000, MaxHP: 1000, Shield: 100, MaxShield: 100, ShieldRecharge: 0,
		Equipment: []domain.InstalledEquipment{{EquipmentID: 42, Type: "up_launcher", Level: 1}},
	}
	w := knockWorker(t, []domain.Ship{knockAttacker(), target})
	w.Tick(context.Background())

	got := snapByID(w)[2]
	assert.Len(t, got.Equipment, 1, "shield up → no module knocked")
}

// AC-2: with the shield down, a hit strips an external (Position 2) module for
// good and the owner is notified on the bus (journal event).
func TestUnit_Knock_ShieldDown_KnocksExternal_AndNotifies(t *testing.T) {
	t.Parallel()
	target := domain.Ship{
		ID: 2, PlayerID: 99, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0},
		HP: 1000, MaxHP: 1000, Shield: 0, MaxShield: 0, // no shield → fully down
		Equipment: []domain.InstalledEquipment{{EquipmentID: 42, Type: "up_launcher", Level: 1}},
	}
	bus := &fakeBus{}
	w := knockWorker(t, []domain.Ship{knockAttacker(), target},
		sector.WithHandoff(handoffTopology(), bus))
	w.Tick(context.Background())

	got := snapByID(w)[2]
	assert.Empty(t, got.Equipment, "external module knocked off for good")
	assert.Equal(t, 999, got.HP, "target survived the hit that knocked the module")

	var knocked *sector.ModuleKnockedEvent
	for _, msg := range bus.snapshot() {
		if msg.topic == sector.ModuleKnockedTopic(99) {
			var ev sector.ModuleKnockedEvent
			require.NoError(t, json.Unmarshal(msg.payload, &ev))
			knocked = &ev
		}
	}
	require.NotNil(t, knocked, "owner got a module-knocked journal event")
	assert.Equal(t, "up_launcher", knocked.Type)
	assert.Equal(t, domain.ShipID(2), knocked.ShipID)
}

// seqRNG returns queued Float64 values in order, then 0.0 once drained (by which
// point the ship has no equipment left to knock, so no further roll is expected).
type seqRNG struct {
	vals []float64
	i    int
}

func (r *seqRNG) Float64() float64 {
	if r.i >= len(r.vals) {
		return 0.0
	}
	v := r.vals[r.i]
	r.i++
	return v
}

// reviveRefitter simulates the app refitter: it unconditionally recomputes
// MaxShield/ShieldRecharge from the class base (which does NOT require up_shield),
// so on its own it would "revive" a collapsed shield after any knockoff.
type reviveRefitter struct{ maxShield, shieldRecharge int }

func (r reviveRefitter) Refit(ship *domain.Ship) {
	ship.MaxShield = r.maxShield
	ship.ShieldRecharge = r.shieldRecharge
}

// CRITICAL #2 regression: once up_shield is knocked off, a LATER unrelated
// knockoff must NOT revive the shield via the refit. The durable
// ShieldGeneratorDestroyed marker re-collapses MaxShield after every refit.
func TestUnit_Knock_CrossEvent_ShieldStaysCollapsed(t *testing.T) {
	t.Parallel()
	target := domain.Ship{
		ID: 2, PlayerID: 99, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0},
		HP: 50, MaxHP: 100, Shield: 0, MaxShield: 100, ShieldRecharge: 0,
		Equipment: []domain.InstalledEquipment{
			{EquipmentID: 60, Type: "up_shield", Level: 1},
			{EquipmentID: 42, Type: "up_launcher", Level: 1},
		},
	}
	// tick1: external miss (0.9), internal hit (0.0)+select(0.0) → up_shield only.
	// tick2: external hit (0.0)+select(0.0) → up_launcher; internal hit (0.0) has
	// no Position-1 candidate left → no select roll.
	rng := &seqRNG{vals: []float64{0.9, 0.0, 0.0, 0.0, 0.0, 0.0}}
	cfg := sector.Config{
		TickInterval: time.Second, AOIRadius: 2000,
		Knock: combat.KnockConfig{Positions: knockTestPositions},
	}
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {knockAttacker(), target}},
		sector.WithRNG(rng),
		sector.WithRefit(reviveRefitter{maxShield: 100, shieldRecharge: 10}),
	)

	w.Tick(context.Background()) // tick1: up_shield knocked → collapse
	got := snapByID(w)[2]
	require.Len(t, got.Equipment, 1, "up_shield gone, up_launcher remains")
	require.Equal(t, 0, got.MaxShield, "tick1: generator destroyed")

	w.Tick(context.Background()) // tick2: up_launcher knocked, refit runs again
	got = snapByID(w)[2]
	assert.Equal(t, 0, got.MaxShield, "marker keeps shield collapsed after the refit revive")
	assert.Equal(t, 0, got.Shield, "no revival")

	w.Tick(context.Background())
	w.Tick(context.Background())
	assert.Equal(t, 0, snapByID(w)[2].Shield, "shield never regenerates")
}

// CRITICAL #1 regression: a knockoff must persist through the equipment path
// (equipment + max_shield + marker), not the dynamic Save path — otherwise
// cold-start reloads the module and the restored shield.
func TestUnit_Knock_PersistsEquipmentAndMarker(t *testing.T) {
	t.Parallel()
	target := domain.Ship{
		ID: 2, PlayerID: 99, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0},
		HP: 50, MaxHP: 100, Shield: 0, MaxShield: 100, ShieldRecharge: 0,
		Equipment: []domain.InstalledEquipment{{EquipmentID: 60, Type: "up_shield", Level: 1}},
	}
	repo := &fakeShipRepo{}
	cfg := sector.Config{
		TickInterval: time.Second, AOIRadius: 2000,
		Knock: combat.KnockConfig{Positions: knockTestPositions},
	}
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{testSector: {knockAttacker(), target}},
		sector.WithRNG(staticRNG{v: 0.0}),
	)
	w.Tick(context.Background())

	saved := repo.savedEquipmentFor(2)
	require.NotNil(t, saved, "knock persisted via the equipment path")
	assert.Empty(t, saved.Equipment, "up_shield removed is persisted")
	assert.Equal(t, 0, saved.MaxShield, "collapsed max_shield persisted")
	assert.True(t, saved.ShieldGeneratorDestroyed, "marker persisted")
}

// AC-3: with the shield down AND the hull pierced, up_shield can be knocked off;
// once it is, the shield collapses to 0 and never regenerates again.
func TestUnit_Knock_HullDown_KnocksShield_CollapsesAndNoRegen(t *testing.T) {
	t.Parallel()
	target := domain.Ship{
		ID: 2, PlayerID: 99, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0},
		HP: 50, MaxHP: 100, // hull 0.5 ≤ 0.7 → internal modules eligible
		Shield: 0, MaxShield: 100, ShieldRecharge: 10, // shield regenerates 10/tick…
		Equipment: []domain.InstalledEquipment{{EquipmentID: 60, Type: "up_shield", Level: 1}},
	}
	w := knockWorker(t, []domain.Ship{knockAttacker(), target})

	// Tick 1: shield charges to 10 (proving it works), the laser hits, and the
	// DestroyModule roll knocks up_shield → the generator collapses.
	w.Tick(context.Background())
	got := snapByID(w)[2]
	assert.Empty(t, got.Equipment, "up_shield knocked off")
	assert.Equal(t, 0, got.MaxShield, "shield generator destroyed → MaxShield 0")
	assert.Equal(t, 0, got.Shield, "shield collapsed to 0")

	// Tick 2: the shield must NOT regenerate even though it charged 10/tick before.
	w.Tick(context.Background())
	assert.Equal(t, 0, snapByID(w)[2].Shield, "no regeneration without up_shield")
}
