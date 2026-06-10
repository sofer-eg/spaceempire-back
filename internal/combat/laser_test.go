package combat_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

func newAttacker() *domain.Ship {
	return &domain.Ship{
		ID:              1,
		Pos:             domain.Vec2{X: 0, Y: 0},
		Energy:          10,
		MaxEnergy:       10,
		LaserDamage:     20,
		LaserRange:      100,
		LaserEnergyCost: 5,
	}
}

func newTarget(id int64, pos domain.Vec2) *domain.Ship {
	return &domain.Ship{
		ID:        domain.ShipID(id),
		Pos:       pos,
		HP:        50,
		MaxHP:     50,
		Shield:    30,
		MaxShield: 30,
	}
}

func TestUnit_FireLaser_OutOfRange(t *testing.T) {
	t.Parallel()
	a := newAttacker()
	tgt := newTarget(2, domain.Vec2{X: 200, Y: 0})
	beam, ok := combat.FireLaser(a, tgt)
	require.False(t, ok)
	require.Equal(t, combat.LaserBeam{}, beam)
	require.Equal(t, 10, a.Energy, "energy must not be debited on a miss")
	require.Equal(t, 30, tgt.Shield)
	require.Equal(t, 50, tgt.HP)
}

func TestUnit_FireLaser_OutOfEnergy(t *testing.T) {
	t.Parallel()
	a := newAttacker()
	a.Energy = 4 // less than cost (5)
	tgt := newTarget(2, domain.Vec2{X: 50, Y: 0})
	beam, ok := combat.FireLaser(a, tgt)
	require.False(t, ok)
	require.Equal(t, 4, a.Energy)
	require.Equal(t, combat.LaserBeam{}, beam)
}

func TestUnit_FireLaser_NoLaserModule(t *testing.T) {
	t.Parallel()
	a := newAttacker()
	a.LaserDamage = 0
	tgt := newTarget(2, domain.Vec2{X: 50, Y: 0})
	beam, ok := combat.FireLaser(a, tgt)
	require.False(t, ok)
	require.Equal(t, 10, a.Energy)
	require.Equal(t, combat.LaserBeam{}, beam)
}

func TestUnit_FireLaser_DeadTarget(t *testing.T) {
	t.Parallel()
	a := newAttacker()
	tgt := newTarget(2, domain.Vec2{X: 50, Y: 0})
	tgt.HP = 0
	beam, ok := combat.FireLaser(a, tgt)
	require.False(t, ok)
	require.Equal(t, 10, a.Energy)
	require.Equal(t, combat.LaserBeam{}, beam)
}

func TestUnit_FireLaser_Hit(t *testing.T) {
	t.Parallel()
	a := newAttacker()
	tgt := newTarget(2, domain.Vec2{X: 50, Y: 0})
	beam, ok := combat.FireLaser(a, tgt)
	require.True(t, ok)
	require.Equal(t, 5, a.Energy, "energy debited by LaserEnergyCost")
	// Damage 20 > shield 30 → shield=10, HP unchanged. Actually 30-20=10.
	require.Equal(t, 10, tgt.Shield)
	require.Equal(t, 50, tgt.HP)
	require.Equal(t, 20, beam.DamageDealt)
	require.False(t, beam.Killed)
	require.Equal(t, domain.ShipID(1), beam.AttackerShipID)
	require.Equal(t, domain.EntityKindShip, beam.Target.Kind)
	require.Equal(t, int64(2), beam.Target.ID)
}

func TestUnit_FireLaser_Kill(t *testing.T) {
	t.Parallel()
	a := newAttacker()
	a.LaserDamage = 200
	a.Energy = 10
	tgt := newTarget(2, domain.Vec2{X: 50, Y: 0})
	beam, ok := combat.FireLaser(a, tgt)
	require.True(t, ok)
	require.Equal(t, 0, tgt.HP)
	require.Equal(t, 0, tgt.Shield)
	require.True(t, beam.Killed)
}

func TestUnit_FireLaser_NilAttacker(t *testing.T) {
	t.Parallel()
	tgt := newTarget(2, domain.Vec2{X: 50, Y: 0})
	_, ok := combat.FireLaser(nil, tgt)
	require.False(t, ok)
}

func TestUnit_FireLaser_NilTarget(t *testing.T) {
	t.Parallel()
	a := newAttacker()
	_, ok := combat.FireLaser(a, nil)
	require.False(t, ok)
}
