package bounties_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	bountyrepo "spaceempire/back/internal/persistence/bounties"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/social/bounties"
	clansrepo "spaceempire/back/internal/social/clans"
)

// memStore is an in-memory model of the bounty TxRepo + Reader: a bounty map
// plus player cash and clan treasury balances. Wallet adjusts reject going
// negative with the same sentinels the real repos use, so the Service's
// insufficiency mapping is exercised for real.
type memStore struct {
	bounties map[domain.BountyID]domain.Bounty
	cash     map[domain.PlayerID]int64
	treasury map[domain.ClanID]int64
	nextID   domain.BountyID
}

func newStore() *memStore {
	return &memStore{
		bounties: map[domain.BountyID]domain.Bounty{},
		cash:     map[domain.PlayerID]int64{},
		treasury: map[domain.ClanID]int64{},
	}
}

func (m *memStore) CreateBounty(_ context.Context, b domain.Bounty) (domain.BountyID, error) {
	m.nextID++
	b.ID = m.nextID
	m.bounties[b.ID] = b
	return b.ID, nil
}

func (m *memStore) ActiveForTargets(_ context.Context, now time.Time, targets []domain.EntityRef) ([]domain.Bounty, error) {
	want := map[domain.EntityRef]bool{}
	for _, t := range targets {
		want[t] = true
	}
	var out []domain.Bounty
	for _, b := range m.bounties {
		if b.Status == domain.BountyActive && b.ExpiresAt.After(now) && want[b.Target] {
			out = append(out, b)
		}
	}
	return out, nil
}

func (m *memStore) DueExpired(_ context.Context, now time.Time, limit int) ([]domain.Bounty, error) {
	var out []domain.Bounty
	for _, b := range m.bounties {
		if b.Status == domain.BountyActive && !b.ExpiresAt.After(now) {
			out = append(out, b)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (m *memStore) MarkPaid(_ context.Context, id domain.BountyID, paidTo domain.PlayerID, at time.Time) error {
	b := m.bounties[id]
	if b.Status != domain.BountyActive {
		return nil
	}
	b.Status = domain.BountyPaid
	b.PaidTo = paidTo
	b.PaidAt = at
	m.bounties[id] = b
	return nil
}

func (m *memStore) MarkExpired(_ context.Context, id domain.BountyID) error {
	b := m.bounties[id]
	if b.Status != domain.BountyActive {
		return nil
	}
	b.Status = domain.BountyExpired
	m.bounties[id] = b
	return nil
}

func (m *memStore) AdjustCash(_ context.Context, p domain.PlayerID, delta int64) (int64, error) {
	if m.cash[p]+delta < 0 {
		return 0, playersrepo.ErrInsufficientCash
	}
	m.cash[p] += delta
	return m.cash[p], nil
}

func (m *memStore) AdjustTreasury(_ context.Context, c domain.ClanID, delta int64) (int64, error) {
	if m.treasury[c]+delta < 0 {
		return 0, clansrepo.ErrInsufficientTreasury
	}
	m.treasury[c] += delta
	return m.treasury[c], nil
}

func (m *memStore) ListActive(_ context.Context, now time.Time, limit int) ([]bountyrepo.View, error) {
	var out []bountyrepo.View
	for _, b := range m.bounties {
		if b.Status == domain.BountyActive && b.ExpiresAt.After(now) {
			out = append(out, bountyrepo.View{Bounty: b})
		}
	}
	return out, nil
}

func (m *memStore) HistoryForTarget(_ context.Context, target domain.EntityRef, _ int) ([]bountyrepo.View, error) {
	var out []bountyrepo.View
	for _, b := range m.bounties {
		if b.Target == target {
			out = append(out, bountyrepo.View{Bounty: b})
		}
	}
	return out, nil
}

// runner adapts a memStore into a TxRunner: it just hands the store to fn (no
// real isolation — the tests are written so a failing op happens before any
// mutation, matching the real tx's all-or-nothing).
type runner struct{ store *memStore }

func (r runner) Do(ctx context.Context, fn func(ctx context.Context, repo bounties.TxRepo) error) error {
	return fn(ctx, r.store)
}

// fakeClans is a static membership/leader table.
type fakeClans struct {
	clanOf map[domain.PlayerID]domain.ClanID
	leader map[domain.ClanID]domain.PlayerID
}

func (f fakeClans) ClanOf(_ context.Context, p domain.PlayerID) (domain.ClanID, bool, error) {
	c, ok := f.clanOf[p]
	return c, ok, nil
}

func (f fakeClans) LeaderOf(_ context.Context, c domain.ClanID) (domain.PlayerID, error) {
	return f.leader[c], nil
}

func newService(store *memStore, fc fakeClans, now time.Time) *bounties.Service {
	return bounties.New(store, runner{store: store}, fc, clock.NewFakeClock(now), nil, bounties.Config{})
}

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestUnit_SetBounty_DebitsAndCreates(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[1] = 10_000
	svc := newService(store, fakeClans{}, epoch)

	id, err := svc.SetBounty(context.Background(), 1, domain.PlayerRef(2), 3_000, time.Hour, false)
	require.NoError(t, err)
	require.NotZero(t, id)

	assert.Equal(t, int64(7_000), store.cash[1], "sponsor debited")
	b := store.bounties[id]
	assert.Equal(t, domain.PlayerRef(2), b.Target)
	assert.Equal(t, domain.PlayerRef(1), b.Sponsor)
	assert.Equal(t, int64(3_000), b.Amount)
	assert.Equal(t, domain.BountyActive, b.Status)
	assert.Equal(t, epoch.Add(time.Hour), b.ExpiresAt)
}

func TestUnit_SetBounty_InsufficientFunds(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[1] = 500
	svc := newService(store, fakeClans{}, epoch)

	_, err := svc.SetBounty(context.Background(), 1, domain.PlayerRef(2), 3_000, time.Hour, false)
	require.ErrorIs(t, err, bounties.ErrInsufficientFunds)
	assert.Equal(t, int64(500), store.cash[1], "no debit on failure")
	assert.Empty(t, store.bounties, "no bounty created")
}

func TestUnit_SetBounty_RejectsSelfAndBadInput(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[1] = 10_000
	svc := newService(store, fakeClans{}, epoch)
	ctx := context.Background()

	_, err := svc.SetBounty(ctx, 1, domain.PlayerRef(1), 100, time.Hour, false)
	require.ErrorIs(t, err, bounties.ErrSelfBounty)

	_, err = svc.SetBounty(ctx, 1, domain.PlayerRef(2), 0, time.Hour, false)
	require.ErrorIs(t, err, bounties.ErrInvalidInput)

	_, err = svc.SetBounty(ctx, 1, domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}, 100, time.Hour, false)
	require.ErrorIs(t, err, bounties.ErrInvalidInput)
}

func TestUnit_SetBounty_FromClan(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.treasury[7] = 20_000
	fc := fakeClans{
		clanOf: map[domain.PlayerID]domain.ClanID{1: 7},
		leader: map[domain.ClanID]domain.PlayerID{7: 1},
	}
	svc := newService(store, fc, epoch)

	id, err := svc.SetBounty(context.Background(), 1, domain.PlayerRef(2), 5_000, time.Hour, true)
	require.NoError(t, err)
	assert.Equal(t, int64(15_000), store.treasury[7], "treasury debited")
	assert.Equal(t, domain.ClanRef(7), store.bounties[id].Sponsor)
}

func TestUnit_SetBounty_FromClan_NotLeader(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.treasury[7] = 20_000
	fc := fakeClans{
		clanOf: map[domain.PlayerID]domain.ClanID{2: 7}, // player 2 is a member
		leader: map[domain.ClanID]domain.PlayerID{7: 1}, // but player 1 leads
	}
	svc := newService(store, fc, epoch)

	_, err := svc.SetBounty(context.Background(), 2, domain.PlayerRef(3), 5_000, time.Hour, true)
	require.ErrorIs(t, err, bounties.ErrNotClanLeader)

	// Player in no clan funding from clan → ErrNotInClan.
	_, err = svc.SetBounty(context.Background(), 9, domain.PlayerRef(3), 5_000, time.Hour, true)
	require.ErrorIs(t, err, bounties.ErrNotInClan)
}

func TestUnit_OnKill_PaysAndCloses(t *testing.T) {
	t.Parallel()
	store := newStore()
	// Two sponsors put a bounty on player 2's head.
	store.cash[10] = 0
	store.bounties[1] = domain.Bounty{ID: 1, Target: domain.PlayerRef(2), Sponsor: domain.PlayerRef(5), Amount: 1_000, Status: domain.BountyActive, ExpiresAt: epoch.Add(time.Hour)}
	store.bounties[2] = domain.Bounty{ID: 2, Target: domain.PlayerRef(2), Sponsor: domain.PlayerRef(6), Amount: 2_500, Status: domain.BountyActive, ExpiresAt: epoch.Add(time.Hour)}
	store.nextID = 2
	svc := newService(store, fakeClans{}, epoch)

	// Killer 10 kills victim 2.
	require.NoError(t, svc.OnKill(context.Background(), 10, 2))

	assert.Equal(t, int64(3_500), store.cash[10], "killer collects the sum of both bounties")
	assert.Equal(t, domain.BountyPaid, store.bounties[1].Status)
	assert.Equal(t, domain.BountyPaid, store.bounties[2].Status)
	assert.Equal(t, domain.PlayerID(10), store.bounties[1].PaidTo)
}

func TestUnit_OnKill_OwnKillNotPaid(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.bounties[1] = domain.Bounty{ID: 1, Target: domain.PlayerRef(2), Sponsor: domain.PlayerRef(5), Amount: 1_000, Status: domain.BountyActive, ExpiresAt: epoch.Add(time.Hour)}
	svc := newService(store, fakeClans{}, epoch)

	// Self-kill: killer == victim.
	require.NoError(t, svc.OnKill(context.Background(), 2, 2))
	assert.Equal(t, domain.BountyActive, store.bounties[1].Status, "own kill leaves the bounty active")
	assert.Zero(t, store.cash[2])

	// Unattributed kill (killer 0).
	require.NoError(t, svc.OnKill(context.Background(), 0, 2))
	assert.Equal(t, domain.BountyActive, store.bounties[1].Status)
}

func TestUnit_OnKill_NPCKillerDoesNotClaim(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.bounties[1] = domain.Bounty{ID: 1, Target: domain.PlayerRef(2), Sponsor: domain.PlayerRef(5), Amount: 1_000, Status: domain.BountyActive, ExpiresAt: epoch.Add(time.Hour)}
	// Player 99 is the __npc__ system player (IgnoreKiller).
	svc := bounties.New(store, runner{store: store}, fakeClans{}, clock.NewFakeClock(epoch), nil, bounties.Config{IgnoreKiller: 99})

	require.NoError(t, svc.OnKill(context.Background(), 99, 2))
	assert.Equal(t, domain.BountyActive, store.bounties[1].Status, "NPC kill leaves the bounty open")
	assert.Zero(t, store.cash[99])
}

func TestUnit_OnKill_ClanTargetMatchesVictimClan(t *testing.T) {
	t.Parallel()
	store := newStore()
	// Bounty is on clan 7; victim 2 is a member of clan 7.
	store.bounties[1] = domain.Bounty{ID: 1, Target: domain.ClanRef(7), Sponsor: domain.PlayerRef(5), Amount: 4_000, Status: domain.BountyActive, ExpiresAt: epoch.Add(time.Hour)}
	fc := fakeClans{clanOf: map[domain.PlayerID]domain.ClanID{2: 7}}
	svc := newService(store, fc, epoch)

	require.NoError(t, svc.OnKill(context.Background(), 10, 2))
	assert.Equal(t, int64(4_000), store.cash[10], "clan bounty pays for any member killed")
	assert.Equal(t, domain.BountyPaid, store.bounties[1].Status)
}

func TestUnit_ExpireDue_RefundsSponsor(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[5] = 0
	store.treasury[7] = 0
	// One player-funded and one clan-funded bounty, both past deadline.
	store.bounties[1] = domain.Bounty{ID: 1, Target: domain.PlayerRef(2), Sponsor: domain.PlayerRef(5), Amount: 1_000, Status: domain.BountyActive, ExpiresAt: epoch.Add(-time.Minute)}
	store.bounties[2] = domain.Bounty{ID: 2, Target: domain.PlayerRef(3), Sponsor: domain.ClanRef(7), Amount: 2_000, Status: domain.BountyActive, ExpiresAt: epoch.Add(-time.Minute)}
	// A still-active one must be untouched.
	store.bounties[3] = domain.Bounty{ID: 3, Target: domain.PlayerRef(4), Sponsor: domain.PlayerRef(5), Amount: 9_000, Status: domain.BountyActive, ExpiresAt: epoch.Add(time.Hour)}
	svc := newService(store, fakeClans{}, epoch)

	n, err := svc.ExpireDue(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, int64(1_000), store.cash[5], "player sponsor refunded")
	assert.Equal(t, int64(2_000), store.treasury[7], "clan sponsor refunded")
	assert.Equal(t, domain.BountyExpired, store.bounties[1].Status)
	assert.Equal(t, domain.BountyExpired, store.bounties[2].Status)
	assert.Equal(t, domain.BountyActive, store.bounties[3].Status, "unexpired bounty left alone")
}
