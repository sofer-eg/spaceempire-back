package combat_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/combat"
	"spaceempire/back/internal/domain"
)

// TestUnit_TorpedoSpecs_Profile pins the ЧТЗ §5.1 relative-parity profile of the
// two torpedo classes through the public DefaultTorpedoSpec accessor rather than
// literal magnitudes (a balance decision, C-01): each class hits far harder than
// a missile, is slower and less nimble, carries area splash and its own finite
// HP, and class 3 dominates class 2 on every axis.
func TestUnit_TorpedoSpecs_Profile(t *testing.T) {
	t.Parallel()

	c2 := combat.DefaultTorpedoSpec(2)
	c3 := combat.DefaultTorpedoSpec(3)

	mis := combat.DefaultMissileSpec()
	for name, s := range map[string]combat.TorpedoSpec{"class2": c2, "class3": c3} {
		// "≫", not just ">": a torpedo is the kratno-mocnee heavy-hitter of
		// §5.1, so pin a several-times margin over a missile rather than a
		// one-point edge that a rebalance could erode while still passing.
		assert.GreaterOrEqualf(t, s.Damage, 3*mis.Damage,
			"%s: Damage must be >> missile (at least 3×%d)", name, mis.Damage)
		assert.Lessf(t, s.Speed, mis.Speed, "%s: Speed must be < missile (slower)", name)
		assert.Lessf(t, s.TurnRate, mis.TurnRate, "%s: TurnRate must be < missile (less nimble)", name)
		assert.Positivef(t, s.SplashRadius, "%s: SplashRadius must be > 0 (area weapon)", name)
		assert.Positivef(t, s.HP, "%s: HP must be > 0 (shoot-downable)", name)
		assert.Positivef(t, s.TTL, "%s: TTL must be finite and > 0", name)
	}

	// Class 3 is the stronger, pricier tier on every axis (ЧТЗ §5.1).
	assert.Greater(t, c3.Damage, c2.Damage, "class3 Damage > class2")
	assert.Greater(t, c3.Speed, c2.Speed, "class3 Speed > class2")
	assert.Greater(t, c3.Accel, c2.Accel, "class3 Accel > class2")
	assert.Greater(t, c3.TurnRate, c2.TurnRate, "class3 TurnRate > class2")
	assert.GreaterOrEqual(t, c3.HitRadius, c2.HitRadius, "class3 HitRadius >= class2")
	assert.Greater(t, c3.SplashRadius, c2.SplashRadius, "class3 SplashRadius > class2")
	assert.Greater(t, c3.TTL, c2.TTL, "class3 TTL > class2")
	assert.GreaterOrEqual(t, c3.HP, c2.HP, "class3 HP >= class2")
}

// TestUnit_DefaultTorpedoSpec_UnknownClassFallsBack: the handler validates the
// class upstream, but the accessor must never hand back a degenerate spec — an
// unknown class yields the class-2 profile.
func TestUnit_DefaultTorpedoSpec_UnknownClassFallsBack(t *testing.T) {
	t.Parallel()
	assert.Equal(t, combat.DefaultTorpedoSpec(2), combat.DefaultTorpedoSpec(99))
}

// TestUnit_TorpedoAmmo_Class3DearerThanClass2 pins the remaining §5.1 parity
// axis — price. The combat magnitude spec carries no price (it is a balance-
// config concern, not a kinematic one): class 2 is fired with gt23
// "Огненная Буря", class 3 with gt24 "Святая Торпеда". This grounds the
// "класс 3 ... заметно дороже класса 2" relation (ЧТЗ §5.1, by ratio not the
// literal original numbers, C-01) on the real balance config, so a rebalance
// that inverts the tiers trips here.
func TestUnit_TorpedoAmmo_Class3DearerThanClass2(t *testing.T) {
	t.Parallel()
	b, err := balance.LoadFromFile(filepath.Join("..", "..", "configs", "balance.yaml"))
	require.NoError(t, err)

	const (
		gt23 = domain.GoodsTypeID(23) // class-2 ammunition
		gt24 = domain.GoodsTypeID(24) // class-3 ammunition
	)
	firestorm, ok := b.Get(gt23)
	require.True(t, ok, "class-2 ammunition gt23 present in the goods catalog")
	holy, ok := b.Get(gt24)
	require.True(t, ok, "class-3 ammunition gt24 present in the goods catalog")

	require.Positive(t, firestorm.AvgPrice, "class-2 ammunition has a price")
	require.Greater(t, holy.AvgPrice, firestorm.AvgPrice,
		"class-3 ammunition (gt24) is the pricier tier than class-2 (gt23) — §5.1 price axis")
}
