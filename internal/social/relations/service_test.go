package relations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/social/relations"
)

// fakeRepo is an in-memory relations store for Service tests.
type fakeRepo struct {
	rows map[[2]domain.EntityRef]domain.Relation
}

func newFakeRepo(rows ...relations.Row) *fakeRepo {
	m := map[[2]domain.EntityRef]domain.Relation{}
	for _, r := range rows {
		m[[2]domain.EntityRef{r.From, r.To}] = r.Status
	}
	return &fakeRepo{rows: m}
}

func (f *fakeRepo) LoadAll(_ context.Context) ([]relations.Row, error) {
	out := make([]relations.Row, 0, len(f.rows))
	for k, v := range f.rows {
		out = append(out, relations.Row{From: k[0], To: k[1], Status: v})
	}
	return out, nil
}

func (f *fakeRepo) Upsert(_ context.Context, from, to domain.EntityRef, status domain.Relation) error {
	f.rows[[2]domain.EntityRef{from, to}] = status
	return nil
}

func (f *fakeRepo) Delete(_ context.Context, from, to domain.EntityRef) error {
	delete(f.rows, [2]domain.EntityRef{from, to})
	return nil
}

type fakeMembers map[domain.PlayerID]domain.ClanID

func (m fakeMembers) LoadAllMemberships(_ context.Context) (map[domain.PlayerID]domain.ClanID, error) {
	return m, nil
}

func primed(t *testing.T, repo relations.Repo, members fakeMembers) *relations.Service {
	t.Helper()
	svc := relations.New(repo, members)
	require.NoError(t, svc.Precount(context.Background()))
	return svc
}

var (
	p1 = domain.PlayerRef(1)
	p2 = domain.PlayerRef(2)
	p3 = domain.PlayerRef(3)
)

func TestUnit_Relations_SelfIsFriend(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(), fakeMembers{})
	assert.Equal(t, domain.RelationFriend, svc.Get(p1, p1))
}

func TestUnit_Relations_NeutralByDefault(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(), fakeMembers{})
	assert.Equal(t, domain.RelationNeutral, svc.Get(p1, p2))
	assert.False(t, svc.IsHostile(p1, p2))
}

func TestUnit_Relations_SameClanFriend(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(), fakeMembers{1: 10, 2: 10})
	assert.Equal(t, domain.RelationFriend, svc.Get(p1, p2))
}

func TestUnit_Relations_DirectHostileIsMutual(t *testing.T) {
	t.Parallel()
	// Only p1 → p2 declared; Get must see it from both sides (war is mutual
	// for hostility).
	svc := primed(t, newFakeRepo(relations.Row{From: p1, To: p2, Status: domain.RelationHostile}), fakeMembers{})
	assert.True(t, svc.IsHostile(p1, p2))
	assert.True(t, svc.IsHostile(p2, p1))
}

func TestUnit_Relations_ClanWarPropagatesToMembers(t *testing.T) {
	t.Parallel()
	// Clans 10 and 20 at war; p1∈10, p2∈20 → members are at war. (criterion:
	// "объявление войны клану меняет отношения у всех членов")
	clanX := domain.ClanRef(10)
	clanY := domain.ClanRef(20)
	svc := primed(t,
		newFakeRepo(relations.Row{From: clanX, To: clanY, Status: domain.RelationAtWar}),
		fakeMembers{1: 10, 2: 20, 3: 10},
	)
	assert.Equal(t, domain.RelationAtWar, svc.Get(p1, p2))
	assert.True(t, svc.IsHostile(p2, p1))
	// A third member of clan X is likewise at war with p2.
	assert.True(t, svc.IsHostile(p3, p2))
	// Two members of the same clan stay friendly.
	assert.Equal(t, domain.RelationFriend, svc.Get(p1, p3))
}

func TestUnit_Relations_FriendDeclaration(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(relations.Row{From: p1, To: p2, Status: domain.RelationFriend}), fakeMembers{})
	assert.Equal(t, domain.RelationFriend, svc.Get(p1, p2))
}

func TestUnit_Relations_HostilityTakesPrecedenceOverFriend(t *testing.T) {
	t.Parallel()
	// Individuals declared friends, but their clans are at war → hostility
	// wins (members of warring clans are hostile).
	clanX := domain.ClanRef(10)
	clanY := domain.ClanRef(20)
	svc := primed(t,
		newFakeRepo(
			relations.Row{From: p1, To: p2, Status: domain.RelationFriend},
			relations.Row{From: clanX, To: clanY, Status: domain.RelationAtWar},
		),
		fakeMembers{1: 10, 2: 20},
	)
	assert.Equal(t, domain.RelationAtWar, svc.Get(p1, p2))
}

func TestUnit_Relations_Set(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := primed(t, newFakeRepo(), fakeMembers{})

	require.NoError(t, svc.Set(ctx, p1, p2, domain.RelationHostile))
	assert.True(t, svc.IsHostile(p1, p2))

	// Reset to neutral clears it.
	require.NoError(t, svc.Set(ctx, p1, p2, domain.RelationNeutral))
	assert.Equal(t, domain.RelationNeutral, svc.Get(p1, p2))
}

func BenchmarkUnit_Relations_Get(b *testing.B) {
	clanX := domain.ClanRef(10)
	clanY := domain.ClanRef(20)
	svc := relations.New(
		newFakeRepo(relations.Row{From: clanX, To: clanY, Status: domain.RelationAtWar}),
		fakeMembers{1: 10, 2: 20},
	)
	_ = svc.Precount(context.Background())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = svc.Get(p1, p2)
	}
}
