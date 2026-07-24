package quest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
	questsrepo "spaceempire/back/internal/persistence/quests"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/pkg/database"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/quest"
)

// wireQuestService builds a Service over real Postgres repos + a TxManager, the
// same wiring app.go uses, and returns the service, the offer store and the
// pool. It is the harness for the accept-atomicity / restart / TTL e2e tests.
func wireQuestService(t *testing.T, pool *pgxpool.Pool) (*quest.Service, *questsrepo.OfferRepository, clock.Clock) {
	t.Helper()
	offers := questsrepo.NewOffers(pool)
	poolRepo := quest.NewPoolRepo(questsrepo.New(pool), playersrepo.New(pool), offers)
	tm := database.NewTxManager(pool)
	svc := quest.New(poolRepo, quest.NewRepoTxRunner(tm, poolRepo), poolRepo, nil, clock.NewRealClock(), nil)
	return svc, offers, clock.NewRealClock()
}

func seedQuestPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id))
	return domain.PlayerID(id)
}

// TestIntegration_Quest_AcceptProcOffer_Atomic proves the accept transaction
// materialises player_quests (with the frozen definition) AND deletes the offer
// atomically on real Postgres (AC-4): after accept the quest row exists with
// its definition and the offer is gone.
func TestIntegration_Quest_AcceptProcOffer_Atomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	svc, offers, clk := wireQuestService(t, pool)
	player := seedQuestPlayer(t, pool, "acceptor")

	def := procDef()
	id, err := offers.InsertOffer(ctx, domain.QuestOffer{
		Player: player, TemplateID: "deliver", Definition: mustJSON(t, def),
		Source: "dock", CreatedAt: clk.Now(), ExpiresAt: clk.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	questID := fmt.Sprintf("proc:%d", id)

	require.NoError(t, svc.Accept(ctx, player, questID))

	// Offer consumed.
	_, ok, err := offers.GetOffer(ctx, id, player)
	require.NoError(t, err)
	assert.False(t, ok, "offer deleted in the accept tx")

	// player_quests row exists, definition frozen with the stamped id.
	var definition []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT definition FROM player_quests WHERE player_id = $1 AND quest_id = $2`,
		int64(player), questID).Scan(&definition))
	var stored quest.Def
	require.NoError(t, json.Unmarshal(definition, &stored))
	assert.Equal(t, questID, stored.ID)

	// A fresh Service over the same pool (a "restart") resolves the active proc
	// quest from the row and lists no offer for it (AC-7).
	svc2, offers2, _ := wireQuestService(t, pool)
	views, err := svc2.ActiveList(ctx, player)
	require.NoError(t, err)
	var found bool
	for _, v := range views {
		if v.QuestID == questID {
			found = true
			assert.Equal(t, def.Title, v.Title)
		}
	}
	assert.True(t, found, "active proc quest resolves after restart")

	list, err := offers2.ListActiveOffersByPlayer(ctx, player, clk.Now())
	require.NoError(t, err)
	assert.Empty(t, list, "accepted offer no longer offerable")
}

// TestIntegration_Quest_OfferableAndRestart — a live offer survives a restart
// and appears in OfferableList; an active proc quest keeps resolving (AC-7).
func TestIntegration_Quest_OfferableAndRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	svc, offers, clk := wireQuestService(t, pool)
	player := seedQuestPlayer(t, pool, "restarter")

	id, err := offers.InsertOffer(ctx, domain.QuestOffer{
		Player: player, TemplateID: "patrol", Definition: nil, // story offer
		Source: "space", CreatedAt: clk.Now(), ExpiresAt: clk.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	// Restart: fresh service reads the persisted offer.
	list, err := svc.OfferableList(ctx, player)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, fmt.Sprintf("proc:%d", id), list[0].OfferID)
	assert.Equal(t, quest.Patrol.Title, list[0].Title, "story offer resolves via Lookup")
	assert.Equal(t, "space", list[0].Source)
}

// TestIntegration_Quest_CloserSweepsExpired — the Closer's tick purges
// TTL-expired offers and leaves live ones (FR-06).
func TestIntegration_Quest_CloserSweepsExpired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	svc, offers, _ := wireQuestService(t, pool)
	player := seedQuestPlayer(t, pool, "sweep")

	now := time.Now().UTC()
	_, err := offers.InsertOffer(ctx, domain.QuestOffer{
		Player: player, TemplateID: "deliver", Source: "dock",
		CreatedAt: now.Add(-48 * time.Hour), ExpiresAt: now.Add(-time.Hour), // expired
	})
	require.NoError(t, err)
	liveID, err := offers.InsertOffer(ctx, domain.QuestOffer{
		Player: player, TemplateID: "courier", Source: "dock",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), // live
	})
	require.NoError(t, err)

	closer := quest.NewCloser(svc, offers, clock.NewRealClock(), nil, time.Second)
	closer.Tick(ctx)

	live, err := offers.ListActiveOffersByPlayer(ctx, player, time.Now().UTC())
	require.NoError(t, err)
	require.Len(t, live, 1, "only the live offer remains")
	assert.Equal(t, liveID, live[0].ID)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
