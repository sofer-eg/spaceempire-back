package app

import (
	"context"
	"log/slog"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// --- fakes -----------------------------------------------------------------

type fakeCreator struct {
	created []domain.Ship
	next    int64
}

func (f *fakeCreator) Create(_ context.Context, s domain.Ship) (domain.ShipID, error) {
	f.next++
	s.ID = domain.ShipID(f.next)
	f.created = append(f.created, s)
	return s.ID, nil
}

type fakeCounter struct{ counts map[domain.RaceID]int }

func (f fakeCounter) CountByRace(_ context.Context) (map[domain.RaceID]int, error) {
	return f.counts, nil
}

type fakeAIState struct{ states []domain.AIState }

func (f *fakeAIState) BatchUpsert(_ context.Context, st []domain.AIState) error {
	f.states = append(f.states, st...)
	return nil
}

type fakeSender struct{ sent []sector.AddShipCommand }

func (f *fakeSender) Send(_ domain.SectorID, cmd sector.Command) error {
	if c, ok := cmd.(sector.AddShipCommand); ok {
		f.sent = append(f.sent, c)
		if c.Reply != nil {
			c.Reply <- sector.CmdResult{} // ack synchronously (buffered cap 1)
		}
	}
	return nil
}

// gateExit is the populated-sector side of the test gate.
var gateExit = domain.Vec2{X: 800, Y: 0}

func testShipClasses(t *testing.T) *balance.ShipClasses {
	t.Helper()
	sc, err := balance.NewShipClasses([]balance.ShipClass{
		{ID: 71, Race: 7, Class: 3, Name: "Xenon M3", Hull: 100, Shield: 50, Speed: 30, Acceleration: 10, ShieldCharge: 1, Laser: 100},
		{ID: 72, Race: 7, Class: 4, Name: "Xenon M4", Hull: 80, Shield: 40, Speed: 40, Acceleration: 12, ShieldCharge: 1, Laser: 80},
		{ID: 73, Race: 7, Class: 5, Name: "Xenon M5", Hull: 60, Shield: 20, Speed: 50, Acceleration: 15, ShieldCharge: 1, Laser: 60},
		{ID: 81, Race: 8, Class: 2, Name: "Khaak M2", Hull: 300, Shield: 150, Speed: 20, Acceleration: 5, ShieldCharge: 2, Laser: 300},
		{ID: 82, Race: 8, Class: 3, Name: "Khaak M3", Hull: 100, Shield: 50, Speed: 30, Acceleration: 10, ShieldCharge: 1, Laser: 100},
		{ID: 11, Race: 1, Class: 3, Name: "Argon M3", Hull: 100, Shield: 50, Speed: 30, Acceleration: 10, ShieldCharge: 1, Laser: 100},
	})
	require.NoError(t, err)
	return sc
}

// newTestSpawner wires a spawner over one populated sector (1) reachable by a
// gate from an empty sector (2). Xenon spawns at the gate exit in sector 1;
// Kha'ak jumps into sector 1's centre.
func newTestSpawner(creator shipCreator, counter raceCounter, aiState aiStateWriter, sender commandSender, classes *balance.ShipClasses) *invasionSpawner {
	topo := world.New(
		[]domain.Sector{{ID: 1}, {ID: 2}},
		[]domain.Gate{{ID: 1, SectorA: 1, PosA: gateExit, SectorB: 2, PosB: domain.Vec2{X: -800}}},
	)
	statics := map[domain.SectorID]domain.SectorStatics{
		1: {Stations: []domain.Station{{}}}, // populated
		2: {},                               // empty — never invaded
	}
	return newInvasionSpawner(
		creator, counter, aiState, sender,
		topo, statics, classes, domain.PlayerID(50),
		ShipSpawnerConfig{}, InvasionConfig{},
		clock.NewRealClock(), slog.New(slog.DiscardHandler),
	)
}

// --- tests -----------------------------------------------------------------

func TestUnit_Invasion_SpawnsXenonWaveAtGate(t *testing.T) {
	t.Parallel()
	creator := &fakeCreator{}
	aiState := &fakeAIState{}
	sender := &fakeSender{}
	// Suppress Kha'ak so only the Xenon wave fires.
	counter := fakeCounter{counts: map[domain.RaceID]int{7: 0, 8: 999}}
	s := newTestSpawner(creator, counter, aiState, sender, testShipClasses(t))

	s.Tick(context.Background())

	require.Len(t, creator.created, 3, "default Xenon group size")
	for _, sh := range creator.created {
		assert.Equal(t, xenonRace, sh.Race)
		assert.Equal(t, domain.SectorID(1), sh.SectorID)
		assert.LessOrEqual(t, math.Hypot(sh.Pos.X-gateExit.X, sh.Pos.Y-gateExit.Y), invasionSpawnRing+1e-6,
			"Xenon spawns on the ring around the gate exit, not the sector centre")
	}
	require.Len(t, sender.sent, 3)
	for _, c := range sender.sent {
		assert.Equal(t, "race", c.ControllerKind, "injected with a race controller")
	}
	require.Len(t, aiState.states, 3)
	for _, st := range aiState.states {
		assert.Equal(t, "race", st.ControllerKind)
	}
}

func TestUnit_Invasion_KhaakJumpsIntoSectorCentre(t *testing.T) {
	t.Parallel()
	creator := &fakeCreator{}
	sender := &fakeSender{}
	// Suppress Xenon so only the Kha'ak cluster fires.
	counter := fakeCounter{counts: map[domain.RaceID]int{7: 999, 8: 0}}
	s := newTestSpawner(creator, counter, &fakeAIState{}, sender, testShipClasses(t))

	s.Tick(context.Background())

	require.Len(t, creator.created, 4, "default Kha'ak group size")
	for _, sh := range creator.created {
		assert.Equal(t, khaakRace, sh.Race)
		assert.Equal(t, domain.SectorID(1), sh.SectorID)
		assert.LessOrEqual(t, math.Hypot(sh.Pos.X, sh.Pos.Y), invasionSpawnRing+1e-6,
			"Kha'ak jumps into the sector centre, not the gate")
	}
}

func TestUnit_Invasion_RespectsMaxActive(t *testing.T) {
	t.Parallel()
	creator := &fakeCreator{}
	counter := fakeCounter{counts: map[domain.RaceID]int{7: 12, 8: 8}} // both at the default cap
	s := newTestSpawner(creator, counter, &fakeAIState{}, &fakeSender{}, testShipClasses(t))

	s.Tick(context.Background())
	assert.Empty(t, creator.created, "no spawn when both factions are at the cap")
}

func TestUnit_Invasion_ClampsGroupToRemainingRoom(t *testing.T) {
	t.Parallel()
	creator := &fakeCreator{}
	// Xenon one below the cap (room 1) → a 3-ship group is clamped to 1.
	counter := fakeCounter{counts: map[domain.RaceID]int{7: 11, 8: 999}}
	s := newTestSpawner(creator, counter, &fakeAIState{}, &fakeSender{}, testShipClasses(t))

	s.Tick(context.Background())
	require.Len(t, creator.created, 1, "wave clamped to the one free slot")
	assert.Equal(t, xenonRace, creator.created[0].Race)
}

func TestUnit_Invasion_WipedWaveFreesLimit(t *testing.T) {
	t.Parallel()
	sender := &fakeSender{}
	classes := testShipClasses(t)

	// At the cap: nothing spawns.
	full := &fakeCreator{}
	atCap := newTestSpawner(full, fakeCounter{counts: map[domain.RaceID]int{7: 12, 8: 999}}, &fakeAIState{}, sender, classes)
	atCap.Tick(context.Background())
	require.Empty(t, full.created)

	// After the wave is wiped (count back to 0) a fresh wave spawns.
	freed := &fakeCreator{}
	emptied := newTestSpawner(freed, fakeCounter{counts: map[domain.RaceID]int{7: 0, 8: 999}}, &fakeAIState{}, sender, classes)
	emptied.Tick(context.Background())
	require.Len(t, freed.created, 3, "freed cap lets the next wave spawn")
}

func TestUnit_CombatClassesForRace(t *testing.T) {
	t.Parallel()
	all := testShipClasses(t).AllShipClasses()

	xenon := combatClassesForRace(all, 7, 3, 4, 5)
	require.Len(t, xenon, 3)
	for _, c := range xenon {
		assert.Equal(t, 7, c.Race)
	}
	assert.Equal(t, 3, xenon[0].Class, "sorted by class ascending")

	// No class matches the filter → fall back to every class of that race.
	fallback := combatClassesForRace(all, 7, 99)
	assert.Len(t, fallback, 3)

	// A race with no classes at all → empty.
	assert.Empty(t, combatClassesForRace(all, 2, 3))
}
