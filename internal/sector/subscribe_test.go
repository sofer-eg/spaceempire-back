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

func TestUnit_Worker_Subscribe_DeliversInitialAdded(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clk := clock.NewRealClock()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64},
		clk,
		nil,
		[]domain.Ship{
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 5, Y: 5}, MaxSpeed: 1},
		},
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub()

	select {
	case patch := <-sub.Patch:
		require.Len(t, patch.Added, 2, "first patch must contain the full ship list")
		assert.Empty(t, patch.Removed)
	case <-time.After(time.Second):
		t.Fatal("no initial patch delivered within 1s")
	}
}

func TestUnit_Worker_Subscribe_DeliversUpdatesOnTick(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 100}},
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub()

	// Drain the initial Added patch.
	select {
	case <-sub.Patch:
	case <-time.After(time.Second):
		t.Fatal("no initial patch")
	}

	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: 7, ShipID: 1, Target: domain.Vec2{X: 100, Y: 0},
	}))

	deadline := time.After(time.Second)
	for {
		select {
		case patch := <-sub.Patch:
			if len(patch.Updated) > 0 {
				return // success
			}
		case <-deadline:
			t.Fatal("no Updated patch within 1s")
		}
	}
}

func TestUnit_Worker_Unsubscribe_ClosesChannel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1}},
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)

	unsub()

	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-sub.Patch:
			if !ok {
				return // success
			}
		case <-deadline:
			t.Fatal("patch channel never closed after Unsubscribe")
		}
	}
}

func TestUnit_Worker_MoveCommand_OwnershipForbiddenForOtherPlayer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, PlayerID: 7, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: 8, ShipID: 1, Target: domain.Vec2{X: 10, Y: 0}, Reply: reply,
	}))
	w.Tick(ctx)

	select {
	case res := <-reply:
		assert.ErrorIs(t, res.Err, sector.ErrForbidden)
	case <-time.After(time.Second):
		t.Fatal("no reply within 1s")
	}
}

func TestUnit_Worker_AOI_PlayersInDifferentSectorRegionsSeeDifferentShips(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Two players on opposite sides of the sector, well outside each other's
	// AOI radius. A neutral ship sits next to player 7 and outside player 8.
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 100},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 10000, Y: 0}, MaxSpeed: 1},
			{ID: 3, PlayerID: 99, Pos: domain.Vec2{X: 50, Y: 0}, MaxSpeed: 1},
		},
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()
	sub8, unsub8, err := w.Subscribe(ctx, testSector, 8)
	require.NoError(t, err)
	defer unsub8()

	p7 := readInitialAddedIDs(t, sub7.Patch)
	p8 := readInitialAddedIDs(t, sub8.Patch)

	assert.ElementsMatch(t, []domain.ShipID{1, 3}, p7,
		"player 7 should see own ship and the neutral within 100 units")
	assert.ElementsMatch(t, []domain.ShipID{2}, p8,
		"player 8 should only see own ship — neutral is 10000 units away")
}

// Phase 10.20 L1: the subscription uses the player ship's personal RadarRange,
// not the flat cfg.AOIRadius. A contact beyond the radar but inside the old
// flat AOI must not be visible.
func TestUnit_Worker_AOI_PersonalRadarLimitsVisibility(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1, RadarRange: 100},
			{ID: 2, PlayerID: 99, Pos: domain.Vec2{X: 50, Y: 0}, MaxSpeed: 1},
			{ID: 3, PlayerID: 99, Pos: domain.Vec2{X: 200, Y: 0}, MaxSpeed: 1},
		},
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()

	ids := readInitialAddedIDs(t, sub7.Patch)
	assert.ElementsMatch(t, []domain.ShipID{1, 2}, ids,
		"radar 100 sees own ship + the contact at 50; the one at 200 (visible under the old flat AOI=5000) is hidden")
}

// Phase 10.20 L1: a ship without a class radar (RadarRange 0, e.g. a
// spacesuit/legacy ship) falls back to cfg.AOIRadius — preserving the pre-10.20
// flat behaviour.
func TestUnit_Worker_AOI_NoClassRadarFallsBackToAOI(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1}, // RadarRange 0
			{ID: 2, PlayerID: 99, Pos: domain.Vec2{X: 200, Y: 0}, MaxSpeed: 1},
		},
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()

	ids := readInitialAddedIDs(t, sub7.Patch)
	assert.ElementsMatch(t, []domain.ShipID{1, 2}, ids,
		"RadarRange 0 → fallback to AOIRadius=5000, so the contact at 200 is visible")
}

// Phase 10.20 L4: a cloaked ship (up_hide → IsHidden via cold-start) is hidden
// from a hostile subscriber beyond the detection range, but stays visible to
// the owner, allies, close hostiles, and while it is firing.
func TestUnit_Worker_Stealth_HidesCloakedFromHostile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rel := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{{7, 9}: domain.RelationFriend}}
	cloak := []domain.InstalledEquipment{{Type: "up_hide", Level: 1}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000, StealthDetectRange: 400},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1, RadarRange: 5000},
			// Cloaked ships carry energy: an "always" cloak surfaces at Energy<=0 (10.3.1).
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 1000, Y: 0}, MaxSpeed: 1, Equipment: cloak, Energy: 100, MaxEnergy: 100}, // cloaked enemy, far → hidden
			{ID: 3, PlayerID: 8, Pos: domain.Vec2{X: 200, Y: 0}, MaxSpeed: 1, Equipment: cloak, Energy: 100, MaxEnergy: 100},  // cloaked enemy, within detect 400 → visible
			{ID: 4, PlayerID: 9, Pos: domain.Vec2{X: 1500, Y: 0}, MaxSpeed: 1, Equipment: cloak, Energy: 100, MaxEnergy: 100}, // cloaked ally, far → visible
			{ID: 5, PlayerID: 8, Pos: domain.Vec2{X: 1200, Y: 0}, MaxSpeed: 1},                                                // normal enemy → visible
		}},
		sector.WithRelations(rel),
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()

	ids := readInitialAddedIDs(t, sub7.Patch)
	assert.ElementsMatch(t, []domain.ShipID{1, 3, 4, 5}, ids,
		"cloaked far enemy (2) hidden; own (1), close cloaked enemy (3), cloaked ally (4), normal enemy (5) visible")
}

// Phase 10.3.1: a cloaked ship whose energy ran dry (Energy<=0) surfaces — the
// up_hide module is an "always" energy consumer and an unpowered cloak fails.
func TestUnit_Worker_Stealth_SurfacesWhenEnergyZero(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cloak := []domain.InstalledEquipment{{Type: "up_hide", Level: 1}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000, StealthDetectRange: 400},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1, RadarRange: 5000},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 1000, Y: 0}, MaxSpeed: 1, Equipment: cloak, Energy: 100, MaxEnergy: 100}, // powered cloak, far → hidden
			{ID: 3, PlayerID: 8, Pos: domain.Vec2{X: 1100, Y: 0}, MaxSpeed: 1, Equipment: cloak, Energy: 0, MaxEnergy: 100},   // dry cloak, far → surfaced
		}},
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()

	ids := readInitialAddedIDs(t, sub7.Patch)
	assert.ElementsMatch(t, []domain.ShipID{1, 3}, ids,
		"powered cloak (2) hidden; dry-energy cloak (3) surfaces; own ship (1) visible")
}

// Phase 10.20a: a cloaked ship that fires a missile is revealed for exactly
// one tick (MissileJustFired), then re-hides on the following tick.
func TestUnit_Stealth_RevealOnMissileLaunch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000, StealthDetectRange: 400},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1, RadarRange: 5000, HP: 200, MaxHP: 200, Shield: 50, MaxShield: 50},
			// ship 2 cloaked, at 1000 — beyond stealthDetect 400 → hidden from player 7.
			// up_launcher so it can fire the reveal missile (phase 10.14b gate).
			// Energy>0 keeps the "always" cloak powered so it stays hidden (10.3.1).
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 1000, Y: 0}, MaxSpeed: 1, Equipment: []domain.InstalledEquipment{{Type: "up_hide", Level: 1}, {Type: "up_launcher", Level: 1}}, HP: 200, MaxHP: 200, Shield: 50, MaxShield: 50, Energy: 100, MaxEnergy: 100},
		}},
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()

	initial := readInitialAddedIDs(t, sub7.Patch)
	assert.ElementsMatch(t, []domain.ShipID{1}, initial, "cloaked ship 2 hidden on subscribe")

	// fire missile from cloaked ship 2 at ship 1
	reply := make(chan sector.LaunchMissileResult, 1)
	require.NoError(t, w.Send(testSector, sector.LaunchMissileCommand{
		PlayerID: 8,
		ShipID:   2,
		Target:   domain.EntityRef{Kind: domain.EntityKindShip, ID: 1},
		Reply:    reply,
	}))
	select {
	case res := <-reply:
		require.NoError(t, res.Err, "missile launch must succeed")
	case <-time.After(time.Second):
		t.Fatal("LaunchMissile not acked within 1s")
	}

	// next patch: ship 2 must appear (MissileJustFired reveal)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case patch := <-sub7.Patch:
			for _, s := range patch.Added {
				if s.ID == 2 {
					goto revealed
				}
			}
		case <-deadline:
			t.Fatal("cloaked ship 2 never revealed after missile launch")
		}
	}
revealed:

	// after reveal tick, ship 2 must disappear again (flag cleared)
	deadline2 := time.After(2 * time.Second)
	for {
		select {
		case patch := <-sub7.Patch:
			for _, id := range patch.Removed {
				if id == 2 {
					return // success: re-hidden on next tick
				}
			}
		case <-deadline2:
			t.Fatal("cloaked ship 2 not re-hidden after missile reveal tick")
		}
	}
}

// Phase 10.20 L2: large statics are visible within RadarRange × bigMult and
// arrive/leave as deltas. A station beyond the big radar is trimmed on the
// first patch; flying next to it brings it back via StaticsAdded.
func TestUnit_Worker_Statics_BigRadarDeltas(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stationA := domain.Station{ID: 1, SectorID: testSector, Pos: domain.Vec2{X: 1000, Y: 0}, HP: 100, Built: true}
	stationB := domain.Station{ID: 2, SectorID: testSector, Pos: domain.Vec2{X: 8000, Y: 0}, HP: 100, Built: true}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000, RadarBigMultiplier: 2.5},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1e6, RadarRange: 1000},
		}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{testSector: {Stations: []domain.Station{stationA, stationB}}}),
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub()

	refB := domain.EntityRef{Kind: domain.EntityKindStation, ID: 2}
	refA := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	containsRef := func(refs []domain.EntityRef, want domain.EntityRef) bool {
		for _, r := range refs {
			if r == want {
				return true
			}
		}
		return false
	}

	// First patch: station B (8000, beyond radar 1000×2.5=2500) is trimmed; A stays.
	select {
	case p := <-sub.Patch:
		assert.True(t, containsRef(p.StaticsRemoved, refB), "far station B trimmed on first patch")
		assert.False(t, containsRef(p.StaticsRemoved, refA), "near station A stays")
		assert.Empty(t, p.StaticsAdded.Stations, "A came via the welcome, not re-added")
	case <-time.After(time.Second):
		t.Fatal("no initial patch within 1s")
	}

	// Fly next to B → it enters the big radar → StaticsAdded.
	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: 7, ShipID: 1, Target: domain.Vec2{X: 7500, Y: 0},
	}))
	deadline := time.After(2 * time.Second)
	for {
		select {
		case p := <-sub.Patch:
			for _, st := range p.StaticsAdded.Stations {
				if st.ID == 2 {
					return // success: B entered the big radar
				}
			}
		case <-deadline:
			t.Fatal("station B never entered the big radar after approach")
		}
	}
}

func TestUnit_Worker_AOI_MovementCausesShipToAppear(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Player 7 at origin; player 8 starts far away and moves into AOI range.
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 100},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 1000, Y: 0}, MaxSpeed: 1e6},
		},
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()

	initial := readInitialAddedIDs(t, sub7.Patch)
	assert.ElementsMatch(t, []domain.ShipID{1}, initial,
		"ship 2 starts at 1000 units — out of 100-unit AOI")

	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: 8, ShipID: 2, Target: domain.Vec2{X: 50, Y: 0},
	}))

	deadline := time.After(2 * time.Second)
	for {
		select {
		case patch := <-sub7.Patch:
			for _, s := range patch.Added {
				if s.ID == 2 {
					return // success: ship 2 now visible
				}
			}
		case <-deadline:
			t.Fatal("ship 2 never appeared in player 7's AOI within 2s")
		}
	}
}

func TestUnit_Worker_AOI_ShipLeavingRadiusBecomesRemoved(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 100},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 1},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 50, Y: 0}, MaxSpeed: 1e6},
		},
	)
	go func() { _ = w.Run(ctx) }()

	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()

	initial := readInitialAddedIDs(t, sub7.Patch)
	assert.ElementsMatch(t, []domain.ShipID{1, 2}, initial)

	require.NoError(t, w.Send(testSector, sector.MoveCommand{
		PlayerID: 8, ShipID: 2, Target: domain.Vec2{X: 10000, Y: 0},
	}))

	deadline := time.After(2 * time.Second)
	for {
		select {
		case patch := <-sub7.Patch:
			for _, id := range patch.Removed {
				if id == 2 {
					return // success
				}
			}
		case <-deadline:
			t.Fatal("ship 2 never disappeared from player 7's AOI within 2s")
		}
	}
}

// readInitialAddedIDs drains the first non-empty patch and returns the IDs
// reported in Added. Fails the test on timeout.
func readInitialAddedIDs(t *testing.T, patches <-chan sector.Patch) []domain.ShipID {
	t.Helper()
	select {
	case patch := <-patches:
		ids := make([]domain.ShipID, 0, len(patch.Added))
		for _, s := range patch.Added {
			ids = append(ids, s.ID)
		}
		return ids
	case <-time.After(time.Second):
		t.Fatal("no initial patch within 1s")
		return nil
	}
}

func TestUnit_Worker_AddShipCommand_AddsToState(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
	)
	go func() { _ = w.Run(ctx) }()

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AddShipCommand{
		Ship:  domain.Ship{ID: 42, PlayerID: 7, Pos: domain.Vec2{X: 1, Y: 2}, HP: 100, Shield: 100},
		Reply: reply,
	}))

	select {
	case res := <-reply:
		require.NoError(t, res.Err)
	case <-time.After(time.Second):
		t.Fatal("AddShip command not acked")
	}

	deadline := time.After(time.Second)
	for {
		snap := w.Snapshot(testSector)
		if len(snap.Ships) == 1 && snap.Ships[0].ID == 42 {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("ship not visible in snapshot; got %+v", snap.Ships)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestUnit_Worker_AppliesCommandBetweenTicks guards the AckTimeout fix: Run
// must apply a queued command as soon as it arrives, not wait for the next
// Tick. With a 10s TickInterval the old (drain-only-on-tick) loop would make
// the spawner's 1s AckTimeout fire before the command was ever processed; the
// inbox-wake case acks well under a tick.
func TestUnit_Worker_AppliesCommandBetweenTicks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 10 * time.Second, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
	)
	go func() { _ = w.Run(ctx) }()

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.AddShipCommand{
		Ship:  domain.Ship{ID: 7, PlayerID: 1, HP: 100, Shield: 100},
		Reply: reply,
	}))

	select {
	case res := <-reply:
		require.NoError(t, res.Err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("add-ship not acked promptly: command waited for the next tick (TickInterval 10s)")
	}
}
