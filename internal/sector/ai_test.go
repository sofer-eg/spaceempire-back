package sector_test

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/ai/race"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// alwaysHostile marks every other ship as a target — used to drive the race
// controller into engagement in the end-to-end worker test.
type alwaysHostile struct{}

func (alwaysHostile) IsHostile(_, _ domain.Ship) bool { return true }

func snapshotShipByID(snap sector.Snapshot, id domain.ShipID) (domain.Ship, bool) {
	for _, s := range snap.Ships {
		if s.ID == id {
			return s, true
		}
	}
	return domain.Ship{}, false
}

// circleController is the "move in circle" test AI from the 5.1 acceptance
// criteria: each tick it advances a phase angle and steers the ship to the
// matching point on a fixed circle. Its full state (center/radius/step/
// phase) is JSON-serialized, so a restart rebuilds it mid-orbit — exercising
// the ai_state persistence path.
type circleController struct {
	Center domain.Vec2 `json:"center"`
	Radius float64     `json:"radius"`
	Step   float64     `json:"step"` // radians per tick
	Phase  float64     `json:"phase"`
}

func (c *circleController) Kind() string { return "circle" }

func (c *circleController) Tick(_ context.Context, _ ai.WorldView) ai.Action {
	c.Phase += c.Step
	return ai.MoveTo{Target: domain.Vec2{
		X: c.Center.X + c.Radius*math.Cos(c.Phase),
		Y: c.Center.Y + c.Radius*math.Sin(c.Phase),
	}}
}

func (c *circleController) MarshalState() ([]byte, error) { return json.Marshal(c) }

// newCircleController is the registry Factory: it rebuilds the controller
// from persisted state. Empty state would yield a zero (radius 0) orbit, so
// production seeds the params into state_json at spawn — the tests do the
// same.
func newCircleController(state []byte) (ai.Controller, error) {
	c := &circleController{}
	if len(state) > 0 {
		if err := json.Unmarshal(state, c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func circleShip(id, playerID int64, pos domain.Vec2) domain.Ship {
	return domain.Ship{
		ID:           domain.ShipID(id),
		PlayerID:     domain.PlayerID(playerID),
		SectorID:     testSector,
		Pos:          pos,
		Direction:    domain.Vec2{X: 1, Y: 0},
		MaxSpeed:     50,
		Acceleration: 50,
		TurnRate:     math.Pi,
		HP:           100,
		MaxHP:        100,
	}
}

func mustMarshalCircle(t *testing.T, c *circleController) []byte {
	t.Helper()
	b, err := json.Marshal(c)
	require.NoError(t, err)
	return b
}

// fakeAIStateRepo records the most recent BatchUpsert per ship so tests can
// assert what the periodic snapshot persisted and feed it into a "restarted"
// worker. Locked because the race test ticks concurrently with readers.
type fakeAIStateRepo struct {
	mu    sync.Mutex
	last  map[domain.ShipID]domain.AIState
	calls int
}

func (r *fakeAIStateRepo) BatchUpsert(_ context.Context, states []domain.AIState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.last == nil {
		r.last = make(map[domain.ShipID]domain.AIState, len(states))
	}
	for _, st := range states {
		r.last[st.ShipID] = st
	}
	return nil
}

func (r *fakeAIStateRepo) snapshot(id domain.ShipID) (domain.AIState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.last[id]
	return st, ok
}

func newAIWorker(
	t *testing.T,
	cfg sector.Config,
	clk clock.Clock,
	repo sector.AIStateRepo,
	ships []domain.Ship,
	states []domain.AIState,
) *sector.Worker {
	t.Helper()
	registry := ai.NewRegistry()
	registry.Register("circle", newCircleController)
	return sector.NewWorker(0, cfg, clk, nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: ships},
		sector.WithAI(registry, repo, map[domain.SectorID][]domain.AIState{testSector: states}),
	)
}

// TestUnit_Worker_AICircle_ShipOrbits checks the acceptance criterion: a
// ship driven by the circle AI actually moves around the circle, visiting
// every quadrant rather than drifting in a line or standing still.
func TestUnit_Worker_AICircle_ShipOrbits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const radius = 200.0
	ctrl := &circleController{Radius: radius, Step: 0.1}
	w := newAIWorker(t,
		sector.Config{TickInterval: time.Second, AOIRadius: 5000},
		clock.NewRealClock(), nil,
		[]domain.Ship{circleShip(1, 0, domain.Vec2{X: radius, Y: 0})},
		[]domain.AIState{{
			ShipID:         1,
			SectorID:       testSector,
			ControllerKind: "circle",
			StateJSON:      mustMarshalCircle(t, ctrl),
		}},
	)

	quadrants := map[int]bool{}
	moved := false
	// 80 ticks * 0.1 rad ≈ 1.3 turns — enough to sweep all four quadrants.
	for i := 0; i < 80; i++ {
		w.Tick(ctx)
		s := w.Snapshot(testSector).Ships[0]
		if s.Pos != (domain.Vec2{X: radius, Y: 0}) {
			moved = true
		}
		quadrants[quadrant(s.Pos)] = true
	}

	require.True(t, moved, "ship never left its start position")
	assert.GreaterOrEqual(t, len(quadrants), 3,
		"ship should sweep the circle (visited quadrants=%v), not drift", quadrants)
}

// quadrant maps a position to 0..3 (or -1 at the exact origin) so the orbit
// test can confirm the ship swept around the centre.
func quadrant(p domain.Vec2) int {
	switch {
	case p.X == 0 && p.Y == 0:
		return -1
	case p.X >= 0 && p.Y >= 0:
		return 0
	case p.X < 0 && p.Y >= 0:
		return 1
	case p.X < 0 && p.Y < 0:
		return 2
	default:
		return 3
	}
}

// TestUnit_Worker_AIState_SurvivesRestart drives a worker until the periodic
// snapshot persists the controller's advanced phase, then builds a second
// worker from that snapshot and confirms it resumes the orbit (phase keeps
// climbing) instead of restarting from zero.
func TestUnit_Worker_AIState_SurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	const step = 0.1
	cfg := sector.Config{TickInterval: time.Second, SnapshotInterval: 5 * time.Second, AOIRadius: 5000}
	initial := mustMarshalCircle(t, &circleController{Radius: 200, Step: step})

	// Worker A: 5 ticks at 1 s reach SnapshotInterval, persisting phase=5*step.
	repoA := &fakeAIStateRepo{}
	clkA := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	wa := newAIWorker(t, cfg, clkA, repoA,
		[]domain.Ship{circleShip(1, 0, domain.Vec2{X: 200, Y: 0})},
		[]domain.AIState{{ShipID: 1, SectorID: testSector, ControllerKind: "circle", StateJSON: initial}},
	)
	for i := 0; i < 5; i++ {
		clkA.Advance(time.Second)
		wa.Tick(ctx)
	}
	require.Equal(t, 1, repoA.calls, "expected exactly one snapshot after 5 ticks")
	savedA, ok := repoA.snapshot(1)
	require.True(t, ok, "phase was not persisted")
	phaseA := unmarshalPhase(t, savedA.StateJSON)
	assert.InDelta(t, 5*step, phaseA, 1e-9)

	// Worker B: cold-start from A's saved state → resumes at phase 5*step and
	// climbs to 10*step after another 5 ticks.
	repoB := &fakeAIStateRepo{}
	clkB := clock.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	wb := newAIWorker(t, cfg, clkB, repoB,
		[]domain.Ship{circleShip(1, 0, domain.Vec2{X: 200, Y: 0})},
		[]domain.AIState{savedA},
	)
	for i := 0; i < 5; i++ {
		clkB.Advance(time.Second)
		wb.Tick(ctx)
	}
	savedB, ok := repoB.snapshot(1)
	require.True(t, ok)
	phaseB := unmarshalPhase(t, savedB.StateJSON)
	assert.InDelta(t, 10*step, phaseB, 1e-9,
		"restart must resume the saved phase, not reset to zero")
}

func unmarshalPhase(t *testing.T, data []byte) float64 {
	t.Helper()
	var c circleController
	require.NoError(t, json.Unmarshal(data, &c))
	return c.Phase
}

// TestUnit_Worker_AIRace_ManyNPCAndPlayers runs 100 AI-driven NPCs alongside
// 10 player ships while players concurrently issue MoveCommands and read
// snapshots. Run under `-race`: the tick goroutine is the only writer, so no
// data race must surface from AI ticking + concurrent commands/reads.
func TestUnit_Worker_AIRace_ManyNPCAndPlayers(t *testing.T) {
	t.Parallel()

	const (
		npcs    = 100
		players = 10
	)
	ships := make([]domain.Ship, 0, npcs+players)
	states := make([]domain.AIState, 0, npcs)
	for i := 1; i <= npcs; i++ {
		ships = append(ships, circleShip(int64(i), 0, domain.Vec2{X: float64(i), Y: 0}))
		st := mustMarshalCircle(t, &circleController{
			Center: domain.Vec2{X: float64(i), Y: 0}, Radius: 50, Step: 0.1,
		})
		states = append(states, domain.AIState{
			ShipID: domain.ShipID(i), SectorID: testSector, ControllerKind: "circle", StateJSON: st,
		})
	}
	for p := 1; p <= players; p++ {
		id := int64(1000 + p)
		ships = append(ships, circleShip(id, id, domain.Vec2{X: 0, Y: float64(p)}))
	}

	w := newAIWorker(t,
		sector.Config{TickInterval: time.Millisecond, AOIRadius: 5000},
		clock.NewRealClock(), &fakeAIStateRepo{}, ships, states)

	ctx, cancel := context.WithCancel(context.Background())

	// One driver goroutine ticks the worker as fast as it can.
	var ticker sync.WaitGroup
	ticker.Add(1)
	go func() {
		defer ticker.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				w.Tick(ctx)
			}
		}
	}()

	// Players concurrently send moves and read snapshots.
	var clients sync.WaitGroup
	for p := 1; p <= players; p++ {
		clients.Add(1)
		go func(pid int64) {
			defer clients.Done()
			for i := 0; i < 100; i++ {
				_ = w.Send(testSector, sector.MoveCommand{
					PlayerID: domain.PlayerID(pid),
					ShipID:   domain.ShipID(pid),
					Target:   domain.Vec2{X: float64(i), Y: float64(i)},
				})
				_ = w.Snapshot(testSector)
			}
		}(int64(1000 + p))
	}
	clients.Wait()
	cancel()
	ticker.Wait()

	require.Len(t, w.Snapshot(testSector).Ships, npcs+players)
}

// TestUnit_Worker_RaceAI_EngagesHostile is the phase 5.2 end-to-end proof:
// a ship driven by the "race" controller (via the registry, hostility from an
// injected targeter) detects a nearby enemy, the worker applies the Attack
// action (sets AttackTarget), and the laser tick (4.2) chips the enemy down.
func TestUnit_Worker_RaceAI_EngagesHostile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Laser-equipped racer at the origin; a sturdy enemy 60 units away
	// (inside both DetectionRange and LaserRange).
	racer := domain.Ship{
		ID: 1, PlayerID: 0, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		MaxSpeed: 10, Acceleration: 10, TurnRate: math.Pi,
		HP: 100, MaxHP: 100,
		Energy: 100, MaxEnergy: 100, EnergyRecharge: 50,
		LaserDamage: 20, LaserRange: 200, LaserEnergyCost: 5,
	}
	enemy := domain.Ship{
		ID: 2, PlayerID: 200, SectorID: testSector,
		Pos: domain.Vec2{X: 60, Y: 0}, Direction: domain.Vec2{X: 1, Y: 0},
		HP: 300, MaxHP: 300, Shield: 100, MaxShield: 100,
	}

	registry := ai.NewRegistry()
	race.Register(registry, alwaysHostile{}, race.Config{DetectionRange: 600, PatrolRadius: 150})

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 5000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {racer, enemy}},
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: {
			{ShipID: 1, SectorID: testSector, ControllerKind: race.Kind, StateJSON: []byte("{}")},
		}}),
	)

	for i := 0; i < 6; i++ {
		w.Tick(ctx)
	}

	snap := w.Snapshot(testSector)
	racerSnap, ok := snapshotShipByID(snap, 1)
	require.True(t, ok)
	require.NotNil(t, racerSnap.AttackTarget, "race controller should have engaged")
	assert.Equal(t, int64(2), racerSnap.AttackTarget.ID)

	enemySnap, ok := snapshotShipByID(snap, 2)
	require.True(t, ok)
	assert.Less(t, enemySnap.Shield+enemySnap.HP, 400, "enemy should have taken laser damage")
}
