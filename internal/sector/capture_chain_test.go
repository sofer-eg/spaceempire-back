package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TestUnit_CaptureChain_KnockShieldThenCapture is the cross-mechanism acceptance
// scenario (ЧТЗ doc-4 §5.2, TASK-100.3.9.7): it proves that module knockoff
// (TASK-100.3.9.1) and capture (TASK-100.3.9.4) COMPOSE — a capture is impossible
// while the target's shield generator lives, and becomes possible once weapon fire
// strips up_shield off. The per-subtask tests cover each mechanic in isolation;
// only this one exercises the full chain through the worker's combat path:
//
//  1. a live shield generator (MaxShield > 0) blocks the capture (ErrCaptureShielded);
//  2. a series of laser hits drops the shield to <=20% and pierces the hull <70%,
//     and the DestroyModule roll knocks up_shield off → MaxShield forced to 0;
//  3. the SAME capture command now passes the shield gate and, on a success roll,
//     re-owns the ship (changeShipOwner), spends the action energy, credits +war,
//     and ejects the old crew.
//
// The rng is scripted so the whole chain is deterministic (NFR-001/004). Consumption
// order: tick1 external-chance (miss); tick2 external-chance (miss) + internal-chance
// (hit) + internal-select (up_shield); then the capture roll (0.9 → success).
func TestUnit_CaptureChain_KnockShieldThenCapture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Attacker: a laser to strip the shield + up_capture to seize the hull.
	attacker := domain.Ship{
		ID: captShipID, PlayerID: captPlayer, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		LaserDamage: 50, LaserRange: 1000, LaserEnergyCost: 0,
		Energy: 500, MaxEnergy: 500,
		Equipment:    []domain.InstalledEquipment{{EquipmentID: 47, Type: "up_capture", Level: 1}},
		AttackTarget: &domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(captTargetID)},
	}
	// Target: an internal up_shield generator, full shield + full hull, 10 units away.
	target := domain.Ship{
		ID: captTargetID, PlayerID: captOldOwner, SectorID: testSector, Race: 0,
		Pos: domain.Vec2{X: 10, Y: 0},
		HP:  100, MaxHP: 100, Shield: 60, MaxShield: 60, ShieldRecharge: 0,
		Equipment: []domain.InstalledEquipment{{EquipmentID: 60, Type: "up_shield", Level: 1}},
	}

	rep := &fakeReputationAwarder{}
	b := &fakeBus{}
	rng := &seqRNG{vals: []float64{0.5, 0.5, 0.0, 0.0, 0.9}}
	cfg := sector.Config{
		TickInterval: time.Second, AOIRadius: 2000,
		Knock:         combat.KnockConfig{Positions: knockTestPositions},
		CaptureChance: 819, KhaakCaptureChance: 876, CaptureRange: 50,
	}
	w := sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {attacker, target}},
		sector.WithRelations(fakeRelations{}), // neutral: not friendly → fire + capture allowed
		sector.WithRNG(rng),
		sector.WithReputation(rep),
		sector.WithHandoff(handoffTopology(), b))

	capture := func(reply chan sector.CaptureResult) {
		require.NoError(t, w.Send(testSector, sector.CaptureShipCommand{
			PlayerID: captPlayer, ShipID: captShipID,
			Target:     domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(captTargetID)},
			EnergyCost: 100, Reply: reply,
		}))
	}

	// (1) Pre-knockoff: the live shield generator blocks the capture. The rejection
	// is applied at the start of this tick (before the laser fires), spending no rng.
	reply1 := make(chan sector.CaptureResult, 1)
	capture(reply1)
	w.Tick(ctx) // capture#1 rejected (shield up), then laser tick1: shield 60→10
	res1 := <-reply1
	assert.ErrorIs(t, res1.Err, sector.ErrCaptureShielded, "capture blocked while the generator lives")
	assert.Equal(t, captOldOwner, snapByID(w)[captTargetID].PlayerID, "owner unchanged pre-knockoff")
	require.Len(t, snapByID(w)[captTargetID].Equipment, 1, "up_shield still installed after tick1")

	// (2) Weapon fire strips the shield and pierces the hull → DestroyModule knocks
	// up_shield off, collapsing MaxShield to 0 (the durable ShieldGeneratorDestroyed
	// marker from .1). This is what opens the ship to capture.
	w.Tick(ctx) // laser tick2: shield→0, HP→60, up_shield knocked
	knocked := snapByID(w)[captTargetID]
	require.Empty(t, knocked.Equipment, "up_shield knocked off by weapon fire")
	require.Equal(t, 0, knocked.MaxShield, "shield generator destroyed → MaxShield 0 opens capture")

	// (3) Post-knockoff: the identical capture now passes the shield gate and, on the
	// success roll, seizes the ship — proving .1 (knockoff) and .4 (capture) compose.
	reply2 := make(chan sector.CaptureResult, 1)
	capture(reply2)
	w.Tick(ctx)
	res2 := <-reply2
	require.NoError(t, res2.Err)
	assert.True(t, res2.Captured, "shield-stripped ship captured")

	got := snapByID(w)[captTargetID]
	assert.Equal(t, captPlayer, got.PlayerID, "target re-owned to the attacker")
	assert.Equal(t, domain.RaceID(0), got.Race, "captured ship neutralised")
	assert.Equal(t, 400, snapByID(w)[captShipID].Energy, "capture action energy spent (500-100)")
	require.Len(t, rep.captures, 1, "+war credited on the capture")
	assert.Equal(t, captPlayer, rep.captures[0].capturer)

	// The old crew's eject event (changeShipOwner) fired on the global topic.
	var ejected bool
	for _, msg := range b.snapshot() {
		if msg.topic == sector.ShipCapturedTopic {
			ejected = true
		}
	}
	assert.True(t, ejected, "captured ship's crew-eject event published")
}
