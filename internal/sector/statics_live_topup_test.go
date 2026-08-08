package sector

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
)

// TASK-193: a static that re-enters a subscriber's radar window ships its layout
// in StaticsAdded, but the client dropped its live combat record when it left
// (StaticsRemoved) — and an intact object never fires another dirty event. These
// tests hold the top-up that puts the live hp/shield back into the same patch,
// and the per-subscriber isolation that top-up must not break.

const topUpSector = domain.SectorID(41)

var topUpNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func topUpLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// topUpSub builds a subscriber that has already seen nothing: an empty
// lastSentStatics is the state a subscriber is in right after the object left
// its window, so the next visibility diff reports it as newly-added.
func topUpSub(id uint64, playerID domain.PlayerID) *Subscription {
	out := make(chan Patch, 4)
	return &Subscription{
		SectorID:        topUpSector,
		PlayerID:        playerID,
		Patch:           out,
		id:              id,
		patchOut:        out,
		lastSentStatics: map[domain.EntityRef]struct{}{},
	}
}

// recvPatch drains the one patch broadcastPatches queued for sub.
func recvPatch(t *testing.T, sub *Subscription) Patch {
	t.Helper()
	select {
	case p := <-sub.patchOut:
		return p
	default:
		t.Fatal("no patch delivered to subscriber")
		return Patch{}
	}
}

// liveRecord returns the DestructibleStatic for ref in the patch's combat delta.
func liveRecord(p Patch, ref domain.EntityRef) (domain.DestructibleStatic, int) {
	var found domain.DestructibleStatic
	count := 0
	for _, d := range p.StaticsUpdated {
		if d.Ref == ref {
			found = d
			count++
		}
	}
	return found, count
}

// TestUnit_BroadcastPatches_ReenteredStaticCarriesLiveCombatState is the AC-1
// case: a damaged laser tower re-enters the radar window in a tick where nothing
// damaged it, so the sector-global dirty delta is empty. The patch must still
// carry its ACTUAL hp/shield next to the layout — otherwise the client renders
// the object with no combat state at all («Состояние цели недоступно.»).
func TestUnit_BroadcastPatches_ReenteredStaticCarriesLiveCombatState(t *testing.T) {
	t.Parallel()

	statics := domain.SectorStatics{LaserTowers: []domain.LaserTower{
		{ID: 2, SectorID: topUpSector, Pos: domain.Vec2{X: 300, Y: 0}, HP: 1000, Shield: 500, MaxShield: 500, Built: true},
	}}
	ship := domain.Ship{ID: 1, PlayerID: 7, SectorID: topUpSector, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 1000}
	s := newSectorState(topUpSector, []domain.Ship{ship}, nil, nil, nil, nil, statics, topUpNow)

	// The tower took a beating in some earlier tick and stopped being dirty.
	ref := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 2}
	s.destructibles[ref].HP = 137
	s.destructibles[ref].Shield = 42

	sub := topUpSub(1, 7)
	s.subs[sub.id] = sub

	broadcastPatches(topUpLogger(), s, 1000, aoiParams{fallbackRadius: 1000})

	p := recvPatch(t, sub)
	require.Len(t, p.StaticsAdded.LaserTowers, 1, "the tower's layout re-enters the window")
	rec, n := liveRecord(p, ref)
	require.Equal(t, 1, n, "the same patch carries the tower's live combat record")
	require.Equal(t, 137, rec.HP, "hp is the ACTUAL value, not the spawn number from the layout")
	require.Equal(t, 42, rec.Shield)
}

// TestUnit_BroadcastPatches_ReenteredGateCarriesLiveCombatState is AC-4. Gates
// are radar-gated like towers, but they are world topology, not sector layout:
// SectorStatics has no gate slice, so StaticsAdded is empty for them and the
// combat top-up is the ONLY thing that restores a gate endpoint's hp/shield on
// the client.
func TestUnit_BroadcastPatches_ReenteredGateCarriesLiveCombatState(t *testing.T) {
	t.Parallel()

	ship := domain.Ship{ID: 1, PlayerID: 7, SectorID: topUpSector, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 1000}
	s := newSectorState(topUpSector, []domain.Ship{ship}, nil, nil, nil, nil, domain.SectorStatics{}, topUpNow)

	// Same shape seedGateEndpoints registers, already chipped by an earlier fight.
	ref := domain.EntityRef{Kind: domain.EntityKindGate, ID: 16}
	s.destructibles[ref] = &domain.DestructibleStatic{
		Ref: ref, Pos: domain.Vec2{X: 200, Y: 0}, HP: 4400, Shield: 900, MaxShield: 2000,
	}

	sub := topUpSub(1, 7)
	s.subs[sub.id] = sub

	broadcastPatches(topUpLogger(), s, 1000, aoiParams{fallbackRadius: 1000})

	p := recvPatch(t, sub)
	require.True(t, p.StaticsAdded.IsEmpty(), "a gate has no layout to add — SectorStatics carries no gates")
	rec, n := liveRecord(p, ref)
	require.Equal(t, 1, n, "the gate endpoint's live combat record rides the patch")
	require.Equal(t, 4400, rec.HP)
	require.Equal(t, 900, rec.Shield)
}

// TestUnit_BroadcastPatches_ReenteredStaticNotDuplicatedWhenDirty covers the
// overlap: the object entered the window AND took damage in the same tick, so
// its ref is in both the added set and the sector-global dirty delta. The client
// keys staticCombat by ref, so a duplicate is harmless to render — it is a
// symptom that the top-up is not consulting what the delta already carries, and
// the delta must stay one record per object.
func TestUnit_BroadcastPatches_ReenteredStaticNotDuplicatedWhenDirty(t *testing.T) {
	t.Parallel()

	statics := domain.SectorStatics{LaserTowers: []domain.LaserTower{
		{ID: 2, SectorID: topUpSector, Pos: domain.Vec2{X: 300, Y: 0}, HP: 1000, Built: true},
	}}
	ship := domain.Ship{ID: 1, PlayerID: 7, SectorID: topUpSector, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 1000}
	s := newSectorState(topUpSector, []domain.Ship{ship}, nil, nil, nil, nil, statics, topUpNow)

	ref := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 2}
	s.destructibles[ref].HP = 800
	s.markDestructibleDirty(ref)

	sub := topUpSub(1, 7)
	s.subs[sub.id] = sub

	broadcastPatches(topUpLogger(), s, 1000, aoiParams{fallbackRadius: 1000})

	p := recvPatch(t, sub)
	rec, n := liveRecord(p, ref)
	require.Equal(t, 1, n, "exactly one combat record for the object that both entered and took damage")
	require.Equal(t, 800, rec.HP)
}

// TestUnit_BroadcastPatches_TopUpIsPerSubscriber is AC-2. staticUpdates is
// computed once per tick and handed to every subscriber, so the top-up must
// build a fresh slice per subscriber; appending into the shared one leaks one
// player's newly-visible object into another player's patch.
//
// The two subscribers sit at opposite ends of the sector with a tower each, so
// each one's top-up is a different record. The dirty set is seeded with a ref
// that has no live record on purpose: collectDirtyDestructibles sizes its slice
// by the dirty count and skips such refs, which leaves the shared slice with
// spare capacity — the exact condition under which `append(shared, …)` writes
// into the shared backing array instead of allocating. Without it the test would
// pass or fail on allocator luck.
func TestUnit_BroadcastPatches_TopUpIsPerSubscriber(t *testing.T) {
	t.Parallel()

	statics := domain.SectorStatics{LaserTowers: []domain.LaserTower{
		{ID: 2, SectorID: topUpSector, Pos: domain.Vec2{X: 300, Y: 0}, HP: 1000, Built: true},
		{ID: 3, SectorID: topUpSector, Pos: domain.Vec2{X: 9000, Y: 0}, HP: 2000, Built: true},
	}}
	ships := []domain.Ship{
		{ID: 1, PlayerID: 7, SectorID: topUpSector, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 1000},
		{ID: 2, PlayerID: 8, SectorID: topUpSector, Pos: domain.Vec2{X: 9300, Y: 0}, RadarRange: 1000},
	}
	s := newSectorState(topUpSector, ships, nil, nil, nil, nil, statics, topUpNow)
	s.destructiblesDirty[domain.EntityRef{Kind: domain.EntityKindSatellite, ID: 99}] = true

	refNear := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 2}
	refFar := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 3}

	subA := topUpSub(1, 7)
	subB := topUpSub(2, 8)
	s.subs[subA.id] = subA
	s.subs[subB.id] = subB

	broadcastPatches(topUpLogger(), s, 1000, aoiParams{fallbackRadius: 1000})

	pA := recvPatch(t, subA)
	pB := recvPatch(t, subB)

	_, nearInA := liveRecord(pA, refNear)
	_, farInA := liveRecord(pA, refFar)
	require.Equal(t, 1, nearInA, "player 7 gets the tower that entered ITS window")
	require.Zero(t, farInA, "player 7 must not receive player 8's newly-visible tower")

	_, farInB := liveRecord(pB, refFar)
	_, nearInB := liveRecord(pB, refNear)
	require.Equal(t, 1, farInB, "player 8 gets the tower that entered ITS window")
	require.Zero(t, nearInB, "player 8 must not receive player 7's newly-visible tower")
}

// TestUnit_Subscribe_StaticInstalledSinceLastPublishReachesNewSubscriber is
// AC-5. The welcome statics frame is built from the LAST PUBLISHED snapshot,
// while an install command puts the object into the live state the moment it
// applies — so between a publish and the next one there is a window where the
// two disagree. Seeding the subscriber's seen-set from the live state marks an
// object as already-delivered that the welcome frame never carried, and the
// big-radar diff then has nothing to add: the client stays blind to it for the
// whole session. Seeding from the same snapshot the frame is built from makes
// the worst case a harmless duplicate instead.
func TestUnit_Subscribe_StaticInstalledSinceLastPublishReachesNewSubscriber(t *testing.T) {
	t.Parallel()

	ship := domain.Ship{ID: 1, PlayerID: 7, SectorID: topUpSector, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 1000}
	s := newSectorState(topUpSector, []domain.Ship{ship}, nil, nil, nil, nil, domain.SectorStatics{}, topUpNow)
	w := NewWorker(0, Config{}, clock.NewRealClock(), nil, nil, nil)

	// Installed after the snapshot the welcome frame will be built from.
	owner := domain.PlayerID(7)
	s.addSatellite(domain.Satellite{
		ID: 5, OwnerID: &owner, SectorID: topUpSector, Pos: domain.Vec2{X: 200, Y: 0},
		Built: true, HP: 5000, Shield: 2000, MaxShield: 2000,
	})

	subscribeCommand{sectorID: topUpSector, playerID: 7}.apply(w, s)
	require.Len(t, s.subs, 1)
	var sub *Subscription
	for _, v := range s.subs {
		sub = v
	}

	broadcastPatches(topUpLogger(), s, 1000, aoiParams{fallbackRadius: 1000})

	p := recvPatch(t, sub)
	require.Len(t, p.StaticsAdded.Satellites, 1, "the satellite the welcome frame missed arrives in the first patch")
	rec, n := liveRecord(p, domain.EntityRef{Kind: domain.EntityKindSatellite, ID: 5})
	require.Equal(t, 1, n, "with its live combat record alongside")
	require.Equal(t, 5000, rec.HP)
}

// TestUnit_WithLiveStatics_SkipsRefsWithoutLiveRecord holds the tolerance the
// helper needs: a ref with no entry in the destructible map contributes nothing
// rather than panicking, so the caller never has to pre-filter what it passes.
func TestUnit_WithLiveStatics_SkipsRefsWithoutLiveRecord(t *testing.T) {
	t.Parallel()

	s := newSectorState(topUpSector, nil, nil, nil, nil, nil, domain.SectorStatics{}, topUpNow)
	known := domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 2}
	s.destructibles[known] = &domain.DestructibleStatic{Ref: known, HP: 10}
	unknown := domain.EntityRef{Kind: domain.EntityKindSatellite, ID: 404}

	out := s.withLiveStatics(nil, []domain.EntityRef{unknown, known})

	require.Len(t, out, 1)
	require.Equal(t, known, out[0].Ref)
}
