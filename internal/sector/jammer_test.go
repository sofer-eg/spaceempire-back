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

type fakeJammerRepo struct {
	deleted []domain.JammerID
}

func (f *fakeJammerRepo) Delete(_ context.Context, id domain.JammerID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func jammerRef(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindJammer, ID: id}
}

// TestUnit_Jammer_InstallAddsToLayoutAndCombat: the install command persists a
// jammer through the transactional installer (the only install path since
// TASK-144), drops it into the rendered layout at the ship's position, and into
// the live combat set so lasers can target it.
func TestUnit_Jammer_InstallAddsToLayoutAndCombat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		[]domain.Ship{installerTestShip()})

	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply}))
	w.Tick(ctx)

	res := <-reply
	require.NoError(t, res.Err)
	require.NotZero(t, res.JammerID)
	require.Len(t, inst.jammers, 1, "install persisted through the installer")
	require.Equal(t, domain.Vec2{X: 30, Y: -40}, inst.jammers[0].Pos)
	require.NotNil(t, inst.jammers[0].OwnerID)
	require.Equal(t, domain.PlayerID(7), *inst.jammers[0].OwnerID)

	snap := w.Snapshot(testSector)
	require.Len(t, snap.Statics.Jammers, 1, "jammer in rendered layout")
	assert.Equal(t, domain.Vec2{X: 30, Y: -40}, snap.Statics.Jammers[0].Pos)
	_, ok := findDestructible(snap, jammerRef(int64(res.JammerID)))
	assert.True(t, ok, "jammer in live combat set")
}

// TestUnit_Jammer_InstallForeignShipForbidden: a player cannot deploy a jammer
// from a ship that is not theirs.
func TestUnit_Jammer_InstallForeignShipForbidden(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		[]domain.Ship{installerTestShip()})

	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{PlayerID: 99, ShipID: 1, GoodsType: 27, Reply: reply}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, sector.ErrForbidden)
	assert.Empty(t, inst.jammers, "nothing installed for a foreign ship")
	assert.Empty(t, w.Snapshot(testSector).Statics.Jammers)
}

// TestUnit_Jammer_InstallDockedRejected: a docked ship cannot deploy — the
// generator is placed in open space at the ship's position.
func TestUnit_Jammer_InstallDockedRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	docked := domain.EntityRef{Kind: domain.EntityKindStation, ID: 3}
	ship := installerTestShip()
	ship.Docked = &docked
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		[]domain.Ship{ship})

	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply}))
	w.Tick(ctx)

	require.ErrorIs(t, (<-reply).Err, sector.ErrShipDocked)
	assert.Empty(t, inst.jammers, "nothing persisted for a docked ship")
	assert.Equal(t, 0, inst.debits, "and nothing charged")
	assert.Empty(t, w.Snapshot(testSector).Statics.Jammers)
}

// TestUnit_Jammer_DestructionPersisted: a jammer killed in combat is removed
// from the layout and deleted via the repo so a restart will not resurrect it
// (mirrors the laser-tower 8.5 / satellite 10.15 contract).
func TestUnit_Jammer_DestructionPersisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	jam := domain.Jammer{ID: 9, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0}, HP: 100, Built: true}
	attacker := staticAttacker(1, 100, domain.Vec2{X: 0, Y: 0}, 1000, jammerRef(9))

	repo := &fakeJammerRepo{}
	statics := map[domain.SectorID]domain.SectorStatics{testSector: {Jammers: []domain.Jammer{jam}}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {attacker}},
		sector.WithStatics(statics),
		sector.WithHostility(ownerBasedHostility),
		sector.WithJammers(repo),
	)
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.Jammers, "destroyed jammer gone from layout")
	require.Equal(t, []domain.JammerID{9}, repo.deleted, "jammer destruction persisted (delete)")
}
