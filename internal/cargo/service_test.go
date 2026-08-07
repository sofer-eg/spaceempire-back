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
	docks         map[domain.ShipID]cargorepo.ShipDock
	failGoodsType bool
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		goodsTypes: make(map[domain.GoodsTypeID]domain.GoodsType),
		capacities: make(map[domain.EntityRef]float64),
		stacks:     make(map[domain.EntityRef]map[stackKey]int64),
		docks:      make(map[domain.ShipID]cargorepo.ShipDock),
	}
}

// seedDock registers a ships row: owned by player, docked to dockedTo
// (nil = in space). Ships absent from the map do not exist.
func (s *stubRepo) seedDock(id domain.ShipID, player domain.PlayerID, dockedTo *domain.EntityRef) {
	s.docks[id] = cargorepo.ShipDock{PlayerID: player, Docked: dockedTo}
}

func (s *stubRepo) ShipDock(_ context.Context, shipID domain.ShipID) (cargorepo.ShipDock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docks[shipID]
	if !ok {
		return cargorepo.ShipDock{}, cargorepo.ErrShipNotFound
	}
	return d, nil
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
	repo.goodsTypes[2] = domain.GoodsType{ID: 2, Name: "Железо", Space: 2}
	repo.seed(owner, 2, 0, 5)

	svc := cargo.New(repo, inlineTx{repo: repo})
	require.NoError(t, svc.Consume(context.Background(), owner, 2, 2))
	assert.EqualValues(t, 3, repo.stacks[owner][stackKey{2, 0}])
}

func TestUnit_CargoService_Consume_InsufficientQuantity(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	repo.goodsTypes[2] = domain.GoodsType{ID: 2, Name: "Железо", Space: 2}
	repo.seed(owner, 2, 0, 1)

	svc := cargo.New(repo, inlineTx{repo: repo})
	err := svc.Consume(context.Background(), owner, 2, 2)
	require.ErrorIs(t, err, cargo.ErrInsufficientQuantity)
	assert.EqualValues(t, 1, repo.stacks[owner][stackKey{2, 0}])
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

// RefundIn is called with a repo already bound to the caller's transaction, so
// these drive it directly — there is no Service wrapper any more (TASK-152).
func TestUnit_CargoRefundIn_RestoresStack(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	repo.goodsTypes[2] = domain.GoodsType{ID: 2, Name: "Железо", Space: 2}
	repo.seed(owner, 2, 0, 3)

	require.NoError(t, cargo.RefundIn(context.Background(), repo, owner, 2, 2))
	assert.EqualValues(t, 5, repo.stacks[owner][stackKey{2, 0}])
}

func TestUnit_CargoRefundIn_RejectsNonPositive(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	err := cargo.RefundIn(context.Background(), repo, domain.EntityRef{Kind: 1, ID: 1}, 50, 0)
	require.ErrorIs(t, err, cargo.ErrNonPositiveQuantity)
}

// TestUnit_CargoRefundIn_IgnoresCapacity pins the deliberate asymmetry with Add:
// the credit must never be refusable, because its caller has already deleted the
// object it is paying for (TASK-152). A caller for whom "it fitted a moment ago"
// does not hold sizes its credit with FitsIn first instead (TASK-156).
func TestUnit_CargoRefundIn_IgnoresCapacity(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 2 // room for one unit of a Space:2 good
	repo.goodsTypes[2] = domain.GoodsType{ID: 2, Name: "Железо", Space: 2}
	repo.seed(owner, 2, 0, 1)

	require.NoError(t, cargo.RefundIn(context.Background(), repo, owner, 2, 3))
	assert.EqualValues(t, 4, repo.stacks[owner][stackKey{2, 0}], "credited past capacity")
}

// FitsIn is how a caller that must not overfill sizes its credit (TASK-156). It
// answers in whole units of the good and never above the caller's own limit.
func TestUnit_CargoFitsIn_SizesTheCreditToFreeSpace(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 100
	// A Space:2 good with a round capacity keeps the arithmetic below readable;
	// FitsIn knows nothing about the catalog, so id 2 «Железо» is as good a
	// stand-in as the drone this was written for (TASK-167 moved the drone to
	// space 290, which would drown the numbers in noise).
	repo.goodsTypes[2] = domain.GoodsType{ID: 2, Name: "Железо", Space: 2}

	// Empty hold: the caller's limit is the answer, not the 50 that would fit.
	fits, err := cargo.FitsIn(context.Background(), repo, owner, 2, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 5, fits, "never more than the caller asked for")

	// 49 units of a Space:2 good leave room for exactly one more.
	repo.seed(owner, 2, 0, 49)
	fits, err = cargo.FitsIn(context.Background(), repo, owner, 2, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 1, fits, "whole units only — 2 free space is one unit")

	// Full hold: zero, and it is not an error.
	repo.seed(owner, 2, 0, 50)
	fits, err = cargo.FitsIn(context.Background(), repo, owner, 2, 5)
	require.NoError(t, err)
	assert.Zero(t, fits)

	// Already over capacity (a pre-TASK-156 refund, or a cargobay downgrade):
	// still zero, never a negative that would index past the caller's slice.
	repo.seed(owner, 2, 0, 80)
	fits, err = cargo.FitsIn(context.Background(), repo, owner, 2, 5)
	require.NoError(t, err)
	assert.Zero(t, fits)
}

// A weightless good is not bounded by capacity, so FitsIn answers the caller's
// limit instead of dividing by a zero space.
func TestUnit_CargoFitsIn_WeightlessGoodIsUnbounded(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.capacities[owner] = 0
	repo.goodsTypes[9] = domain.GoodsType{ID: 9, Name: "Weightless", Space: 0}

	fits, err := cargo.FitsIn(context.Background(), repo, owner, 9, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 3, fits)
}

func TestUnit_CargoFitsIn_UnknownGoodsType(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	_, err := cargo.FitsIn(context.Background(), repo, domain.EntityRef{Kind: 1, ID: 1}, 51, 1)
	require.ErrorIs(t, err, cargo.ErrGoodsTypeNotFound)
}

// AvailableIn is FitsIn read backwards, for the debit side (TASK-176): a caller
// that must launch "as many as the hold has" sizes its own debit with it instead
// of asking ConsumeIn to refuse an amount that was never there.
func TestUnit_CargoAvailableIn_SizesTheDebitToTheStack(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	owner := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	repo.goodsTypes[21] = domain.GoodsType{ID: 21, Name: "Боевой дрон", Space: 290}

	// An empty stack answers 0 and is NOT an error: "nothing to launch" is the
	// caller's decision to make, not this helper's.
	avail, err := cargo.AvailableIn(context.Background(), repo, owner, 21)
	require.NoError(t, err)
	assert.Zero(t, avail)

	repo.seed(owner, 21, 0, 3)
	avail, err = cargo.AvailableIn(context.Background(), repo, owner, 21)
	require.NoError(t, err)
	assert.EqualValues(t, 3, avail)

	// Another player's deposit sitting in the same hold is not this ship's
	// ammunition: ConsumeIn debits the unowned stack, so this must count the same
	// one or the debit it sizes would be refused.
	repo.seed(owner, 21, 5, 4)
	avail, err = cargo.AvailableIn(context.Background(), repo, owner, 21)
	require.NoError(t, err)
	assert.EqualValues(t, 3, avail, "only the unowned stack ConsumeIn debits")
}

// A goods id that is not in the catalog is a misconfigured constant (500), not an
// empty magazine (400) — the exact confusion TASK-167 spent a release on. Checked
// here rather than left to ConsumeIn, because a zero answer short-circuits the
// debit and ConsumeIn is never reached.
func TestUnit_CargoAvailableIn_UnknownGoodsType(t *testing.T) {
	t.Parallel()
	repo := newStubRepo()
	_, err := cargo.AvailableIn(context.Background(), repo, domain.EntityRef{Kind: 1, ID: 1}, 51)
	require.ErrorIs(t, err, cargo.ErrGoodsTypeNotFound)
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

// --- TASK-189: the player-initiated transfer gate -------------------------
//
// MoveByPlayer refuses anything the docked UI could not have produced: a ship
// that is not the caller's, a ship in space, a ship docked somewhere else, and
// a pair of ends that is not exactly one ship and one station.

func TestUnit_CargoService_MoveByPlayer_UnloadToDockedStation(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	repo.capacities[ship] = 1000
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(ship, 1, 0, 50)
	repo.seedDock(2, 7, &station)

	svc := cargo.New(repo, inlineTx{repo: repo})

	require.NoError(t, svc.MoveByPlayer(context.Background(), 7, ship, station, 1, 30))
	assert.EqualValues(t, 20, repo.stacks[ship][stackKey{1, 0}])
	assert.EqualValues(t, 30, repo.stacks[station][stackKey{1, 7}])
}

func TestUnit_CargoService_MoveByPlayer_LoadFromDockedTradeStation(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	station := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 5}
	repo.capacities[ship] = 1000
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(station, 1, 7, 40)
	repo.seedDock(2, 7, &station)

	svc := cargo.New(repo, inlineTx{repo: repo})

	require.NoError(t, svc.MoveByPlayer(context.Background(), 7, station, ship, 1, 25))
	assert.EqualValues(t, 15, repo.stacks[station][stackKey{1, 7}])
	assert.EqualValues(t, 25, repo.stacks[ship][stackKey{1, 0}])
}

// The exploit that opened TASK-189's second front: naming someone else's ship
// id emptied its hold from anywhere on the map.
func TestUnit_CargoService_MoveByPlayer_OtherPlayersShipForbidden(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	repo.capacities[ship] = 1000
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(ship, 1, 0, 50)
	repo.seedDock(2, 9, &station) // ship belongs to player 9, docked correctly

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.MoveByPlayer(context.Background(), 7, ship, station, 1, 30)
	require.ErrorIs(t, err, cargo.ErrShipForbidden)
	assert.EqualValues(t, 50, repo.stacks[ship][stackKey{1, 0}], "no goods left the hold")
	assert.Empty(t, repo.stacks[station])
}

// The exploit named in the task: a ship in open space unloading onto a station
// it only knows the id of.
func TestUnit_CargoService_MoveByPlayer_ShipInSpaceRefused(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 21}
	repo.capacities[ship] = 1000
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(ship, 1, 0, 50)
	repo.seedDock(2, 7, nil) // owned by the caller, but in space

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.MoveByPlayer(context.Background(), 7, ship, station, 1, 5)
	require.ErrorIs(t, err, cargo.ErrNotDocked)
	assert.EqualValues(t, 50, repo.stacks[ship][stackKey{1, 0}])
	assert.Empty(t, repo.stacks[station])
}

func TestUnit_CargoService_MoveByPlayer_DockedElsewhereRefused(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	here := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	there := domain.EntityRef{Kind: domain.EntityKindStation, ID: 21}
	repo.capacities[ship] = 1000
	repo.capacities[there] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(ship, 1, 0, 50)
	repo.seedDock(2, 7, &here)

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.MoveByPlayer(context.Background(), 7, ship, there, 1, 5)
	require.ErrorIs(t, err, cargo.ErrWrongStation)
	assert.EqualValues(t, 50, repo.stacks[ship][stackKey{1, 0}])
	assert.Empty(t, repo.stacks[there])
}

// Same id, different kind: docked to station 1 is not docked to trade station 1.
func TestUnit_CargoService_MoveByPlayer_DockedToSameIdOtherKindRefused(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	tradeStation := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 1}
	repo.capacities[ship] = 1000
	repo.capacities[tradeStation] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(ship, 1, 0, 50)
	repo.seedDock(2, 7, &station)

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.MoveByPlayer(context.Background(), 7, ship, tradeStation, 1, 5)
	require.ErrorIs(t, err, cargo.ErrWrongStation)
	assert.EqualValues(t, 50, repo.stacks[ship][stackKey{1, 0}])
}

func TestUnit_CargoService_MoveByPlayer_UnknownShipNotFound(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 404}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	repo.capacities[ship] = 1000
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.MoveByPlayer(context.Background(), 7, ship, station, 1, 5)
	require.ErrorIs(t, err, cargo.ErrShipNotFound)
}

// Ship↔ship has its own gated path (POST /api/cmd/transport-cargo) and must not
// be reachable here, docked or not.
func TestUnit_CargoService_MoveByPlayer_ShipToShipRefused(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	mine := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	theirs := domain.EntityRef{Kind: domain.EntityKindShip, ID: 3}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	repo.capacities[mine] = 1000
	repo.capacities[theirs] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(theirs, 1, 0, 50)
	repo.seedDock(2, 7, &station)
	repo.seedDock(3, 7, &station)

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.MoveByPlayer(context.Background(), 7, theirs, mine, 1, 10)
	require.ErrorIs(t, err, cargo.ErrInvalidTransfer)
	assert.EqualValues(t, 50, repo.stacks[theirs][stackKey{1, 0}])
	assert.Empty(t, repo.stacks[mine])
}

func TestUnit_CargoService_MoveByPlayer_StationToStationRefused(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	a := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	b := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 5}
	repo.capacities[a] = 1000
	repo.capacities[b] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(a, 1, 7, 50)

	svc := cargo.New(repo, inlineTx{repo: repo})

	err := svc.MoveByPlayer(context.Background(), 7, a, b, 1, 10)
	require.ErrorIs(t, err, cargo.ErrInvalidTransfer)
	assert.EqualValues(t, 50, repo.stacks[a][stackKey{1, 7}])
	assert.Empty(t, repo.stacks[b])
}

// The engine keeps its ungated entry point: the NPC trade hauler moves goods
// between a station and a ship that never docks (app.traderHauler.Haul), and
// gating Move would stop NPC logistics dead.
func TestUnit_CargoService_Move_EngineNeedsNoDock(t *testing.T) {
	t.Parallel()

	repo := newStubRepo()
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 1}
	repo.capacities[ship] = 1000
	repo.capacities[station] = 1000
	repo.goodsTypes[1] = domain.GoodsType{ID: 1, Name: "Batteries", Space: 1}
	repo.seed(station, 1, 0, 50)
	// No ships row seeded at all: Move must not even look one up.

	svc := cargo.New(repo, inlineTx{repo: repo})

	require.NoError(t, svc.Move(context.Background(), 0, station, ship, 1, 30))
	assert.EqualValues(t, 20, repo.stacks[station][stackKey{1, 0}])
	assert.EqualValues(t, 30, repo.stacks[ship][stackKey{1, 0}])
}
