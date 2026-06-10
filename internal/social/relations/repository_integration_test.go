package relations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/social/relations"
)

// TestIntegration_Relations_RoundTrip upserts, updates, loads and deletes a
// declared relation against real Postgres. The relations table has no FKs
// (endpoints are generic kind+id pairs), so no fixtures are needed.
func TestIntegration_Relations_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := relations.NewRepository(pool)

	from := domain.ClanRef(10)
	to := domain.ClanRef(20)

	require.NoError(t, repo.Upsert(ctx, from, to, domain.RelationAtWar))

	rows, err := repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, from, rows[0].From)
	assert.Equal(t, to, rows[0].To)
	assert.Equal(t, domain.RelationAtWar, rows[0].Status)

	// Upsert the same pair with a softer status — must update in place.
	require.NoError(t, repo.Upsert(ctx, from, to, domain.RelationHostile))
	rows, err = repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "upsert must not duplicate the pair")
	assert.Equal(t, domain.RelationHostile, rows[0].Status)

	// Delete clears it.
	require.NoError(t, repo.Delete(ctx, from, to))
	rows, err = repo.LoadAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
}
