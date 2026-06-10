package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnit_SellDecision_CreditsHalfBasePrice(t *testing.T) {
	t.Parallel()
	credit, err := sellDecision(1000, 0.5, 2, false)
	require.NoError(t, err)
	assert.EqualValues(t, 500, credit)
}

func TestUnit_SellDecision_RejectsLastShip(t *testing.T) {
	t.Parallel()
	_, err := sellDecision(1000, 0.5, 1, false)
	assert.ErrorIs(t, err, errSellLastShip)
}

func TestUnit_SellDecision_RejectsActiveShip(t *testing.T) {
	t.Parallel()
	_, err := sellDecision(1000, 0.5, 3, true)
	assert.ErrorIs(t, err, errSellActiveShip)
}

func TestUnit_SellDecision_ZeroBasePriceCreditsZero(t *testing.T) {
	t.Parallel()
	credit, err := sellDecision(0, 0.5, 2, false)
	require.NoError(t, err)
	assert.Zero(t, credit)
}
