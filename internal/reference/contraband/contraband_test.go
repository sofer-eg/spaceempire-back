package contraband_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/reference/contraband"
)

func TestUnit_Contraband_SlavesIllegalForMainRaces(t *testing.T) {
	t.Parallel()
	for race := domain.RaceID(1); race <= 5; race++ {
		assert.True(t, contraband.IsIllegal(race, contraband.Slaves),
			"slaves must be contraband for main race %d", race)
	}
}

func TestUnit_Contraband_SlavesLegalForPirates(t *testing.T) {
	t.Parallel()
	assert.False(t, contraband.IsIllegal(6, contraband.Slaves), "pirates trade slaves freely")
}

func TestUnit_Contraband_OrdinaryGoodsLegal(t *testing.T) {
	t.Parallel()
	const battery = domain.GoodsTypeID(1)
	assert.False(t, contraband.IsIllegal(1, battery))
}
