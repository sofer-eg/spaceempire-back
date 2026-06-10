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

type fakeTowerRepo struct {
	deleted []domain.LaserTowerID
}

func (f *fakeTowerRepo) Delete(_ context.Context, id domain.LaserTowerID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// TestUnit_Tower_DestructionPersisted: a tower killed in combat is deleted via
// the tower repo (8.5) so a restart will not resurrect it.
func TestUnit_Tower_DestructionPersisted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tower := domain.LaserTower{ID: 5, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0}, HP: 100, Built: true}
	attacker := staticAttacker(1, 100, domain.Vec2{X: 0, Y: 0}, 1000,
		domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 5})

	repo := &fakeTowerRepo{}
	statics := map[domain.SectorID]domain.SectorStatics{testSector: {LaserTowers: []domain.LaserTower{tower}}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {attacker}},
		sector.WithStatics(statics),
		sector.WithHostility(ownerBasedHostility),
		sector.WithTowerPersistence(repo),
	)
	w.Tick(ctx)

	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.LaserTowers, "destroyed tower gone from layout")
	require.Equal(t, []domain.LaserTowerID{5}, repo.deleted, "tower destruction persisted (delete)")
}

// TestUnit_Tower_DestructionWithoutRepoNoPanic: without WithTowerPersistence a
// tower kill is RAM-only and must not panic.
func TestUnit_Tower_DestructionWithoutRepoNoPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tower := domain.LaserTower{ID: 5, OwnerID: ownerPtr(7), SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 0}, HP: 100, Built: true}
	attacker := staticAttacker(1, 100, domain.Vec2{X: 0, Y: 0}, 1000,
		domain.EntityRef{Kind: domain.EntityKindLaserTower, ID: 5})

	statics := map[domain.SectorID]domain.SectorStatics{testSector: {LaserTowers: []domain.LaserTower{tower}}}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {attacker}},
		sector.WithStatics(statics),
		sector.WithHostility(ownerBasedHostility),
	)
	w.Tick(ctx)
	assert.Empty(t, w.Snapshot(testSector).Statics.LaserTowers)
}
