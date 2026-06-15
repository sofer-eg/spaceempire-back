package sector_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/miner"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

const (
	miningOre    = domain.GoodsTypeID(2)
	miningAstID  = domain.AsteroidID(7)
	miningPlayer = domain.PlayerID(0)
	miningShipID = domain.ShipID(1)
)

// fullHoldLogistics is a sector.MinerLogistics whose AddOre always reports a
// full hold (cargo.ErrNoSpace) — used to prove the up_drill L2 deposit falls
// back to a container drop when the ship cannot hold more ore.
type fullHoldLogistics struct{ calls int }

func (f *fullHoldLogistics) AddOre(context.Context, domain.EntityRef, domain.GoodsTypeID, int64) error {
	f.calls++
	return cargo.ErrNoSpace
}

// miningWorker wires a single player ship parked next to one asteroid, with the
// given up_drill level and energy. snapshotEvery=true persists asteroid mass to
// the repo every tick so tests can read the remaining mass back. A nil ore
// logistics leaves AddOre unwired (L1-style); pass a logistics for L2 tests.
func miningWorker(
	t *testing.T,
	drillLevel int,
	energy int,
	cfg sector.Config,
	containerRepo sector.ContainerRepo,
	astRepo sector.AsteroidRepo,
	logistics sector.MinerLogistics,
) *sector.Worker {
	t.Helper()
	ship := domain.Ship{
		ID: miningShipID, PlayerID: miningPlayer, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		HP: 100, MaxHP: 100, Energy: energy, MaxEnergy: 1000,
	}
	if drillLevel > 0 {
		ship.Equipment = []domain.InstalledEquipment{{Type: "up_drill", Level: drillLevel}}
	}
	asteroid := domain.Asteroid{ID: miningAstID, SectorID: testSector, Pos: domain.Vec2{X: 5, Y: 0}, Mass: 100, OreType: miningOre}

	opts := []sector.Option{
		sector.WithAsteroids(astRepo, map[domain.SectorID][]domain.Asteroid{testSector: {asteroid}}),
	}
	if containerRepo != nil {
		opts = append(opts, sector.WithContainers(containerRepo, nil))
	}
	if logistics != nil {
		opts = append(opts, sector.WithMinerLogistics(logistics))
	}
	return sector.NewWorker(0, cfg,
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		opts...,
	)
}

// npcMinerWorker wires an NPC miner (no up_drill, owner 0) parked at a home
// factory with one asteroid to drill — the same shape as the phase 5.4 E2E.
// Returns the worker and the NPC ship id. Used to prove the player up_drill gate
// does not touch the NPC mining path.
func npcMinerWorker(t *testing.T, astRepo sector.AsteroidRepo, logistics *fakeLogistics) (*sector.Worker, domain.ShipID) {
	t.Helper()
	const npcShipID = domain.ShipID(1)
	home := miner.Leg{Sector: testSector, Pos: domain.Vec2{X: 100, Y: 100}, Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}}
	target := miner.Target{ID: miningAstID, Sector: testSector, Pos: domain.Vec2{X: 60, Y: 140}}
	state, err := miner.NewInitialState(home, miningOre, target)
	require.NoError(t, err)

	registry := ai.NewRegistry()
	miner.Register(registry, miner.Config{ArriveRadius: 6, MineRange: 12, DrillRate: 5, LoadTarget: 40})

	asteroid := domain.Asteroid{ID: miningAstID, SectorID: testSector, Pos: target.Pos, Mass: 20, OreType: miningOre}
	ship := domain.Ship{
		ID: npcShipID, PlayerID: 0, SectorID: testSector,
		Pos: home.Pos, Direction: domain.Vec2{X: 1, Y: 0},
		MaxSpeed: 50, Acceleration: 50, TurnRate: math.Pi, HP: 100, MaxHP: 100,
	}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, DockRange: 3, AOIRadius: 5000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithRouter(&stubRouter{}),
		sector.WithTraderLogistics(logistics),
		sector.WithMinerLogistics(logistics),
		sector.WithAsteroids(astRepo, map[domain.SectorID][]domain.Asteroid{testSector: {asteroid}}),
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: {
			{ShipID: npcShipID, SectorID: testSector, ControllerKind: miner.Kind, StateJSON: state},
		}}),
	)
	return w, npcShipID
}

// startMining sends a MineCommand and ticks once; returns the command result.
func startMining(t *testing.T, w *sector.Worker, ast domain.AsteroidID) sector.CmdResult {
	t.Helper()
	id := ast
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.MineCommand{
		PlayerID: miningPlayer,
		ShipID:   miningShipID,
		Asteroid: &id,
		Reply:    reply,
	}))
	w.Tick(context.Background())
	return <-reply
}

func miningShip(w *sector.Worker) (domain.Ship, bool) {
	return snapshotShipByID(w.Snapshot(testSector), miningShipID)
}

// snapshotEveryCfg returns a config whose snapshot cadence is effectively
// every tick, so persistAsteroids flushes mass to the repo each tick.
func snapshotEveryCfg() sector.Config {
	return sector.Config{
		TickInterval: time.Second, SnapshotInterval: time.Nanosecond,
		AOIRadius: 2000, MineRange: 12, MineRate: 5, MineEnergyCost: 10,
	}
}

// TestUnit_Mining_PerTick_ExtractsOreAndLowersMass proves the per-tick drill
// extracts MineRate ore and reduces the asteroid mass.
func TestUnit_Mining_PerTick_ExtractsOreAndLowersMass(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	logistics := newFakeLogistics()
	cfg := snapshotEveryCfg()
	w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, astRepo, logistics)

	require.NoError(t, startMining(t, w, miningAstID).Err) // tick 1 arms + drills once
	w.Tick(context.Background())                           // tick 2 drills again

	// Two drill ticks at MineRate 5 each: 10 ore deposited, asteroid 100 -> 90.
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(miningShipID)}
	assert.Equal(t, int64(10), logistics.store[shipRef], "L2 deposit goes into the hold")
	assert.Equal(t, int64(90), astRepo.lastMass[miningAstID], "asteroid mass drops by 2*MineRate")
}

// TestUnit_Mining_EnergyGate proves a ship below the action cost does not drill,
// while one with enough energy debits the cost each tick.
func TestUnit_Mining_EnergyGate(t *testing.T) {
	t.Parallel()

	t.Run("no energy does not drill", func(t *testing.T) {
		t.Parallel()
		astRepo := newFakeAsteroidRepo()
		logistics := newFakeLogistics()
		cfg := snapshotEveryCfg()                                                 // MineEnergyCost 10
		w := miningWorker(t, 2, 5, cfg, &fakeContainerRepo{}, astRepo, logistics) // energy 5 < 10

		require.NoError(t, startMining(t, w, miningAstID).Err)

		shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(miningShipID)}
		assert.Zero(t, logistics.store[shipRef], "no ore mined without energy")
		ship, ok := miningShip(w)
		require.True(t, ok)
		assert.Equal(t, 5, ship.Energy, "energy untouched when the tick cannot drill")
		assert.NotNil(t, ship.MiningTarget, "mode stays armed, waiting for recharge")
	})

	t.Run("enough energy debits the cost", func(t *testing.T) {
		t.Parallel()
		astRepo := newFakeAsteroidRepo()
		logistics := newFakeLogistics()
		cfg := snapshotEveryCfg()
		w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, astRepo, logistics)

		require.NoError(t, startMining(t, w, miningAstID).Err) // drills once, -10 energy

		ship, ok := miningShip(w)
		require.True(t, ok)
		assert.Equal(t, 990, ship.Energy, "one drill tick debits MineEnergyCost")
	})
}

// TestUnit_Mining_DepositLevel1_DropsContainer proves up_drill level 1 deposits
// drilled ore as a loot container in space (never into the hold).
func TestUnit_Mining_DepositLevel1_DropsContainer(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	repo := &fakeContainerRepo{}
	logistics := newFakeLogistics() // wired, but level 1 must not use it
	cfg := snapshotEveryCfg()
	w := miningWorker(t, 1, 1000, cfg, repo, astRepo, logistics)

	require.NoError(t, startMining(t, w, miningAstID).Err)

	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(miningShipID)}
	assert.Zero(t, logistics.store[shipRef], "L1 never deposits into the hold")
	require.Len(t, repo.spawned, 1, "L1 drops one ore container per drill tick")
	assert.Equal(t, miningOre, repo.spawned[0].GoodsType)
	assert.Equal(t, int64(5), repo.spawned[0].Quantity, "container carries MineRate ore")
	assert.Len(t, w.Snapshot(testSector).Containers, 1, "container is live in the sector")
}

// TestUnit_Mining_DepositLevel2_HoldThenContainerFallback proves level 2 puts
// ore in the hold, and falls back to a container when the hold is full.
func TestUnit_Mining_DepositLevel2_HoldThenContainerFallback(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	repo := &fakeContainerRepo{}
	full := &fullHoldLogistics{}
	cfg := snapshotEveryCfg()
	w := miningWorker(t, 2, 1000, cfg, repo, astRepo, full)

	require.NoError(t, startMining(t, w, miningAstID).Err)

	assert.Equal(t, 1, full.calls, "L2 tries the hold first")
	require.Len(t, repo.spawned, 1, "full hold falls back to a container")
	assert.Equal(t, int64(5), repo.spawned[0].Quantity)
	assert.Equal(t, int64(95), astRepo.lastMass[miningAstID], "asteroid still mines down on fallback")
}

// TestUnit_Mining_DepletesAsteroid_ResetsMode proves draining an asteroid to
// zero deletes it and clears the ship's mining mode.
func TestUnit_Mining_DepletesAsteroid_ResetsMode(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	logistics := newFakeLogistics()
	cfg := snapshotEveryCfg()
	// Asteroid mass 100, MineRate 5 -> 20 drill ticks to deplete.
	w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, astRepo, logistics)

	require.NoError(t, startMining(t, w, miningAstID).Err)
	for i := 0; i < 25; i++ {
		w.Tick(context.Background())
	}

	assert.True(t, astRepo.deleted[miningAstID], "depleted asteroid is deleted")
	ship, ok := miningShip(w)
	require.True(t, ok)
	assert.Nil(t, ship.MiningTarget, "mining mode cleared on depletion")
}

// TestUnit_Mining_NoDrill_Rejected proves a ship without up_drill cannot start
// mining (ErrEquipmentRequired) and never drills.
func TestUnit_Mining_NoDrill_Rejected(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	logistics := newFakeLogistics()
	cfg := snapshotEveryCfg()
	w := miningWorker(t, 0, 1000, cfg, &fakeContainerRepo{}, astRepo, logistics) // no up_drill

	res := startMining(t, w, miningAstID)

	require.ErrorIs(t, res.Err, sector.ErrEquipmentRequired)
	ship, ok := miningShip(w)
	require.True(t, ok)
	assert.Nil(t, ship.MiningTarget, "rejected command leaves no mining mode")
	shipRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(miningShipID)}
	assert.Zero(t, logistics.store[shipRef])
}

// TestUnit_Mining_OutOfRange_Rejected proves a ship farther than MineRange from
// the asteroid cannot start mining. The asteroid sits at (5,0); a MineRange of 1
// puts it out of reach.
func TestUnit_Mining_OutOfRange_Rejected(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	cfg := snapshotEveryCfg()
	cfg.MineRange = 1 // asteroid at dist 5 is now out of range
	w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, astRepo, newFakeLogistics())

	res := startMining(t, w, miningAstID)

	require.ErrorIs(t, res.Err, sector.ErrAsteroidOutOfRange)
	ship, ok := miningShip(w)
	require.True(t, ok)
	assert.Nil(t, ship.MiningTarget, "rejected command leaves no mining mode")
}

// TestUnit_Mining_AsteroidNotFound_Rejected proves mining an unknown asteroid id
// is rejected.
func TestUnit_Mining_AsteroidNotFound_Rejected(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	cfg := snapshotEveryCfg()
	w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, astRepo, newFakeLogistics())

	res := startMining(t, w, domain.AsteroidID(999))

	require.ErrorIs(t, res.Err, sector.ErrAsteroidNotFound)
}

// TestUnit_Mining_NPCMinerUnaffected proves the NPC miner path (ai.Mine ->
// applyMine) still drills without an up_drill module and is never routed
// through the player tickPlayerMining (its ship keeps MiningTarget == nil).
// Regression guard for the phase 10.3.6 split: the player gate must not break
// NPC mining.
func TestUnit_Mining_NPCMinerUnaffected(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	logistics := newFakeLogistics()
	// An NPC miner ship: no up_drill equipment, owner 0, driven by ai.Mine
	// through the AI controller (set up exactly like the existing miner E2E).
	w, npcShipID := npcMinerWorker(t, astRepo, logistics)

	for i := 0; i < 40; i++ {
		w.Tick(context.Background())
	}

	assert.True(t, astRepo.deleted[miningAstID], "NPC miner still depletes its asteroid")
	ship, ok := snapshotShipByID(w.Snapshot(testSector), npcShipID)
	if ok { // ship may have despawned; if present it must never carry a player mining mode
		assert.Nil(t, ship.MiningTarget, "NPC ship is never given a player MiningTarget")
		assert.Empty(t, ship.Equipment, "NPC miner has no up_drill — proves the gate did not touch it")
	}
}

// TestUnit_Mining_Stop_ClearsMode proves a nil-asteroid MineCommand (and
// cease-fire) stops sustained mining.
func TestUnit_Mining_Stop_ClearsMode(t *testing.T) {
	t.Parallel()
	astRepo := newFakeAsteroidRepo()
	logistics := newFakeLogistics()
	cfg := snapshotEveryCfg()
	w := miningWorker(t, 2, 1000, cfg, &fakeContainerRepo{}, astRepo, logistics)

	require.NoError(t, startMining(t, w, miningAstID).Err)
	ship, ok := miningShip(w)
	require.True(t, ok)
	require.NotNil(t, ship.MiningTarget)

	// Cease-fire stops mining.
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.CeaseFireCommand{
		PlayerID: miningPlayer, ShipID: miningShipID, Reply: reply,
	}))
	w.Tick(context.Background())
	require.NoError(t, (<-reply).Err)

	ship, ok = miningShip(w)
	require.True(t, ok)
	assert.Nil(t, ship.MiningTarget, "cease-fire clears the mining mode")
}
