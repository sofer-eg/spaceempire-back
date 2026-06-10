package sector_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

type scanCall struct {
	race   domain.RaceID
	target domain.ShipID
	player domain.PlayerID
}

type killCall struct {
	killer domain.PlayerID
	race   domain.RaceID
}

// fakePoliceScanner records the worker's scanner calls and returns a
// programmed scan result.
type fakePoliceScanner struct {
	result  sector.PoliceScanResult
	scanned []scanCall
	killed  []killCall
}

func (f *fakePoliceScanner) Scan(_ context.Context, race domain.RaceID, target domain.ShipID, player domain.PlayerID) (sector.PoliceScanResult, error) {
	f.scanned = append(f.scanned, scanCall{race, target, player})
	return f.result, nil
}

func (f *fakePoliceScanner) OnRaceShipKilled(_ context.Context, killer domain.PlayerID, race domain.RaceID) error {
	f.killed = append(f.killed, killCall{killer, race})
	return nil
}

// policeShip is a navy ship of a police race (set Race), stationary.
func policeShip(id int64, race domain.RaceID, pos domain.Vec2) domain.Ship {
	return domain.Ship{ID: domain.ShipID(id), Race: race, Pos: pos, HP: 50, MaxHP: 50}
}

// playerShip is a factionless (race 0) ship owned by a real player.
func playerShip(id, playerID int64, pos domain.Vec2) domain.Ship {
	return domain.Ship{ID: domain.ShipID(id), PlayerID: domain.PlayerID(playerID), Pos: pos, HP: 50, MaxHP: 50}
}

func policeConfig() sector.PoliceConfig {
	return sector.PoliceConfig{Races: []domain.RaceID{1}, ScanRange: 100, CooldownTicks: 10}
}

func TestUnit_Worker_PoliceScan_ConfiscatesContrabandAndPublishes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := bus.NewInMemory(8)
	t.Cleanup(b.Close)
	got := make(chan sector.PoliceScanEvent, 1)
	require.NoError(t, b.Subscribe(ctx, sector.PoliceScanTopic(100), func(payload []byte) {
		var ev sector.PoliceScanEvent
		if err := json.Unmarshal(payload, &ev); err == nil {
			got <- ev
		}
	}))

	scanner := &fakePoliceScanner{result: sector.PoliceScanResult{
		Confiscated: true, GoodsType: 323, Quantity: 5, Wanted: true,
	}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			policeShip(1, 1, domain.Vec2{X: 0, Y: 0}),
			playerShip(2, 100, domain.Vec2{X: 10, Y: 0}),
		}},
		sector.WithHandoff(nil, b),
		sector.WithPolice(scanner, policeConfig()),
	)

	w.Tick(ctx)

	require.Equal(t, []scanCall{{race: 1, target: 2, player: 100}}, scanner.scanned)
	select {
	case ev := <-got:
		assert.Equal(t, domain.PlayerID(100), ev.PlayerID)
		assert.Equal(t, domain.RaceID(1), ev.Race)
		assert.Equal(t, domain.GoodsTypeID(323), ev.GoodsType)
		assert.EqualValues(t, 5, ev.Quantity)
		assert.True(t, ev.Wanted)
	case <-time.After(time.Second):
		t.Fatal("expected a police scan event")
	}
}

func TestUnit_Worker_PoliceScan_CleanHoldNoEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b := bus.NewInMemory(8)
	t.Cleanup(b.Close)
	events := 0
	require.NoError(t, b.Subscribe(ctx, sector.PoliceScanTopic(100), func([]byte) { events++ }))

	scanner := &fakePoliceScanner{result: sector.PoliceScanResult{}} // clean
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			policeShip(1, 1, domain.Vec2{X: 0, Y: 0}),
			playerShip(2, 100, domain.Vec2{X: 10, Y: 0}),
		}},
		sector.WithHandoff(nil, b),
		sector.WithPolice(scanner, policeConfig()),
	)

	w.Tick(ctx)

	require.Len(t, scanner.scanned, 1, "the player is still scanned")
	assert.Zero(t, events, "a clean hold publishes no event")
}

func TestUnit_Worker_PoliceScan_OutOfRangeNotScanned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	scanner := &fakePoliceScanner{}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 5000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			policeShip(1, 1, domain.Vec2{X: 0, Y: 0}),
			playerShip(2, 100, domain.Vec2{X: 500, Y: 0}), // beyond ScanRange 100
		}},
		sector.WithPolice(scanner, policeConfig()),
	)

	w.Tick(ctx)
	assert.Empty(t, scanner.scanned)
}

func TestUnit_Worker_PoliceScan_SkipsNPCsAndRespectsCooldown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	registry := ai.NewRegistry()
	registry.Register("circle", newCircleController)

	scanner := &fakePoliceScanner{result: sector.PoliceScanResult{}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			policeShip(1, 1, domain.Vec2{X: 0, Y: 0}),
			playerShip(2, 100, domain.Vec2{X: 10, Y: 0}), // real player — scanned
			// id 3: NPC (race 0, owned by __npc__) with an AI controller — skipped.
			{ID: 3, PlayerID: 50, Pos: domain.Vec2{X: 10, Y: 0}, HP: 50, MaxHP: 50},
		}},
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: {
			{ShipID: 3, SectorID: testSector, ControllerKind: "circle"},
		}}),
		sector.WithPolice(scanner, policeConfig()),
	)

	// First tick: only the controller-less player (id 2) is scanned.
	w.Tick(ctx)
	require.Equal(t, []scanCall{{race: 1, target: 2, player: 100}}, scanner.scanned)

	// Second tick: still within the 10-tick cooldown — no re-scan.
	w.Tick(ctx)
	require.Len(t, scanner.scanned, 1, "cooldown suppresses re-scan")
}

func TestUnit_Worker_PoliceScan_StandingDropsOnRaceShipKill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	scanner := &fakePoliceScanner{}
	killer := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 1000
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	victim := laserShip(2, 0, domain.Vec2{X: 30, Y: 0})
	victim.Race = 1 // a main-race navy ship

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {killer, victim}},
		sector.WithPolice(scanner, policeConfig()),
	)

	w.Tick(ctx) // killer one-shots the navy ship; kill sweep drops standing

	require.Equal(t, []killCall{{killer: 100, race: 1}}, scanner.killed)
}
