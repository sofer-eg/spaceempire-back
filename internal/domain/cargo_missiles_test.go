package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/domain"
)

// TestUnit_MissileGoodsTypes_AllFiveClassesInOrder pins the ct_missiles.cargo_id
// mapping the whole feature hangs on: class 1..5 → goods 10..14, in class order,
// with no gaps and no duplicates. Spelled out literally rather than built from the
// constants so a constant aimed at the wrong id fails here (the ids' existence in
// the served catalog is a separate check —
// api.TestUnit_AmmunitionGoodsIDsAreInTheCatalog).
func TestUnit_MissileGoodsTypes_AllFiveClassesInOrder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []domain.GoodsTypeID{10, 11, 12, 13, 14}, domain.MissileGoodsTypes(),
		"class 1..5 map to ct_missiles.cargo_id 10..14 in order")
}
