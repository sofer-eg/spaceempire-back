package rent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/rent"
	playersrepo "spaceempire/back/internal/persistence/players"
	stationsrepo "spaceempire/back/internal/persistence/stations"
	"spaceempire/back/internal/pkg/clock"
)

// memStore models the rent TxRepo + RentStore + Stations: rent rows, player
// cash, the player-owned object list, and a record of confiscations. Cash
// adjust rejects going negative with the real ErrInsufficientCash sentinel so
// the Service's non-payment branch is exercised for real.
type memStore struct {
	rents   map[domain.RentID]domain.Rent
	cash    map[domain.PlayerID]int64
	owned   []stationsrepo.OwnedStatic
	owners  map[domain.EntityRef]domain.PlayerID
	cleared []domain.EntityRef
	nextID  domain.RentID
}

func newStore() *memStore {
	return &memStore{
		rents:  map[domain.RentID]domain.Rent{},
		cash:   map[domain.PlayerID]int64{},
		owners: map[domain.EntityRef]domain.PlayerID{},
	}
}

func (m *memStore) ClaimStation(_ context.Context, station domain.EntityRef, owner domain.PlayerID) (bool, error) {
	if _, taken := m.owners[station]; taken {
		return false, nil
	}
	m.owners[station] = owner
	return true, nil
}

// --- TxRepo ---

func (m *memStore) Due(_ context.Context, now time.Time, limit int) ([]domain.Rent, error) {
	var out []domain.Rent
	for _, r := range m.rents {
		if !r.NextDueAt.After(now) {
			out = append(out, r)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (m *memStore) MarkPaid(_ context.Context, id domain.RentID, paidAt, nextDue time.Time) error {
	r := m.rents[id]
	r.UnpaidPeriods = 0
	r.LastPaidAt = paidAt
	r.NextDueAt = nextDue
	m.rents[id] = r
	return nil
}

func (m *memStore) MarkUnpaid(_ context.Context, id domain.RentID, unpaidPeriods int, nextDue time.Time) error {
	r := m.rents[id]
	r.UnpaidPeriods = unpaidPeriods
	r.NextDueAt = nextDue
	m.rents[id] = r
	return nil
}

func (m *memStore) Delete(_ context.Context, id domain.RentID) error {
	delete(m.rents, id)
	return nil
}

func (m *memStore) AdjustCash(_ context.Context, p domain.PlayerID, delta int64) (int64, error) {
	if m.cash[p]+delta < 0 {
		return 0, playersrepo.ErrInsufficientCash
	}
	m.cash[p] += delta
	return m.cash[p], nil
}

func (m *memStore) ClearOwner(_ context.Context, station domain.EntityRef) error {
	m.cleared = append(m.cleared, station)
	for i := range m.owned {
		if m.owned[i].Ref == station {
			m.owned = append(m.owned[:i], m.owned[i+1:]...)
			break
		}
	}
	return nil
}

// --- RentStore ---

func (m *memStore) Ensure(_ context.Context, payer domain.PlayerID, station domain.EntityRef, amount int64, nextDue time.Time) error {
	for _, r := range m.rents {
		if r.Station == station {
			return nil // ON CONFLICT DO NOTHING
		}
	}
	m.nextID++
	m.rents[m.nextID] = domain.Rent{
		ID:              m.nextID,
		Payer:           payer,
		Station:         station,
		AmountPerPeriod: amount,
		NextDueAt:       nextDue,
	}
	return nil
}

func (m *memStore) ListByPayer(_ context.Context, payer domain.PlayerID) ([]domain.Rent, error) {
	var out []domain.Rent
	for _, r := range m.rents {
		if r.Payer == payer {
			out = append(out, r)
		}
	}
	return out, nil
}

// --- Stations ---

func (m *memStore) PlayerOwned(_ context.Context) ([]stationsrepo.OwnedStatic, error) {
	return append([]stationsrepo.OwnedStatic(nil), m.owned...), nil
}

// runner hands the store to fn as the TxRepo (no isolation — the tests are
// written so the in-tx ops never need rollback).
type runner struct{ store *memStore }

func (r runner) Do(ctx context.Context, fn func(ctx context.Context, repo rent.TxRepo) error) error {
	// Snapshot the mutable maps and restore on error, modelling tx rollback
	// (so a failed Claim leaves no owner/rent/debit behind).
	rents := make(map[domain.RentID]domain.Rent, len(r.store.rents))
	for k, v := range r.store.rents {
		rents[k] = v
	}
	cash := make(map[domain.PlayerID]int64, len(r.store.cash))
	for k, v := range r.store.cash {
		cash[k] = v
	}
	owners := make(map[domain.EntityRef]domain.PlayerID, len(r.store.owners))
	for k, v := range r.store.owners {
		owners[k] = v
	}
	nextID := r.store.nextID
	if err := fn(ctx, r.store); err != nil {
		r.store.rents, r.store.cash, r.store.owners, r.store.nextID = rents, cash, owners, nextID
		return err
	}
	return nil
}

// fakePub records the OverdueEvents the Service publishes.
type fakePub struct{ events []rent.OverdueEvent }

func (p *fakePub) Publish(_ context.Context, _ string, payload []byte) error {
	var ev rent.OverdueEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return err
	}
	p.events = append(p.events, ev)
	return nil
}

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func station(id int64) domain.EntityRef {
	return domain.EntityRef{Kind: domain.EntityKindStation, ID: id}
}

func newService(store *memStore, pub *fakePub, clk clock.Clock, cfg rent.Config) *rent.Service {
	return rent.New(store, store, runner{store: store}, pub, clk, nil, cfg)
}

func claimCfg() rent.Config {
	return rent.Config{Period: 24 * time.Hour, DefaultAmount: 5000, ClaimPrice: 100000}
}

func TestUnit_Claim_TakesStationCreatesRent(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 200000
	svc := newService(store, &fakePub{}, clock.NewFakeClock(epoch), claimCfg())

	require.NoError(t, svc.Claim(context.Background(), 10, 1))
	assert.Equal(t, int64(100000), store.cash[10], "claim price debited")
	assert.Equal(t, domain.PlayerID(10), store.owners[station(1)], "station owned by claimer")
	require.Len(t, store.rents, 1, "rent obligation created")
	for _, r := range store.rents {
		assert.Equal(t, station(1), r.Station)
		assert.Equal(t, int64(5000), r.AmountPerPeriod)
		assert.Equal(t, domain.PlayerID(10), r.Payer)
	}
}

func TestUnit_Claim_AlreadyOwned(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 200000
	store.owners[station(1)] = 99 // already owned
	svc := newService(store, &fakePub{}, clock.NewFakeClock(epoch), claimCfg())

	require.ErrorIs(t, svc.Claim(context.Background(), 10, 1), rent.ErrStationOwned)
	assert.Equal(t, int64(200000), store.cash[10], "not charged for an owned station")
	assert.Empty(t, store.rents)
}

func TestUnit_Claim_InsufficientFunds(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 50000 // < 100000 price
	svc := newService(store, &fakePub{}, clock.NewFakeClock(epoch), claimCfg())

	require.ErrorIs(t, svc.Claim(context.Background(), 10, 1), rent.ErrInsufficientFunds)
	assert.Empty(t, store.owners, "station not claimed (tx rolled back)")
	assert.Empty(t, store.rents)
	assert.Equal(t, int64(50000), store.cash[10])
}

func TestUnit_Reconcile_CreatesRentsIdempotent(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.owned = []stationsrepo.OwnedStatic{
		{Ref: station(1), Owner: 10},
		{Ref: domain.EntityRef{Kind: domain.EntityKindShipyard, ID: 2}, Owner: 11},
	}
	svc := newService(store, &fakePub{}, clock.NewFakeClock(epoch), rent.Config{Period: time.Hour, DefaultAmount: 1000})

	require.NoError(t, svc.Reconcile(context.Background()))
	require.Len(t, store.rents, 2)
	// Idempotent: a second pass does not duplicate.
	require.NoError(t, svc.Reconcile(context.Background()))
	require.Len(t, store.rents, 2)

	for _, r := range store.rents {
		assert.Equal(t, int64(1000), r.AmountPerPeriod)
		assert.Equal(t, epoch.Add(time.Hour), r.NextDueAt)
	}
}

func TestUnit_ProcessDue_ChargesPayer(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 5000
	store.rents[1] = domain.Rent{ID: 1, Payer: 10, Station: station(1), AmountPerPeriod: 1000, NextDueAt: epoch.Add(-time.Minute)}
	pub := &fakePub{}
	clk := clock.NewFakeClock(epoch)
	svc := newService(store, pub, clk, rent.Config{Period: 24 * time.Hour, MaxUnpaid: 3})

	require.NoError(t, svc.ProcessDue(context.Background(), 100))

	assert.Equal(t, int64(4000), store.cash[10], "charged one period")
	assert.Equal(t, 0, store.rents[1].UnpaidPeriods)
	assert.Equal(t, epoch.Add(24*time.Hour), store.rents[1].NextDueAt, "schedule advanced")
	assert.Empty(t, pub.events, "no overdue event on a paid charge")
}

func TestUnit_ProcessDue_NotDueSkipped(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 5000
	store.rents[1] = domain.Rent{ID: 1, Payer: 10, Station: station(1), AmountPerPeriod: 1000, NextDueAt: epoch.Add(time.Hour)}
	svc := newService(store, &fakePub{}, clock.NewFakeClock(epoch), rent.Config{Period: 24 * time.Hour})

	require.NoError(t, svc.ProcessDue(context.Background(), 100))
	assert.Equal(t, int64(5000), store.cash[10], "future-due rent not charged")
}

func TestUnit_ProcessDue_UnpaidIncrements(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 500 // cannot afford 1000
	store.rents[1] = domain.Rent{ID: 1, Payer: 10, Station: station(1), AmountPerPeriod: 1000, NextDueAt: epoch.Add(-time.Minute)}
	store.owned = []stationsrepo.OwnedStatic{{Ref: station(1), Owner: 10}}
	pub := &fakePub{}
	svc := newService(store, pub, clock.NewFakeClock(epoch), rent.Config{Period: 24 * time.Hour, MaxUnpaid: 3})

	require.NoError(t, svc.ProcessDue(context.Background(), 100))

	assert.Equal(t, int64(500), store.cash[10], "no charge — couldn't pay")
	assert.Equal(t, 1, store.rents[1].UnpaidPeriods)
	assert.Empty(t, store.cleared, "not confiscated yet")
	require.Len(t, pub.events, 1)
	assert.False(t, pub.events[0].Confiscated)
	assert.Equal(t, 1, pub.events[0].UnpaidPeriods)
}

func TestUnit_ProcessDue_ConfiscatesAtMaxUnpaid(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 0
	// Already missed 2 of 3 — this miss tips it over.
	store.rents[1] = domain.Rent{ID: 1, Payer: 10, Station: station(1), AmountPerPeriod: 1000, UnpaidPeriods: 2, NextDueAt: epoch.Add(-time.Minute)}
	store.owned = []stationsrepo.OwnedStatic{{Ref: station(1), Owner: 10}}
	pub := &fakePub{}
	svc := newService(store, pub, clock.NewFakeClock(epoch), rent.Config{Period: 24 * time.Hour, MaxUnpaid: 3})

	require.NoError(t, svc.ProcessDue(context.Background(), 100))

	assert.NotContains(t, store.rents, domain.RentID(1), "rent deleted on confiscation")
	require.Len(t, store.cleared, 1)
	assert.Equal(t, station(1), store.cleared[0], "station owner cleared")
	require.Len(t, pub.events, 1)
	assert.True(t, pub.events[0].Confiscated)
}

// TestUnit_ProcessDue_MonthOfBilling drives the FakeClock forward one period
// at a time (the task's "продвинуть время на месяц") through pay → can't-pay →
// confiscation, asserting the full lifecycle.
func TestUnit_ProcessDue_MonthOfBilling(t *testing.T) {
	t.Parallel()
	store := newStore()
	store.cash[10] = 2500 // enough for two full charges of 1000
	store.owned = []stationsrepo.OwnedStatic{{Ref: station(1), Owner: 10}}
	pub := &fakePub{}
	clk := clock.NewFakeClock(epoch)
	cfg := rent.Config{Period: 24 * time.Hour, MaxUnpaid: 3, DefaultAmount: 1000}
	svc := newService(store, pub, clk, cfg)

	require.NoError(t, svc.Reconcile(context.Background())) // next_due = epoch + 24h

	ctx := context.Background()
	for day := 0; day < 6; day++ {
		clk.Advance(24 * time.Hour)
		require.NoError(t, svc.ProcessDue(ctx, 100))
	}

	// Two days paid (2500 → 500), then three misses (days 3-5) → confiscation
	// on the third. Day 6 has no rent left to process.
	assert.Equal(t, int64(500), store.cash[10])
	assert.NotContains(t, store.rents, domain.RentID(1), "confiscated and removed")
	require.Len(t, store.cleared, 1)
	// Three overdue events: two warnings then one confiscation.
	require.Len(t, pub.events, 3)
	assert.False(t, pub.events[0].Confiscated)
	assert.False(t, pub.events[1].Confiscated)
	assert.True(t, pub.events[2].Confiscated)
}
