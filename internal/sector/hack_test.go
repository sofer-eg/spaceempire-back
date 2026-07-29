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

// fakeRobber is a test StationRobber: it records the call and returns a canned
// result/error so the command's gating, energy, loot and journal side-effects
// can be asserted without a market or DB.
type fakeRobber struct {
	result     sector.RobResult
	err        error
	called     bool
	gotStation domain.EntityRef
	gotRace    domain.RaceID
	gotHacker  domain.PlayerID
	gotShip    domain.EntityRef
	gotDeposit bool
	gotLoot    sector.LootDrop
}

func (f *fakeRobber) Rob(_ context.Context, station domain.EntityRef, race domain.RaceID,
	hacker domain.PlayerID, ship domain.EntityRef, deposit bool,
	loot sector.LootDrop) (sector.RobResult, error) {
	f.called = true
	f.gotStation = station
	f.gotRace = race
	f.gotHacker = hacker
	f.gotShip = ship
	f.gotDeposit = deposit
	f.gotLoot = loot
	return f.result, f.err
}

const (
	hackPlayer   = domain.PlayerID(7)
	hackShipID   = domain.ShipID(1)
	hackStatID   = int64(3)
	hackGoodType = domain.GoodsTypeID(2)
)

// hackShip is a stationary hacker: up_hide (so it is cloaked) + up_hack at the
// given level, plenty of energy, parked at the origin.
func hackShip(hackLevel int) domain.Ship {
	eq := []domain.InstalledEquipment{{EquipmentID: 90, Type: "up_hide", Level: 1}}
	if hackLevel > 0 {
		eq = append(eq, domain.InstalledEquipment{EquipmentID: 122, Type: "up_hack", Level: hackLevel})
	}
	return domain.Ship{
		ID: hackShipID, PlayerID: hackPlayer, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, Energy: 500, MaxEnergy: 500,
		Equipment: eq,
	}
}

// hackStation is a built Argon (race 1) trade station 10 units from the origin.
func hackStation() domain.TradeStation {
	return domain.TradeStation{
		ID: domain.TradeStationID(hackStatID), Type: 1, SectorID: testSector,
		Pos: domain.Vec2{X: 10, Y: 0}, Race: 1, Built: true,
	}
}

// hackProductionStation is the same target as a production factory (owner_kind
// 2) — the scope-extension carries the real stock on live data.
func hackProductionStation() domain.Station {
	return domain.Station{
		ID: domain.StationID(hackStatID), Type: 1, SectorID: testSector,
		Pos: domain.Vec2{X: 10, Y: 0}, Race: 1, Built: true,
	}
}

// tsStatics / stStatics wrap the single hack target in a SectorStatics.
func tsStatics(ts ...domain.TradeStation) domain.SectorStatics {
	return domain.SectorStatics{TradeStations: ts}
}
func stStatics(st ...domain.Station) domain.SectorStatics {
	return domain.SectorStatics{Stations: st}
}

func hackWorker(t *testing.T, ship domain.Ship, statics domain.SectorStatics, opts ...sector.Option) *sector.Worker {
	t.Helper()
	cfg := sector.Config{TickInterval: time.Second, AOIRadius: 2000, HackRange: 50}
	all := map[domain.SectorID]domain.SectorStatics{testSector: statics}
	opts = append(opts, sector.WithStatics(all))
	return sector.NewWorker(0, cfg, clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}}, opts...)
}

// sendHack routes a HackStationCommand at the given target and returns the
// worker's ack after one tick.
func sendHack(t *testing.T, w *sector.Worker, target domain.EntityRef) sector.HackResult {
	t.Helper()
	reply := make(chan sector.HackResult, 1)
	require.NoError(t, w.Send(testSector, sector.HackStationCommand{
		PlayerID: hackPlayer, ShipID: hackShipID, Target: target, EnergyCost: 100, Reply: reply,
	}))
	w.Tick(context.Background())
	select {
	case res := <-reply:
		return res
	default:
		t.Fatal("no hack ack")
		return sector.HackResult{}
	}
}

func tradeStationRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: id}
}
func prodStationRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindStation, ID: id}
}

// AC-8: no up_hack module → ErrEquipmentRequired, the robber is never consulted.
func TestUnit_Hack_NoModule_ErrEquipmentRequired(t *testing.T) {
	t.Parallel()
	robber := &fakeRobber{}
	w := hackWorker(t, hackShip(0), tsStatics(hackStation()),
		sector.WithStationRobber(robber))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	assert.ErrorIs(t, res.Err, sector.ErrEquipmentRequired)
	assert.False(t, robber.called, "gate fails before the rob")
}

// AC-8: a target that is neither a trade station nor a production station →
// ErrInvalidAttackTarget (a shipyard is not hackable).
func TestUnit_Hack_TargetNotStation_ErrInvalidTarget(t *testing.T) {
	t.Parallel()
	robber := &fakeRobber{}
	w := hackWorker(t, hackShip(1), tsStatics(hackStation()),
		sector.WithStationRobber(robber))

	res := sendHack(t, w, domain.EntityRef{Kind: domain.EntityKindShipyard, ID: hackStatID})
	assert.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
	assert.False(t, robber.called)
}

// A trade-station id that is not in this sector → ErrInvalidAttackTarget.
func TestUnit_Hack_TargetNotInSector_ErrInvalidTarget(t *testing.T) {
	t.Parallel()
	w := hackWorker(t, hackShip(1), tsStatics(hackStation()),
		sector.WithStationRobber(&fakeRobber{}))

	res := sendHack(t, w, tradeStationRef(999))
	assert.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
}

// A trade station under construction cannot be hacked.
func TestUnit_Hack_NotBuilt_ErrInvalidTarget(t *testing.T) {
	t.Parallel()
	ts := hackStation()
	ts.Built = false
	w := hackWorker(t, hackShip(1), tsStatics(ts),
		sector.WithStationRobber(&fakeRobber{}))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	assert.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
}

// AC-8: a pirate (race 6) trade station cannot be hacked.
func TestUnit_Hack_PirateRace_ErrInvalidTarget(t *testing.T) {
	t.Parallel()
	ts := hackStation()
	ts.Race = 6
	robber := &fakeRobber{}
	w := hackWorker(t, hackShip(1), tsStatics(ts),
		sector.WithStationRobber(robber))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	assert.ErrorIs(t, res.Err, sector.ErrInvalidAttackTarget)
	assert.False(t, robber.called)
}

// The hacker must be within HackRange (50) of the station.
func TestUnit_Hack_OutOfRange_ErrHackOutOfRange(t *testing.T) {
	t.Parallel()
	ship := hackShip(1)
	ship.Pos = domain.Vec2{X: 200, Y: 0} // 190 away from the station at x=10
	robber := &fakeRobber{}
	w := hackWorker(t, ship, tsStatics(hackStation()),
		sector.WithStationRobber(robber))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	assert.ErrorIs(t, res.Err, sector.ErrHackOutOfRange)
	assert.False(t, robber.called)
}

// AC-8: below the action-energy cost → ErrNotEnoughEnergy, no rob.
func TestUnit_Hack_NotEnoughEnergy(t *testing.T) {
	t.Parallel()
	ship := hackShip(1)
	ship.Energy = 50 // < EnergyCost 100
	robber := &fakeRobber{}
	w := hackWorker(t, ship, tsStatics(hackStation()),
		sector.WithStationRobber(robber))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	assert.ErrorIs(t, res.Err, sector.ErrNotEnoughEnergy)
	assert.False(t, robber.called)
}

// AC-8: the station is too depleted (robber reports ErrHackTooLittleGoods) →
// the command rejects and spends NO energy.
func TestUnit_Hack_TooLittleGoods_NoEnergySpent(t *testing.T) {
	t.Parallel()
	robber := &fakeRobber{err: sector.ErrHackTooLittleGoods}
	w := hackWorker(t, hackShip(1), tsStatics(hackStation()),
		sector.WithStationRobber(robber))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	assert.ErrorIs(t, res.Err, sector.ErrHackTooLittleGoods)
	assert.Equal(t, 500, snapByID(w)[hackShipID].Energy, "rejected hack spends no energy")
}

// AC-9: a valid hack at up_hack level 1 debits energy, reveals the cloaked
// hacker for this tick, drops the loot as a container (level 1 never deposits to
// the hold), and journals "Похищено N ед." to the hacker.
func TestUnit_Hack_Success_Level1_Container_Energy_Reveal_Event(t *testing.T) {
	t.Parallel()
	robber := &fakeRobber{result: sector.RobResult{
		GoodsType: hackGoodType, Robbed: 150, Damaged: 50, Delivered: false,
		Container: &domain.Container{ID: 55, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0}},
	}}
	fcr := &fakeContainerRepo{}
	b := &fakeBus{}
	w := hackWorker(t, hackShip(1), tsStatics(hackStation()),
		sector.WithStationRobber(robber),
		sector.WithContainers(fcr, nil),
		sector.WithHandoff(handoffTopology(), b))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	require.NoError(t, res.Err)
	assert.Equal(t, int64(150), res.Robbed)

	// Robber consulted with the right target/race and level-1 → deposit=false.
	require.True(t, robber.called)
	assert.Equal(t, domain.RaceID(1), robber.gotRace)
	assert.False(t, robber.gotDeposit, "up_hack level 1 never deposits to the hold")
	assert.Equal(t, tradeStationRef(hackStatID), robber.gotStation)

	got := snapByID(w)[hackShipID]
	assert.Equal(t, 400, got.Energy, "action energy debited (500-100)")
	assert.True(t, got.MissileJustFired, "cloaked hacker revealed for this tick")

	// TASK-160: the container rode the rob's transaction, so the worker only adds
	// it to the live set — it must not write one of its own.
	containers := w.Snapshot(testSector).Containers
	require.Len(t, containers, 1, "the loot container is live in the sector")
	assert.Equal(t, domain.ContainerID(55), containers[0].ID)
	assert.Empty(t, fcr.spawned, "the worker no longer spawns the loot container itself")
	assert.Equal(t, testSector, robber.gotLoot.SectorID, "the worker chose where the loot lands")
	assert.False(t, robber.gotLoot.ExpiresAt.IsZero(), "the drop carries the container TTL")

	ev := hackedEvent(t, b)
	require.NotNil(t, ev, "hacker got a journal event")
	assert.Equal(t, int64(150), ev.Robbed)
	assert.Equal(t, hackGoodType, ev.GoodsType)
	assert.Equal(t, domain.RaceID(1), ev.Race)
}

// AC-9 scope extension: a production station (EntityKindStation) is a valid
// hack target too — same gates and effect, robbed with the factory's race.
func TestUnit_Hack_ProductionStation_Success(t *testing.T) {
	t.Parallel()
	robber := &fakeRobber{result: sector.RobResult{
		GoodsType: hackGoodType, Robbed: 705, Damaged: 235, Delivered: false,
		Container: &domain.Container{ID: 56, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0}},
	}}
	fcr := &fakeContainerRepo{}
	b := &fakeBus{}
	w := hackWorker(t, hackShip(1), stStatics(hackProductionStation()),
		sector.WithStationRobber(robber),
		sector.WithContainers(fcr, nil),
		sector.WithHandoff(handoffTopology(), b))

	res := sendHack(t, w, prodStationRef(hackStatID))
	require.NoError(t, res.Err)
	assert.Equal(t, int64(705), res.Robbed)

	require.True(t, robber.called)
	assert.Equal(t, prodStationRef(hackStatID), robber.gotStation, "production station targeted")
	assert.Equal(t, domain.RaceID(1), robber.gotRace, "factory race drives the penalty")

	require.Len(t, w.Snapshot(testSector).Containers, 1, "the loot container is live in the sector")
	assert.Empty(t, fcr.spawned, "the worker no longer spawns the loot container itself")

	ev := hackedEvent(t, b)
	require.NotNil(t, ev)
	assert.Equal(t, int64(705), ev.Robbed)
}

// AC-9: at up_hack level 2 the loot goes to the hold (Delivered) so NO container
// is dropped; the journal event still fires.
func TestUnit_Hack_Success_Level2_Delivered_NoContainer(t *testing.T) {
	t.Parallel()
	robber := &fakeRobber{result: sector.RobResult{
		GoodsType: hackGoodType, Robbed: 150, Damaged: 50, Delivered: true,
	}}
	fcr := &fakeContainerRepo{}
	b := &fakeBus{}
	w := hackWorker(t, hackShip(2), tsStatics(hackStation()),
		sector.WithStationRobber(robber),
		sector.WithContainers(fcr, nil),
		sector.WithHandoff(handoffTopology(), b))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	require.NoError(t, res.Err)
	assert.True(t, robber.gotDeposit, "up_hack level 2 deposits to the hold")
	assert.Empty(t, w.Snapshot(testSector).Containers, "loot delivered to the hold → no container")
	assert.Empty(t, fcr.spawned)

	ev := hackedEvent(t, b)
	require.NotNil(t, ev)
	assert.Equal(t, int64(150), ev.Robbed)
}

// AC-9 (unsuccessful branch): when nothing is stolen (only damage lands) no
// container drops, energy is still spent, and the journal event carries Robbed=0
// so the SPA logs "Неудачная попытка взлома".
func TestUnit_Hack_Robbed0_Unsuccessful_NoContainer(t *testing.T) {
	t.Parallel()
	robber := &fakeRobber{result: sector.RobResult{
		GoodsType: hackGoodType, Robbed: 0, Damaged: 25, Delivered: false,
	}}
	fcr := &fakeContainerRepo{}
	b := &fakeBus{}
	w := hackWorker(t, hackShip(1), tsStatics(hackStation()),
		sector.WithStationRobber(robber),
		sector.WithContainers(fcr, nil),
		sector.WithHandoff(handoffTopology(), b))

	res := sendHack(t, w, tradeStationRef(hackStatID))
	require.NoError(t, res.Err)
	assert.Equal(t, int64(0), res.Robbed)
	assert.Empty(t, w.Snapshot(testSector).Containers, "nothing stolen → no loot container")
	assert.Empty(t, fcr.spawned)
	assert.Equal(t, 400, snapByID(w)[hackShipID].Energy, "energy still spent on the attempt")

	ev := hackedEvent(t, b)
	require.NotNil(t, ev)
	assert.Equal(t, int64(0), ev.Robbed, "Robbed=0 drives the 'unsuccessful' journal line")
}

// hackedEvent returns the StationHackedEvent published to the hacker, or nil.
func hackedEvent(t *testing.T, b *fakeBus) *sector.StationHackedEvent {
	t.Helper()
	for _, msg := range b.snapshot() {
		if msg.topic == sector.StationHackedTopic(hackPlayer) {
			var ev sector.StationHackedEvent
			require.NoError(t, json.Unmarshal(msg.payload, &ev))
			return &ev
		}
	}
	return nil
}
