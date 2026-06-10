package production_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/production"
)

// stubStore is a memory-backed production.StationStore: one station by id,
// or a pre-set error.
type stubStore struct {
	station domain.Station
	err     error
}

func (s stubStore) GetStation(_ context.Context, _ domain.StationID) (domain.Station, error) {
	if s.err != nil {
		return domain.Station{}, s.err
	}
	return s.station, nil
}

func TestUnit_StationCycle_Producing(t *testing.T) {
	t.Parallel()
	next := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	reader := production.NewReader(buildBalance(t), stubStore{
		station: domain.Station{ID: 7, Type: stationType, InProgress: true, NextCycleAt: next},
	})

	info, err := reader.StationCycle(context.Background(), 7)
	require.NoError(t, err)
	assert.True(t, info.Produces)
	assert.True(t, info.InProgress)
	assert.Equal(t, next, info.NextCycleAt)
	assert.Equal(t, time.Second, info.CycleTime)
}

func TestUnit_StationCycle_NoRecipe(t *testing.T) {
	t.Parallel()
	reader := production.NewReader(buildBalance(t), stubStore{
		station: domain.Station{ID: 7, Type: 999},
	})

	info, err := reader.StationCycle(context.Background(), 7)
	require.NoError(t, err)
	assert.False(t, info.Produces)
	assert.Zero(t, info.CycleTime)
}

func TestUnit_StationCycle_StoreError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	reader := production.NewReader(buildBalance(t), stubStore{err: want})

	_, err := reader.StationCycle(context.Background(), 7)
	require.ErrorIs(t, err, want)
}
