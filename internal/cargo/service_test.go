package cargo_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	cargorepo "spaceempire/back/internal/persistence/cargo"
)

// stackKey identifies one cargo row in the stub: a goods type deposited by a
// specific player (goodsOwner 0 = unowned). Mirrors the four-column UNIQUE of
// the real cargo table minus the physical owner, which is the outer map key.
type stackKey struct {
	gtype      domain.GoodsTypeID
	goodsOwner domain.PlayerID
}

// stubRepo is an in-memory cargo.Repo implementation. It is also its own
// TxRunner — Do just invokes fn with the stub itself, which mirrors the
// "every op atomic" behavior of a real single-statement transaction
// closely enough for service-level assertions.
type stubRepo struct {
	mu            sync.Mutex
	goodsTypes    map[domain.GoodsTypeID]domain.GoodsType
	capacities    map[domain.EntityRef]float64
	stacks        map[domain.EntityRef]map[stackKey]int64
	failGoodsType bool
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		goodsTypes: make(map[domain.GoodsTypeID]domain.GoodsType),
		capacities: make(map[domain.EntityRef]float64),
		stacks:     make(map[domain.EntityRef]map[stackKey]int64),
	}
}

// seed places qty units of gtype deposited by goodsOwner into owner's hold.
func (s *stubRepo) seed(owner domain.EntityRef, gtype domain.GoodsTypeID, goodsOwner domain.PlayerID, qty int64) {
	if s.stacks[owner] == nil {
		s.stacks[owner] = make(map[stackKey]int64)
	}
	s.stacks[owner][stackKey{gtype, goodsOwner}] = qty
}

func (s *stubRepo) GoodsType(_ context.Context, id domain.GoodsTypeID) (domain.GoodsType, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failGoodsType {
		return domain.GoodsType{}, errors.New("boom")
	}
	gt, ok := s.goodsTypes[id]
	if !ok {
		return domain.GoodsType{}, cargorepo.ErrGoodsTypeNotFound
	}
	return gt, nil
}

func (s *stubRepo) Capacity(_ context.Context, owner domain.EntityRef) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.capacities[owner]
	if !ok {
		return 0, cargorepo.ErrOwnerNotFound
	}
	return c, nil
}

func (s *stubRepo) UsedSpace(_ context.Context, owner domain.EntityRef) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Physical usage: every stack counts, regardless of depositor.
	var used float64
	for key, qty := range s.stacks[owner] {
		used += float64(qty) * s.goodsTypes[key.gtype].Space
	}
	return used, nil
}

func (s *stubRepo) ListByOwner(_ context.Context, owner domain.EntityRef, viewer domain.PlayerID) ([]domain.CargoItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Sum unowned (0) + the viewer's own stacks into one item per goods type.
	merged := make(map[domain.GoodsTypeID]int64)
	for key, qty := range s.stacks[owner] {
		if key.goodsOwner == 0 || key.goodsOwner == viewer {
			merged[key.gtype] += qty
		}
	}
	out := make([]domain.CargoItem, 0, len(merged))
	for gid, qty := range merged {
		out = append(out, domain.CargoItem{GoodsType: gid, Quantity: qty})
	}
	return out, nil
}

func (s *stubRepo) Add(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64, goodsOwner domain.PlayerID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stacks[owner] == nil {
		s.stacks[owner] = make(map[stackKey]int64)
	}
	s.stacks[owner][stackKey{gtype, goodsOwner}] += qty
	return nil
}

func (s *stubRepo) Subtract(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64, goodsOwner domain.PlayerID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stackKey{gtype, goodsOwner}
	have := s.stacks[owner][key]
	if have < qty {
		return cargorepo.ErrInsufficientQuantity
	}
	have -= qty
	if have == 0 {
		delete(s.stacks[owner], key)
	} else {
		s.stacks[owner][key] = have
	}
	return nil
}

func (s *stubRepo) Quantity(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, goodsOwner domain.PlayerID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stacks[owner][stackKey{gtype, goodsOwner}], nil
}

func (s *stubRepo) HasOthersGoods(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, viewer domain.PlayerID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, qty := range s.stacks[owner] {
		if key.gtype == gtype && key.goodsOwner != 0 && key.goodsOwner != viewer && qty > 0 {
			return true, nil
		}
	}
	return false, nil
}

// inlineTx implements cargo.TxRunner by invoking fn with the underlying
// repo directly — no real transaction, but the assertions only care that
// fn is called and that its error propagates.
type inlineTx struct{ repo cargo.Repo }

func (t inlineTx) Do(ctx context.Context, fn func(context.Context, cargo.Repo) error) error {
	return fn(ctx, t.repo)
}

func TestUnit_CargoService_Inventory_ReturnsCapacityUsedItems(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(owner, 1, 0, 5)

	svc := cargo.New(repo, inlineTx{repo: repo})
	inv, err := svc.Inventory(context.Background(), owner, 0)
	require.NoError(t, err)
	assert.Equal(t, owner, inv.Owner)
	assert.InDelta(t, 100.0, inv.Capacity, 1e-9)
	assert.InDelta(t, 5.0, inv.Used, 1e-9)
	require.Len(t, inv.Items, 1)
	assert.Equal(t, domain.GoodsTypeID(1), inv.Items[0].GoodsType)
	assert.EqualValues(t, 5, inv.Items[0].Quantity)
}

func TestUnit_CargoService_Inventory_OwnerNotFound(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	svc := cargo.New(repo, inlineTx{repo: repo})

	_, err := svc.Inventory(context.Background(), domain.EntityRef{Kind: domain.EntityKindShip, ID: 42}, 0)
	require.ErrorIs(t, err, cargo.ErrOwnerNotFound)
}

func TestUnit_CargoService_Move_HappyPath(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	from := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	to := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[from] = 1000
	repo.capacities[to] = 100
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(from, 1, 0, 50)

	svc := cargo.New(repo, inlineTx{repo: repo})

	// actor 0 (NPC) withdraws from the station's unowned pool.
	require.NoError(t, svc.Move(context.Background(), 0, from, to, 1, 30))
	assert.EqualValues(t, 20, repo.stacks[from][stackKey{1, 0}])
	assert.EqualValues(t, 30, repo.stacks[to][stackKey{1, 0}])
}

func TestUnit_CargoService_Move_NoSpace(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	from := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	to := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[from] = 1000
	repo.capacities[to] = 10
	repo.goodsTypes[3] = domain.GoodsType{ID: 3, Name: "Silicon Wafers", Space: 5}
	repo.seed(from, 3, 0, 50)

	svc := cargo.New(repo, inlineTx{repo: repo})

	// 3 units * 5 space = 15 > capacity 10 → ErrNoSpace, no mutation.
	err := svc.Move(context.Background(), 0, from, to, 3, 3)
	require.ErrorIs(t, err, cargo.ErrNoSpace)
	assert.EqualValues(t, 50, repo.stacks[from][stackKey{3, 0}])
	assert.Empty(t, repo.stacks[to])
}

func TestUnit_CargoService_Move_InsufficientSource(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	from := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	to := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[from] = 1000
	repo.capacities[to] = 100
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(from, 1, 0, 5)

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.Move(context.Background(), 0, from, to, 1, 10)
	require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
}

func TestUnit_CargoService_Move_RejectsNonPositive(t *testing.T) {
	t.Parallel()
	svc := cargo.New(newStubRepo(), inlineTx{repo: newStubRepo()})
	err := svc.Move(context.Background(), 0, domain.EntityRef{Kind: 1, ID: 1}, domain.EntityRef{Kind: 1, ID: 2}, 1, 0)
	require.ErrorIs(t, err, cargo.ErrNonPositiveQuantity)
}

func TestUnit_CargoService_Move_RejectsSameOwner(t *testing.T) {
	t.Parallel()
	svc := cargo.New(newStubRepo(), inlineTx{repo: newStubRepo()})
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 1}
	err := svc.Move(context.Background(), 0, owner, owner, 1, 5)
	require.ErrorIs(t, err, cargo.ErrSameOwner)
}

// --- phase 10.22: per-player station holds -------------------------------

func TestUnit_CargoService_Move_DepositToStationTagsActor(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	repo.capacities[ship] = 1000
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(ship, 1, 0, 50) // ship goods are unowned

	svc := cargo.New(repo, inlineTx{repo: repo})

	// Player 7 unloads onto the station — the new stack is tagged with 7.
	require.NoError(t, svc.Move(context.Background(), 7, ship, station, 1, 30))
	assert.EqualValues(t, 30, repo.stacks[station][stackKey{1, 7}])
	assert.EqualValues(t, 0, repo.stacks[station][stackKey{1, 0}])
}

func TestUnit_CargoService_Inventory_StationHidesOtherPlayersGoods(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.goodsTypes[2] = domain.GoodsType{ID: 2, Name: "Iron", Space: 2}
	repo.seed(station, 1, 7, 10)  // player 7's deposit
	repo.seed(station, 1, 9, 5)   // player 9's deposit, same type
	repo.seed(station, 2, 0, 100) // unowned pool

	svc := cargo.New(repo, inlineTx{repo: repo})

	inv, err := svc.Inventory(context.Background(), station, 7)
	require.NoError(t, err)
	// Player 7 sees their 10 Batteries + the 100 unowned Iron — never 9's 5.
	got := map[domain.GoodsTypeID]int64{}
	for _, it := range inv.Items {
		got[it.GoodsType] = it.Quantity
	}
	assert.EqualValues(t, 10, got[1])
	assert.EqualValues(t, 100, got[2])
}

func TestUnit_CargoService_Move_WithdrawOwnFromStation(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[station] = 1000
	repo.capacities[ship] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(station, 1, 7, 40)

	svc := cargo.New(repo, inlineTx{repo: repo})

	require.NoError(t, svc.Move(context.Background(), 7, station, ship, 1, 25))
	assert.EqualValues(t, 15, repo.stacks[station][stackKey{1, 7}])
	assert.EqualValues(t, 25, repo.stacks[ship][stackKey{1, 0}])
}

func TestUnit_CargoService_Move_WithdrawOthersGoodsForbidden(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[station] = 1000
	repo.capacities[ship] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(station, 1, 9, 40) // belongs to player 9

	svc := cargo.New(repo, inlineTx{repo: repo})

	// Player 7 has nothing of their own and no unowned pool → forbidden, no mutation.
	err := svc.Move(context.Background(), 7, station, ship, 1, 10)
	require.ErrorIs(t, err, cargo.ErrForbidden)
	assert.EqualValues(t, 40, repo.stacks[station][stackKey{1, 9}])
	assert.Empty(t, repo.stacks[ship])
}

func TestUnit_CargoService_Move_WithdrawUnownedFromStation(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[station] = 1000
	repo.capacities[ship] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(station, 1, 0, 40) // unowned pool (NPC/loot)

	svc := cargo.New(repo, inlineTx{repo: repo})

	// Any player may take unowned goods.
	require.NoError(t, svc.Move(context.Background(), 7, station, ship, 1, 10))
	assert.EqualValues(t, 30, repo.stacks[station][stackKey{1, 0}])
	assert.EqualValues(t, 10, repo.stacks[ship][stackKey{1, 0}])
}

func TestUnit_CargoService_Move_WithdrawDrawsOwnThenUnowned(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[station] = 1000
	repo.capacities[ship] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(station, 1, 7, 8)  // player's own 8
	repo.seed(station, 1, 0, 20) // unowned 20

	svc := cargo.New(repo, inlineTx{repo: repo})

	// Take 15: own 8 fully drained first, then 7 from the unowned pool.
	require.NoError(t, svc.Move(context.Background(), 7, station, ship, 1, 15))
	_, ownStill := repo.stacks[station][stackKey{1, 7}]
	assert.False(t, ownStill, "own stack fully drained and removed")
	assert.EqualValues(t, 13, repo.stacks[station][stackKey{1, 0}])
	assert.EqualValues(t, 15, repo.stacks[ship][stackKey{1, 0}])
}

func TestUnit_CargoService_Consume_HappyPath(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	repo.goodsTypes[50] = domain.GoodsType{ID: 50, Name: "Missile", Space: 2}
	repo.seed(owner, 50, 0, 5)

	svc := cargo.New(repo, inlineTx{repo: repo})
	require.NoError(t, svc.Consume(context.Background(), owner, 50, 2))
	assert.EqualValues(t, 3, repo.stacks[owner][stackKey{50, 0}])
}

func TestUnit_CargoService_Consume_InsufficientQuantity(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	repo.goodsTypes[50] = domain.GoodsType{ID: 50, Name: "Missile", Space: 2}
	repo.seed(owner, 50, 0, 1)

	svc := cargo.New(repo, inlineTx{repo: repo})
	err := svc.Consume(context.Background(), owner, 50, 2)
	require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
	assert.EqualValues(t, 1, repo.stacks[owner][stackKey{50, 0}])
}

func TestUnit_CargoService_Consume_RejectsNonPositive(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	svc := cargo.New(repo, inlineTx{repo: repo})
	err := svc.Consume(context.Background(), domain.EntityRef{Kind: 1, ID: 1}, 50, 0)
	require.ErrorIs(t, err, cargo.ErrNonPositiveQuantity)
}

func TestUnit_CargoService_Consume_UnknownGoodsType(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	svc := cargo.New(repo, inlineTx{repo: repo})
	err := svc.Consume(context.Background(), owner, 999, 1)
	require.ErrorIs(t, err, cargo.ErrGoodsTypeNotFound)
}

func TestUnit_CargoService_Refund_RestoresStack(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	repo.goodsTypes[50] = domain.GoodsType{ID: 50, Name: "Missile", Space: 2}
	repo.seed(owner, 50, 0, 3)

	svc := cargo.New(repo, inlineTx{repo: repo})
	require.NoError(t, svc.Refund(context.Background(), owner, 50, 2))
	assert.EqualValues(t, 5, repo.stacks[owner][stackKey{50, 0}])
}

func TestUnit_CargoService_Refund_RejectsNonPositive(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	svc := cargo.New(repo, inlineTx{repo: repo})
	err := svc.Refund(context.Background(), domain.EntityRef{Kind: 1, ID: 1}, 50, 0)
	require.ErrorIs(t, err, cargo.ErrNonPositiveQuantity)
}

func TestUnit_CargoService_Move_UnknownGoodsType(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	from := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	to := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	repo.capacities[from] = 100
	repo.capacities[to] = 100
	// goods_types intentionally empty.

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.Move(context.Background(), 0, from, to, 999, 1)
	require.ErrorIs(t, err, cargo.ErrGoodsTypeNotFound)
}
