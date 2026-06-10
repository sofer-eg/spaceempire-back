package combat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

func playerID(id int64) *domain.PlayerID {
	p := domain.PlayerID(id)
	return &p
}

// ownerBased is the owner-keyed stand-in for relations (6.2) used to
// exercise the fire path: any ship not owned by the tower owner is hostile.
func ownerBased(towerOwner *domain.PlayerID, ship *domain.Ship) bool {
	if towerOwner == nil {
		return ship.PlayerID != 0
	}
	return ship.PlayerID != *towerOwner
}

func towerShip(id int64, owner domain.PlayerID, pos domain.Vec2) *domain.Ship {
	return &domain.Ship{
		ID:       domain.ShipID(id),
		PlayerID: owner,
		Pos:      pos,
		HP:       50,
		MaxHP:    50,
	}
}

func newTower() domain.LaserTower {
	return domain.LaserTower{
		ID:       1,
		OwnerID:  playerID(7),
		SectorID: 1,
		Pos:      domain.Vec2{X: 0, Y: 0},
	}
}

func TestUnit_SelectTowerTarget_NearestHostile(t *testing.T) {
	t.Parallel()
	spec := combat.DefaultTowerSpec()
	tower := newTower()
	far := towerShip(2, 99, domain.Vec2{X: 100, Y: 0})
	near := towerShip(3, 99, domain.Vec2{X: 40, Y: 0})
	ships := map[domain.ShipID]*domain.Ship{far.ID: far, near.ID: near}

	got := combat.SelectTowerTarget(tower, ships, spec, ownerBased)
	require.NotNil(t, got)
	require.Equal(t, near.ID, got.ID, "must pick the closest hostile ship")
}

func TestUnit_SelectTowerTarget_OutOfRangeExcluded(t *testing.T) {
	t.Parallel()
	spec := combat.DefaultTowerSpec()
	tower := newTower()
	outside := towerShip(2, 99, domain.Vec2{X: spec.Range + 1, Y: 0})
	ships := map[domain.ShipID]*domain.Ship{outside.ID: outside}

	require.Nil(t, combat.SelectTowerTarget(tower, ships, spec, ownerBased))
}

func TestUnit_SelectTowerTarget_OwnShipNotAttacked(t *testing.T) {
	t.Parallel()
	spec := combat.DefaultTowerSpec()
	tower := newTower() // owner 7
	own := towerShip(2, 7, domain.Vec2{X: 30, Y: 0})
	ships := map[domain.ShipID]*domain.Ship{own.ID: own}

	require.Nil(t, combat.SelectTowerTarget(tower, ships, spec, ownerBased),
		"a tower never targets a ship of its own owner")
}

func TestUnit_SelectTowerTarget_DeadShipSkipped(t *testing.T) {
	t.Parallel()
	spec := combat.DefaultTowerSpec()
	tower := newTower()
	dead := towerShip(2, 99, domain.Vec2{X: 20, Y: 0})
	dead.HP = 0
	alive := towerShip(3, 99, domain.Vec2{X: 60, Y: 0})
	ships := map[domain.ShipID]*domain.Ship{dead.ID: dead, alive.ID: alive}

	got := combat.SelectTowerTarget(tower, ships, spec, ownerBased)
	require.NotNil(t, got)
	require.Equal(t, alive.ID, got.ID, "dead ship is skipped even though closer")
}

func TestUnit_SelectTowerTarget_NoHostilitySelectsNothing(t *testing.T) {
	t.Parallel()
	spec := combat.DefaultTowerSpec()
	tower := newTower()
	ship := towerShip(2, 99, domain.Vec2{X: 10, Y: 0})
	ships := map[domain.ShipID]*domain.Ship{ship.ID: ship}

	require.Nil(t, combat.SelectTowerTarget(tower, ships, spec, combat.NoHostility),
		"the production stub never acquires a target")
}
