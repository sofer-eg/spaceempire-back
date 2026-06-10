package insurance_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/insurance"
	insurancerepo "spaceempire/back/internal/persistence/insurance"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/pkg/clock"
)

// memStore models the insurance TxRepo + Reader: policies, player cash, and a
// ship-ownership table. AdjustCash rejects going negative with the real
// ErrInsufficientCash sentinel.
type memStore struct {
	policies map[domain.PolicyID]domain.InsurancePolicy
	cash     map[domain.PlayerID]int64
	ships    map[domain.ShipID]shipOwn
	nextID   domain.PolicyID
}

type shipOwn struct {
	owner  domain.PlayerID
	docked *domain.EntityRef
}

func newStore() *memStore {
	return &memStore{
		policies: map[domain.PolicyID]domain.InsurancePolicy{},
		cash:     map[domain.PlayerID]int64{},
		ships:    map[domain.ShipID]shipOwn{},
	}
}

func (m *memStore) activePolicy(shipID domain.ShipID, now time.Time) (domain.PolicyID, bool) {
	for id, p := range m.policies {
		if p.ShipID == shipID && p.Status == domain.PolicyActive && p.ExpiresAt.After(now) {
			return id, true
		}
	}
	return 0, false
}

func (m *memStore) ExpireActiveForShip(_ context.Context, shipID domain.ShipID, now time.Time) error {
	for id, p := range m.policies {
		if p.ShipID == shipID && p.Status == domain.PolicyActive && !p.ExpiresAt.After(now) {
			p.Status = domain.PolicyExpired
			m.policies[id] = p
		}
	}
	return nil
}

func (m *memStore) Create(_ context.Context, p domain.InsurancePolicy) (domain.PolicyID, error) {
	// Enforce the active-per-ship unique index (unexpired only — Create is
	// always called after ExpireActiveForShip in the same tx).
	for _, ex := range m.policies {
		if ex.ShipID == p.ShipID && ex.Status == domain.PolicyActive {
			return 0, insurancerepo.ErrAlreadyInsured
		}
	}
	m.nextID++
	p.ID = m.nextID
	m.policies[p.ID] = p
	return p.ID, nil
}

func (m *memStore) ActiveForShip(_ context.Context, shipID domain.ShipID, now time.Time) (domain.InsurancePolicy, bool, error) {
	if id, ok := m.activePolicy(shipID, now); ok {
		return m.policies[id], true, nil
	}
	return domain.InsurancePolicy{}, false, nil
}

func (m *memStore) Claim(_ context.Context, id domain.PolicyID, claimedAt time.Time) error {
	p := m.policies[id]
	if p.Status != domain.PolicyActive {
		return nil
	}
	p.Status = domain.PolicyClaimed
	p.ClaimedAt = claimedAt
	m.policies[id] = p
	return nil
}

func (m *memStore) AdjustCash(_ context.Context, p domain.PlayerID, delta int64) (int64, error) {
	if m.cash[p]+delta < 0 {
		return 0, playersrepo.ErrInsufficientCash
	}
	m.cash[p] += delta
	return m.cash[p], nil
}

func (m *memStore) ListByPlayer(_ context.Context, player domain.PlayerID) ([]domain.InsurancePolicy, error) {
	var out []domain.InsurancePolicy
	for _, p := range m.policies {
		if p.PlayerID == player {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) ShipOwnership(_ context.Context, shipID domain.ShipID) (domain.PlayerID, *domain.EntityRef, error) {
	s, ok := m.ships[shipID]
	if !ok {
		return 0, nil, insurancerepo.ErrShipNotFound
	}
	return s.owner, s.docked, nil
}

// runner models a transaction: it snapshots the store's mutable maps before
// fn and restores them if fn returns an error, so a debit followed by a failed
// Create rolls back exactly like the real pgx tx.
type runner struct{ store *memStore }

func (r runner) Do(ctx context.Context, fn func(ctx context.Context, repo insurance.TxRepo) error) error {
	policies := make(map[domain.PolicyID]domain.InsurancePolicy, len(r.store.policies))
	for k, v := range r.store.policies {
		policies[k] = v
	}
	cash := make(map[domain.PlayerID]int64, len(r.store.cash))
	for k, v := range r.store.cash {
		cash[k] = v
	}
	nextID := r.store.nextID
	if err := fn(ctx, r.store); err != nil {
		r.store.policies = policies
		r.store.cash = cash
		r.store.nextID = nextID
		return err
	}
	return nil
}

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func dockedAt() *domain.EntityRef {
	return &domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
}

func newService(store *memStore, now time.Time) *insurance.Service {
	return insurance.New(store, runner{store: store}, clock.NewFakeClock(now), nil, insurance.Config{})
}

func TestUnit_Buy_DebitsAndCovers(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 5000
	store.ships[7] = shipOwn{owner: 10, docked: dockedAt()}
	svc := newService(store, epoch)

	id, err := svc.Buy(context.Background(), 10, 7, 1000, 30)
	require.NoError(t, err)
	require.NotZero(t, id)

	assert.Equal(t, int64(4000), store.cash[10], "premium debited")
	p := store.policies[id]
	assert.Equal(t, int64(10_000), p.Coverage, "coverage = premium × 10")
	assert.Equal(t, domain.PolicyActive, p.Status)
	assert.Equal(t, epoch.Add(30*24*time.Hour), p.ExpiresAt)
}

func TestUnit_Buy_RejectsNotOwnerNotDocked(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 5000
	store.ships[7] = shipOwn{owner: 99, docked: dockedAt()} // owned by someone else
	store.ships[8] = shipOwn{owner: 10, docked: nil}        // owned but flying
	svc := newService(store, epoch)
	ctx := context.Background()

	_, err := svc.Buy(ctx, 10, 7, 1000, 30)
	require.ErrorIs(t, err, insurance.ErrNotOwner)

	_, err = svc.Buy(ctx, 10, 8, 1000, 30)
	require.ErrorIs(t, err, insurance.ErrNotDocked)

	assert.Equal(t, int64(5000), store.cash[10], "no debit on rejected buy")
}

func TestUnit_Buy_InsufficientFunds(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 500
	store.ships[7] = shipOwn{owner: 10, docked: dockedAt()}
	svc := newService(store, epoch)

	_, err := svc.Buy(context.Background(), 10, 7, 1000, 30)
	require.ErrorIs(t, err, insurance.ErrInsufficientFunds)
	assert.Empty(t, store.policies)
}

func TestUnit_Buy_AlreadyInsured(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 5000
	store.ships[7] = shipOwn{owner: 10, docked: dockedAt()}
	store.policies[1] = domain.InsurancePolicy{ID: 1, ShipID: 7, PlayerID: 10, Status: domain.PolicyActive, ExpiresAt: epoch.Add(time.Hour)}
	svc := newService(store, epoch)

	_, err := svc.Buy(context.Background(), 10, 7, 1000, 30)
	require.ErrorIs(t, err, insurance.ErrAlreadyInsured)
	assert.Equal(t, int64(5000), store.cash[10], "premium not charged when already insured")
}

func TestUnit_OnKill_PaysHolder(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 0
	store.policies[1] = domain.InsurancePolicy{ID: 1, ShipID: 7, PlayerID: 10, Coverage: 10_000, Status: domain.PolicyActive, ExpiresAt: epoch.Add(time.Hour)}
	svc := newService(store, epoch)

	require.NoError(t, svc.OnKill(context.Background(), 7))
	assert.Equal(t, int64(10_000), store.cash[10], "holder paid coverage")
	assert.Equal(t, domain.PolicyClaimed, store.policies[1].Status)
}

func TestUnit_OnKill_ExpiredNotPaid(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 0
	// Active in the DB but past expiry — must not pay (the task edge case).
	store.policies[1] = domain.InsurancePolicy{ID: 1, ShipID: 7, PlayerID: 10, Coverage: 10_000, Status: domain.PolicyActive, ExpiresAt: epoch.Add(-time.Hour)}
	svc := newService(store, epoch)

	require.NoError(t, svc.OnKill(context.Background(), 7))
	assert.Equal(t, int64(0), store.cash[10], "expired policy does not pay out")
	assert.Equal(t, domain.PolicyActive, store.policies[1].Status, "unchanged")
}

func TestUnit_OnKill_UninsuredNoop(t *testing.T) {
	t.Parallel()
	store := newStore()
	svc := newService(store, epoch)
	require.NoError(t, svc.OnKill(context.Background(), 7))
}
