package sector

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

func ast(id domain.AsteroidID, x, y float64, mass int64) domain.Asteroid {
	return domain.Asteroid{ID: id, SectorID: 1, Pos: domain.Vec2{X: x, Y: y}, Mass: mass, OreType: 2}
}

func TestUnit_DiffAsteroids_AddedUpdatedRemoved(t *testing.T) {
	t.Parallel()

	prev := map[domain.AsteroidID]domain.Asteroid{
		1: ast(1, 0, 0, 100),
		2: ast(2, 5, 5, 100), // will be mined down
		3: ast(3, 7, 7, 100), // will deplete / leave
	}
	curr := map[domain.AsteroidID]domain.Asteroid{
		1: ast(1, 0, 0, 100), // unchanged
		2: ast(2, 5, 5, 80),  // mass changed -> updated
		4: ast(4, 9, 9, 100), // added
		// 3 removed
	}

	added, updated, removed := diffAsteroids(prev, curr)

	require.Len(t, added, 1)
	assert.Equal(t, domain.AsteroidID(4), added[0].ID)
	require.Len(t, updated, 1)
	assert.Equal(t, domain.AsteroidID(2), updated[0].ID)
	assert.Equal(t, int64(80), updated[0].Mass)
	require.Len(t, removed, 1)
	assert.Equal(t, domain.AsteroidID(3), removed[0])
}

func TestUnit_DiffAsteroids_FirstDeliveryIsAllAdded(t *testing.T) {
	t.Parallel()

	curr := map[domain.AsteroidID]domain.Asteroid{
		1: ast(1, 0, 0, 100),
		2: ast(2, 1, 1, 100),
	}
	added, updated, removed := diffAsteroids(nil, curr)

	require.Len(t, added, 2)
	ids := []domain.AsteroidID{added[0].ID, added[1].ID}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	assert.Equal(t, []domain.AsteroidID{1, 2}, ids)
	assert.Empty(t, updated)
	assert.Empty(t, removed)
}

func TestUnit_DiffAsteroids_EmptyWhenNoChange(t *testing.T) {
	t.Parallel()

	curr := map[domain.AsteroidID]domain.Asteroid{1: ast(1, 1, 1, 100)}
	added, updated, removed := diffAsteroids(curr, curr)
	assert.Empty(t, added)
	assert.Empty(t, updated)
	assert.Empty(t, removed)
}

func TestUnit_AsteroidsInRadius_FiltersOutsideAOI(t *testing.T) {
	t.Parallel()

	near := ast(1, 10, 0, 100) // inside radius 50
	far := ast(2, 100, 0, 100) // outside radius 50
	src := map[domain.AsteroidID]*domain.Asteroid{1: &near, 2: &far}

	got := asteroidsInRadius(src, domain.Vec2{X: 0, Y: 0}, 50)

	require.Len(t, got, 1)
	_, hasNear := got[1]
	_, hasFar := got[2]
	assert.True(t, hasNear, "asteroid inside AOI must be returned")
	assert.False(t, hasFar, "asteroid outside AOI must be filtered out")
}

func TestUnit_AsteroidsInRadius_ZeroRadiusReturnsAll(t *testing.T) {
	t.Parallel()

	a := ast(1, 1000, 1000, 100)
	src := map[domain.AsteroidID]*domain.Asteroid{1: &a}

	got := asteroidsInRadius(src, domain.Vec2{}, 0)
	require.Len(t, got, 1, "radius<=0 disables the AOI filter")
}
