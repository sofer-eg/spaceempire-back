package sector_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

const (
	captPlayer   = domain.PlayerID(7)
	captShipID   = domain.ShipID(1)
	captTargetID = domain.ShipID(2)
	captOldOwner = domain.PlayerID(9)
)

// captureAttacker is a stationary attacker with up_capture at the origin, plenty
// of energy. captureLevel 0 leaves the module off (equipment gate test).
func captureAttacker(captureLevel int) domain.Ship {
	var eq []domain.InstalledEquipment
	if captureLevel > 0 {
		eq = append(eq, domain.InstalledEquipment{EquipmentID: 47, Type: "up_capture", Level: captureLevel})
	}
	return domain.Ship{
		ID: captShipID, PlayerID: captPlayer, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, Energy: 500, MaxEnergy: 500,
		Equipment: eq,
	}
}

// captureTarget is a shield-stripped (MaxShield 0) enemy ship 10 units away, owned
// by captOldOwner, of the given race, MaxHP 160.
func captureTarget(race domain.RaceID) domain.Ship {
	return domain.Ship{
		ID: captTargetID, PlayerID: captOldOwner, SectorID: testSector, Race: race,
		Pos: domain.Vec2{X: 10, Y: 0}, HP: 160, MaxHP: 160, MaxShield: 0,
	}
}

// atWar wires a relations oracle where the attacker and the target owner are at war.
func atWar() fakeRelations {
	return fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{
		{captPlayer, captOldOwner}: domain.RelationAtWar,
	}}
}

func captureWorker(t *testing.T, attacker, target domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	cfg := sector.Config{
		TickInterval: time.Second, AOIRadius: 2000,
		CaptureChance: 819, KhaakCaptureChance: 876, CaptureRange: 50,
	}
	return sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {attacker, target}}, opts...)
}

func sendCapture(t *testing.T, w *sector.Worker, target domain.ShipID) sector.CaptureResult {
	t.Helper()
	reply := make(chan sector.CaptureResult, 1)
	require.NoError(t, w.Send(testSector, sector.CaptureShipCommand{
		PlayerID: captPlayer, ShipID: captShipID,
		Target:     domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target)},
		EnergyCost: 100, Reply: reply,
	}))
	w.Tick(context.Background())
	select {
	case res := <-reply:
		return res
	default:
		t.Fatal("no capture ack")
		return sector.CaptureResult{}
	}
}

// AC-4: the target still has a working shield generator (MaxShield > 0) → reject,
// the owner is unchanged and no roll is made.
func TestUnit_Capture_ShieldUp_ErrCaptureShielded(t *testing.T) {
	t.Parallel()
	target := captureTarget(0)
	target.MaxShield = 100 // shield generator alive
	w := captureWorker(t, captureAttacker(1), target,
		sector.WithRelations(atWar()), sector.WithRNG(staticRNG{v: 0.99}))

	res := sendCapture(t, w, captTargetID)
	assert.ErrorIs(t, res.Err, sector.ErrCaptureShielded)
	assert.Equal(t, captOldOwner, snapByID(w)[captTargetID].PlayerID, "owner unchanged")
}

// AC-5: no up_capture module → ErrEquipmentRequired.
func TestUnit_Capture_NoModule_ErrEquipmentRequired(t *testing.T) {
	t.Parallel()
	w := captureWorker(t, captureAttacker(0), captureTarget(0),
		sector.WithRelations(atWar()), sector.WithRNG(staticRNG{v: 0.99}))

	res := sendCapture(t, w, captTargetID)
	assert.ErrorIs(t, res.Err, sector.ErrEquipmentRequired)
	assert.Equal(t, captOldOwner, snapByID(w)[captTargetID].PlayerID)
}

// AC-5: target farther than capture_range (50) → ErrCaptureOutOfRange.
func TestUnit_Capture_OutOfRange(t *testing.T) {
	t.Parallel()
	target := captureTarget(0)
	target.Pos = domain.Vec2{X: 200, Y: 0} // 200 away, > 50
	w := captureWorker(t, captureAttacker(1), target,
		sector.WithRelations(atWar()), sector.WithRNG(staticRNG{v: 0.99}))

	res := sendCapture(t, w, captTargetID)
	assert.ErrorIs(t, res.Err, sector.ErrCaptureOutOfRange)
}

// AC-5 (#2): a FRIENDLY target (same clan / declared friend) → ErrInvalidAttackTarget.
// The gate is damage-parity (!shipsAreFriendly), so only allies are off-limits.
func TestUnit_Capture_Friendly_ErrInvalidTarget(t *testing.T) {
	t.Parallel()
	friendly := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{
		{captPlayer, captOldOwner}: domain.RelationFriend,
	}}
	w := captureWorker(t, captureAttacker(1), captureTarget(0),
		sector.WithRelations(friendly), sector.WithRNG(staticRNG{v: 0.99}))

	res := sendCapture(t, w, captTargetID)
	assert.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
	assert.Equal(t, captOldOwner, snapByID(w)[captTargetID].PlayerID, "an ally is never captured")
}

// AC-5 (#5, C-06 main case): a NON-friendly target — a neutral player or an NPC
// ship (all NPC factions share the __npc__ owner, so the player/clan oracle reports
// Neutral, not Friend) — passes the damage-parity hostility gate and, with its
// shield down and a success roll, is captured. This is the ЧТЗ C-06 main case that
// the earlier IsHostile gate (declared-war only) broke.
func TestUnit_Capture_NonFriendlyNeutral_Captured(t *testing.T) {
	t.Parallel()
	rep := &fakeReputationAwarder{}
	w := captureWorker(t, captureAttacker(1), captureTarget(0),
		sector.WithRelations(fakeRelations{}), // no declared relation → Neutral (not Friend)
		sector.WithRNG(staticRNG{v: 0.9}),     // roll 900 > 819 → success
		sector.WithReputation(rep))

	res := sendCapture(t, w, captTargetID)
	require.NoError(t, res.Err)
	assert.True(t, res.Captured, "a non-friendly, shield-stripped ship is capturable")
	assert.Equal(t, captPlayer, snapByID(w)[captTargetID].PlayerID, "target re-owned to the attacker")
}

// AC-5: below the action-energy cost → ErrNotEnoughEnergy, no capture.
func TestUnit_Capture_NotEnoughEnergy(t *testing.T) {
	t.Parallel()
	att := captureAttacker(1)
	att.Energy = 50 // < EnergyCost 100
	w := captureWorker(t, att, captureTarget(0),
		sector.WithRelations(atWar()), sector.WithRNG(staticRNG{v: 0.99}))

	res := sendCapture(t, w, captTargetID)
	assert.ErrorIs(t, res.Err, sector.ErrNotEnoughEnergy)
	assert.Equal(t, captOldOwner, snapByID(w)[captTargetID].PlayerID)
}

// AC-6: seed "success" → the target changes owner, energy is spent, the reputation
// awarder is credited with the captured race (read BEFORE the owner change zeroes
// it), and both players get a capture journal event. The target's crew eject event
// (ShipCapturedTopic) fires too.
func TestUnit_Capture_Success_ReownsCreditsAndJournals(t *testing.T) {
	t.Parallel()
	rep := &fakeReputationAwarder{}
	b := &fakeBus{}
	target := captureTarget(1) // NPC race 1 (Argon) hull, at war with the attacker
	target.PassengerPlayers = []domain.PlayerID{100}
	w := captureWorker(t, captureAttacker(1), target,
		sector.WithRelations(atWar()),
		sector.WithRNG(staticRNG{v: 0.9}), // roll 900 > 819 → success
		sector.WithReputation(rep),
		sector.WithHandoff(handoffTopology(), b))

	res := sendCapture(t, w, captTargetID)
	require.NoError(t, res.Err)
	assert.True(t, res.Captured)

	got := snapByID(w)[captTargetID]
	assert.Equal(t, captPlayer, got.PlayerID, "target re-owned to the attacker")
	assert.Equal(t, domain.RaceID(0), got.Race, "captured ship neutralised")
	assert.Equal(t, 400, snapByID(w)[captShipID].Energy, "action energy spent (500-100)")

	require.Len(t, rep.captures, 1, "capturer credited once")
	assert.Equal(t, captPlayer, rep.captures[0].capturer)
	assert.Equal(t, domain.RaceID(1), rep.captures[0].race, "captured race read before neutralisation")

	// Crew eject (from changeShipOwner) and both journal events fired.
	require.NotNil(t, capturedEjectEvent(t, b), "old crew eject event published")
	attEv := captureJournal(t, b, captPlayer)
	require.NotNil(t, attEv, "attacker journal event")
	assert.True(t, attEv.Captor)
	assert.True(t, attEv.Success)
	ownEv := captureJournal(t, b, captOldOwner)
	require.NotNil(t, ownEv, "old owner journal event")
	assert.False(t, ownEv.Captor)
	assert.True(t, ownEv.Success)
}

// AC-6 (Kha'ak threshold): a Kha'ak target (race 8) uses the higher
// khaak_capture_chance (876), so a roll that would succeed against a normal target
// (850 > 819) fails against Kha'ak (850 < 876) — proving the race-specific threshold.
func TestUnit_Capture_Khaak_UsesHigherThreshold(t *testing.T) {
	t.Parallel()
	rep := &fakeReputationAwarder{}
	target := captureTarget(8) // Kha'ak
	w := captureWorker(t, captureAttacker(1), target,
		sector.WithRelations(atWar()),
		sector.WithRNG(staticRNG{v: 0.85}), // roll 850: > 819 (normal) but < 876 (khaak)
		sector.WithReputation(rep))

	res := sendCapture(t, w, captTargetID)
	require.NoError(t, res.Err)
	assert.False(t, res.Captured, "roll 850 < khaak threshold 876 → capture fails")
	assert.Equal(t, captOldOwner, snapByID(w)[captTargetID].PlayerID, "owner unchanged on failed khaak capture")
	assert.Empty(t, rep.captures, "no reputation credit on a failed capture")
}

// AC-7: seed "failure" with a low hull → the target is destroyed (killShip), energy
// is still spent, and the attacker gets a "failed" journal event.
func TestUnit_Capture_Failure_KillsWeakTarget(t *testing.T) {
	t.Parallel()
	b := &fakeBus{}
	target := captureTarget(0)
	target.HP = 5 // maxHP/16 = 10 >= 5 → destroyed
	w := captureWorker(t, captureAttacker(1), target,
		sector.WithRelations(atWar()),
		sector.WithRNG(staticRNG{v: 0.1}), // roll 100 < 819 → failure
		sector.WithHandoff(handoffTopology(), b))

	res := sendCapture(t, w, captTargetID)
	require.NoError(t, res.Err)
	assert.False(t, res.Captured)
	_, alive := snapByID(w)[captTargetID]
	assert.False(t, alive, "weak target destroyed by the failed capture")
	assert.Equal(t, 400, snapByID(w)[captShipID].Energy, "energy spent even on failure")

	ev := captureJournal(t, b, captPlayer)
	require.NotNil(t, ev)
	assert.True(t, ev.Captor)
	assert.False(t, ev.Success, "failure journal line")
}

// AC-7: seed "failure" with a healthy hull → the target survives with maxHP/16
// hull damage; energy is still spent.
func TestUnit_Capture_Failure_DamagesHealthyTarget(t *testing.T) {
	t.Parallel()
	target := captureTarget(0)
	target.HP = 100 // maxHP/16 = 10 < 100 → survives at 90
	w := captureWorker(t, captureAttacker(1), target,
		sector.WithRelations(atWar()),
		sector.WithRNG(staticRNG{v: 0.1}))

	res := sendCapture(t, w, captTargetID)
	require.NoError(t, res.Err)
	assert.False(t, res.Captured)
	got := snapByID(w)[captTargetID]
	assert.Equal(t, 90, got.HP, "hull docked by maxHP/16")
	assert.Equal(t, captOldOwner, got.PlayerID, "owner unchanged on failure")
	assert.Equal(t, 400, snapByID(w)[captShipID].Energy)
}

// capturedEjectEvent returns the ShipCapturedEvent (crew eject) published to the
// global topic, or nil.
func capturedEjectEvent(t *testing.T, b *fakeBus) *sector.ShipCapturedEvent {
	t.Helper()
	for _, msg := range b.snapshot() {
		if msg.topic == sector.ShipCapturedTopic {
			var ev sector.ShipCapturedEvent
			require.NoError(t, json.Unmarshal(msg.payload, &ev))
			return &ev
		}
	}
	return nil
}

// captureJournal returns the ShipCaptureEvent published to the given player's
// per-player journal topic, or nil.
func captureJournal(t *testing.T, b *fakeBus, player domain.PlayerID) *sector.ShipCaptureEvent {
	t.Helper()
	for _, msg := range b.snapshot() {
		if msg.topic == sector.ShipCaptureTopic(player) {
			var ev sector.ShipCaptureEvent
			require.NoError(t, json.Unmarshal(msg.payload, &ev))
			return &ev
		}
	}
	return nil
}
