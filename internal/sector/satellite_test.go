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

type fakeSatelliteRepo struct {
	nextID  domain.SatelliteID
	created []domain.Satellite
	deleted []domain.SatelliteID
}

func (f *fakeSatelliteRepo) Create(_ context.Context, s domain.Satellite) (domain.SatelliteID, error) {
	f.created = append(f.created, s)
	return f.nextID, nil
}

func (f *fakeSatelliteRepo) Delete(_ context.Context, id domain.SatelliteID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func satRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindSatellite, ID: id}
}

// TestUnit_Satellite_InstallAddsToLayoutAndCombat: the install command persists
// a satellite (Create), drops it into the rendered layout at the ship's
// position, and into the live combat set so lasers can target it.
func TestUnit_Satellite_InstallAddsToLayoutAndCombat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := &fakeSatelliteRepo{nextID: 42}
	ship := domain.Ship{ID: 1, PlayerID: 7, SectorID: testSector, Pos: domain.Vec2{X: 30, Y: -40}, Race: 2}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithSatellites(repo),
	)

	reply := make(chan sector.InstallSatelliteResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{PlayerID: 7, ShipID: 1, Reply: reply}))
	w.Tick(ctx)

	res := <-reply
	require.NoError(t, res.Err)
	require.Equal(t, domain.SatelliteID(42), res.SatelliteID)
	require.Len(t, repo.created, 1, "install persisted via Create")
	require.Equal(t, domain.Vec2{X: 30, Y: -40}, repo.created[0].Pos)
	require.NotNil(t, repo.created[0].OwnerID)
	require.Equal(t, domain.PlayerID(7), *repo.created[0].OwnerID)

	snap := w.Snapshot(testSector)
	require.Len(t, snap.Statics.Satellites, 1, "satellite in rendered layout")
	assert.Equal(t, domain.Vec2{X: 30, Y: -40}, snap.Statics.Satellites[0].Pos)
	_, ok := findDestructible(snap, satRef(42))
	assert.True(t, ok, "satellite in live combat set")
}

// TestUnit_Satellite_InstallForeignShipForbidden: a player cannot deploy a
// satellite from a ship that is not theirs.
func TestUnit_Satellite_InstallForeignShipForbidden(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ship := domain.Ship{ID: 1, PlayerID: 7, SectorID: testSector, Pos: domain.Vec2{}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		sector.WithSatellites(&fakeSatelliteRepo{}),
	)

	reply := make(chan sector.InstallSatelliteResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{PlayerID: 99, ShipID: 1, Reply: reply}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, sector.ErrForbidden)
	assert.Empty(t, w.Snapshot(testSector).Statics.Satellites)
}

// TestUnit_Satellite_DestructionPersisted: a satellite killed in combat is
// removed from the layout and deleted via the repo so a restart will not
// resurrect it (mirrors the laser-tower 8.5 contract).
func TestUnit_Satellite_DestructionPersisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sat := domain.Satellite{ID: 9, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0}, HP: 100, Built: true}
	attacker := staticAttacker(1, 100, domain.Vec2{X: 0, Y: 0}, 1000, satRef(9))

	repo := &fakeSatelliteRepo{}
	statics := map[domain.SectorID]domain.SectorStatics{testSector: {Satellites: []domain.Satellite{sat}}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {attacker}},
		sector.WithStatics(statics),
		sector.WithHostility(ownerBasedHostility),
		sector.WithSatellites(repo),
	)
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.Satellites, "destroyed satellite gone from layout")
	require.Equal(t, []domain.SatelliteID{9}, repo.deleted, "satellite destruction persisted (delete)")
}

// TestUnit_Satellite_RevealsSector: while the player's own live satellite is
// present, their AOI widens to the whole sector — a ship beyond the player's
// own small radar becomes visible only after the satellite is deployed.
func TestUnit_Satellite_RevealsSector(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 100, MaxSpeed: 1},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 500, Y: 0}, RadarRange: 100, MaxSpeed: 1},
		},
	)
	go func() { _ = w.Run(ctx) }()

	sub, unsub, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub()

	// First patch: ship 2 at 500 is outside player 7's 100-unit radar.
	select {
	case patch := <-sub.Patch:
		for _, sh := range patch.Added {
			require.NotEqual(t, domain.ShipID(2), sh.ID, "ship 2 must be hidden before the satellite")
		}
	case <-time.After(time.Second):
		t.Fatal("no initial patch")
	}

	// Deploy a satellite from ship 1 — it reveals the whole sector.
	reply := make(chan sector.InstallSatelliteResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{PlayerID: 7, ShipID: 1, Reply: reply}))
	require.NoError(t, (<-reply).Err)

	deadline := time.After(time.Second)
	for {
		select {
		case patch := <-sub.Patch:
			for _, sh := range patch.Added {
				if sh.ID == 2 {
					return // success: the far ship is now revealed
				}
			}
		case <-deadline:
			t.Fatal("ship 2 not revealed after satellite install")
		}
	}
}

// TestUnit_Satellite_RevealGatedByOwner: a live satellite reveals the sector
// only to its owner and the owner's allies (phase 10.20 L5). A neutral player
// gets no reveal, so a contact beyond their own small radar stays hidden; the
// owner and a clan ally see it.
func TestUnit_Satellite_RevealGatedByOwner(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// player 9 is allied to the satellite owner (7); player 8 is neutral.
	rel := fakeRelations{pairs: map[[2]domain.PlayerID]domain.Relation{{7, 9}: domain.RelationFriend}}
	sat := domain.Satellite{ID: 1, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, Built: true}
	statics := map[domain.SectorID]domain.SectorStatics{testSector: {Satellites: []domain.Satellite{sat}}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 5 * time.Millisecond, InboxCapacity: 64, AOIRadius: 5000, SatelliteRevealRadius: 10000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {
			{ID: 1, PlayerID: 7, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 100, MaxSpeed: 1},
			{ID: 2, PlayerID: 8, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 100, MaxSpeed: 1},
			{ID: 3, PlayerID: 9, Pos: domain.Vec2{X: 0, Y: 0}, RadarRange: 100, MaxSpeed: 1},
			{ID: 4, PlayerID: 99, Pos: domain.Vec2{X: 2000, Y: 0}, RadarRange: 100, MaxSpeed: 1}, // far contact
		}},
		sector.WithStatics(statics),
		sector.WithRelations(rel),
	)
	go func() { _ = w.Run(ctx) }()

	// Owner (7): the satellite reveals the far contact (ship 4).
	sub7, unsub7, err := w.Subscribe(ctx, testSector, 7)
	require.NoError(t, err)
	defer unsub7()
	assert.Contains(t, readInitialAddedIDs(t, sub7.Patch), domain.ShipID(4),
		"owner sees the far contact via their own satellite")

	// Ally (9): also revealed.
	sub9, unsub9, err := w.Subscribe(ctx, testSector, 9)
	require.NoError(t, err)
	defer unsub9()
	assert.Contains(t, readInitialAddedIDs(t, sub9.Patch), domain.ShipID(4),
		"clan ally of the owner sees the far contact too")

	// Neutral (8): no reveal — the far contact stays beyond their 100-unit radar.
	sub8, unsub8, err := w.Subscribe(ctx, testSector, 8)
	require.NoError(t, err)
	defer unsub8()
	assert.NotContains(t, readInitialAddedIDs(t, sub8.Patch), domain.ShipID(4),
		"neutral player gets no reveal from someone else's satellite")
}
