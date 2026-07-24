package quests_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	questsrepo "spaceempire/back/internal/persistence/quests"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
)

// TestIntegration_QuestOffers_CRUD exercises the offer store against real
// Postgres: insert→get roundtrip (incl. JSONB definition), ownership scoping,
// active-vs-expired filtering, counting, and both delete paths.
func TestIntegration_QuestOffers_CRUD(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := questsrepo.NewOffers(pool)

	owner := seedPlayer(t, pool, "offer-owner")
	other := seedPlayer(t, pool, "offer-other")

	now := time.Now().UTC().Truncate(time.Microsecond)
	offer := domain.QuestOffer{
		Player:     owner,
		TemplateID: "deliver",
		Definition: []byte(`{"kind":"deliver","qty":7}`),
		Source:     "dock",
		CreatedAt:  now,
		ExpiresAt:  now.Add(24 * time.Hour),
	}
	id, err := repo.InsertOffer(ctx, offer)
	require.NoError(t, err)
	assert.NotZero(t, id)

	// insert→get roundtrip.
	got, ok, err := repo.GetOffer(ctx, id, owner)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, owner, got.Player)
	assert.Equal(t, "deliver", got.TemplateID)
	assert.Equal(t, "dock", got.Source)
	assert.JSONEq(t, `{"kind":"deliver","qty":7}`, string(got.Definition))
	assert.True(t, got.CreatedAt.Equal(now))
	assert.True(t, got.ExpiresAt.Equal(now.Add(24*time.Hour)))

	// Ownership: another player cannot read the offer.
	_, ok, err = repo.GetOffer(ctx, id, other)
	require.NoError(t, err)
	assert.False(t, ok, "offer is scoped to its owner")

	// Story offer: NULL definition roundtrips as nil.
	storyID, err := repo.InsertOffer(ctx, domain.QuestOffer{
		Player:     owner,
		TemplateID: "xt_saga1",
		Definition: nil,
		Source:     "space",
		CreatedAt:  now,
		ExpiresAt:  now.Add(24 * time.Hour),
	})
	require.NoError(t, err)
	story, ok, err := repo.GetOffer(ctx, storyID, owner)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Nil(t, story.Definition, "story offer has NULL definition")

	// An expired offer for the same owner.
	_, err = repo.InsertOffer(ctx, domain.QuestOffer{
		Player:     owner,
		TemplateID: "kill",
		Source:     "dock",
		CreatedAt:  now.Add(-48 * time.Hour),
		ExpiresAt:  now.Add(-time.Hour),
	})
	require.NoError(t, err)

	// ListActive skips the expired one; ordered by created_at.
	active, err := repo.ListActiveOffersByPlayer(ctx, owner, now)
	require.NoError(t, err)
	require.Len(t, active, 2)
	assert.Equal(t, id, active[0].ID)
	assert.Equal(t, storyID, active[1].ID)

	// CountActive matches ListActive.
	n, err := repo.CountActiveOffers(ctx, owner, now)
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// DeleteOffer: wrong owner → false, no row removed.
	deleted, err := repo.DeleteOffer(ctx, id, other)
	require.NoError(t, err)
	assert.False(t, deleted)

	// DeleteOffer: correct owner → true.
	deleted, err = repo.DeleteOffer(ctx, id, owner)
	require.NoError(t, err)
	assert.True(t, deleted)
	_, ok, err = repo.GetOffer(ctx, id, owner)
	require.NoError(t, err)
	assert.False(t, ok, "deleted offer is gone")

	// DeleteExpiredOffers purges only the TTL-elapsed row.
	purged, err := repo.DeleteExpiredOffers(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), purged)
	n, err = repo.CountActiveOffers(ctx, owner, now)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the story offer remains active")
}

// TestIntegration_QuestCounters_Upsert covers absent→get (ok=false), insert via
// UPSERT, and a conflicting re-UPSERT that overwrites the row.
func TestIntegration_QuestCounters_Upsert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := questsrepo.NewCounters(pool)

	player := seedPlayer(t, pool, "pacer")

	// Absent counters read as ok=false.
	_, ok, err := repo.GetCounters(ctx, player)
	require.NoError(t, err)
	assert.False(t, ok)

	// First UPSERT inserts.
	require.NoError(t, repo.UpsertCounters(ctx, domain.QuestCounters{
		Player: player, Docks: 3, Jumps: 1, NextDocks: 12, NextJumps: 15,
	}))
	got, ok, err := repo.GetCounters(ctx, player)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, domain.QuestCounters{
		Player: player, Docks: 3, Jumps: 1, NextDocks: 12, NextJumps: 15,
	}, got)

	// Second UPSERT on conflict overwrites all counter columns.
	require.NoError(t, repo.UpsertCounters(ctx, domain.QuestCounters{
		Player: player, Docks: 0, Jumps: 2, NextDocks: 18, NextJumps: 0,
	}))
	got, ok, err = repo.GetCounters(ctx, player)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, domain.QuestCounters{
		Player: player, Docks: 0, Jumps: 2, NextDocks: 18, NextJumps: 0,
	}, got)
}

// TestIntegration_QuestProcedural_MigrationDownUp verifies migration 0060 is
// reversible on PG16: the harness applies it (up), we roll it back (down) and
// re-apply it (up), then confirm the schema round-trips by inserting an offer
// referencing player_quests.definition again (AC-6).
func TestIntegration_QuestProcedural_MigrationDownUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	dsn := pool.Config().ConnString()

	// Down rolls back the most recent migration (0060), then up re-applies it.
	require.NoError(t, database.MigrateDown(ctx, dsn), "roll back 0060")
	require.NoError(t, database.MigrateUp(ctx, dsn), "re-apply 0060")

	// Schema is back and usable end-to-end.
	repo := questsrepo.NewOffers(pool)
	player := seedPlayer(t, pool, "after-remigrate")
	now := time.Now().UTC()
	id, err := repo.InsertOffer(ctx, domain.QuestOffer{
		Player:     player,
		TemplateID: "courier",
		Source:     "dock",
		CreatedAt:  now,
		ExpiresAt:  now.Add(time.Hour),
	})
	require.NoError(t, err)
	_, ok, err := repo.GetOffer(ctx, id, player)
	require.NoError(t, err)
	assert.True(t, ok)

	// player_quests.definition column exists again.
	_, err = pool.Exec(ctx,
		`INSERT INTO player_quests (player_id, quest_id, definition) VALUES ($1, 'proc:x', $2)`,
		int64(player), []byte(`{"kind":"courier"}`))
	require.NoError(t, err)
}
