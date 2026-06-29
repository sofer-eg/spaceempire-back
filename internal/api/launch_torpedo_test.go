package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeTorpedoCargo is an in-memory api.TorpedoCargo. It tracks per-goods-type
// stock so tests can assert that the correct class's ammunition (gt23 / gt24)
// was debited, and records Consume/Refund counts for the refund-on-rejection
// path.
type fakeTorpedoCargo struct {
	mu           sync.Mutex
	stock        map[domain.GoodsTypeID]int64
	consume      int
	refund       int
	lastConsumed domain.GoodsTypeID
}

func newFakeTorpedoCargo(stock map[domain.GoodsTypeID]int64) *fakeTorpedoCargo {
	if stock == nil {
		stock = map[domain.GoodsTypeID]int64{}
	}
	return &fakeTorpedoCargo{stock: stock}
}

func (f *fakeTorpedoCargo) Consume(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consume++
	if f.stock[gtype] < qty {
		return cargo.ErrInsufficientQuantity
	}
	f.stock[gtype] -= qty
	f.lastConsumed = gtype
	return nil
}

func (f *fakeTorpedoCargo) Refund(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refund++
	f.stock[gtype] += qty
	return nil
}

func (f *fakeTorpedoCargo) snapshot(gtype domain.GoodsTypeID) (stock int64, consume, refund int, last domain.GoodsTypeID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stock[gtype], f.consume, f.refund, f.lastConsumed
}

// fakeEquipCatalog is a minimal api.EquipmentCatalog for the energy-cost test:
// it lets NewServer resolve up_torpedo_launcher.energy_usage.
type fakeEquipCatalog struct{ items []balance.Equipment }

func (f fakeEquipCatalog) AllEquipment() []balance.Equipment { return f.items }

func torpedoTestShip() domain.Ship {
	return domain.Ship{
		ID:        1,
		PlayerID:  0, // matches the zero player id of an unauthenticated test request
		SectorID:  domain.SectorID(1),
		Pos:       domain.Vec2{X: 0, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
		HP:        100,
		MaxHP:     100,
		Shield:    50,
		MaxShield: 50,
		Equipment: []domain.InstalledEquipment{{Type: "up_torpedo_launcher", Level: 1}},
	}
}

// newTorpedoTestServer wires the launch-torpedo path: a real Worker, a
// fakeTorpedoCargo, and an api.Server with TorpedoCargo populated. equip is
// optional — pass nil to disable the energy gate (cost 0).
func newTorpedoTestServer(t *testing.T, initial []domain.Ship, fake *fakeTorpedoCargo, equip api.EquipmentCatalog) (*api.Server, *sector.Worker) {
	t.Helper()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		TorpedoCargo:     fake,
		Equipment:        equip,
	}, nil)
	return srv, w
}

func torpedoRequest(t *testing.T, srv *api.Server, body dto.LaunchTorpedoRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-torpedo", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestUnit_LaunchTorpedo_OKClass2: a class-2 launch debits exactly one gt23
// (ЧТЗ AC-2). TorpedoID is zero until the spawn sub-task TASK-100.3.5.4.
func TestUnit_LaunchTorpedo_OKClass2(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	fake := newFakeTorpedoCargo(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 1})
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip(), target}, fake, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.LaunchTorpedoResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)

	stock, consume, refund, last := fake.snapshot(api.TorpedoFirestormGoodsType)
	require.EqualValues(t, 0, stock, "one gt23 consumed")
	require.Equal(t, 1, consume)
	require.Equal(t, 0, refund)
	require.Equal(t, api.TorpedoFirestormGoodsType, last)
}

// TestUnit_LaunchTorpedo_OKClass3: a class-3 launch debits gt24, not gt23.
func TestUnit_LaunchTorpedo_OKClass3(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	fake := newFakeTorpedoCargo(map[domain.GoodsTypeID]int64{api.TorpedoHolyGoodsType: 1})
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip(), target}, fake, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     3,
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	stock, consume, _, last := fake.snapshot(api.TorpedoHolyGoodsType)
	require.EqualValues(t, 0, stock, "one gt24 consumed")
	require.Equal(t, 1, consume)
	require.Equal(t, api.TorpedoHolyGoodsType, last)
}

// TestUnit_LaunchTorpedo_NoCargo: no ammunition of the chosen class → 400 and
// no refund (Consume itself failed), nothing else debited (ЧТЗ AC-2).
func TestUnit_LaunchTorpedo_NoCargo(t *testing.T) {
	t.Parallel()
	fake := newFakeTorpedoCargo(nil) // empty
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip()}, fake, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, _, refund, _ := fake.snapshot(api.TorpedoFirestormGoodsType)
	require.EqualValues(t, 0, stock)
	require.Equal(t, 0, refund, "no refund when Consume itself failed")
}

// TestUnit_LaunchTorpedo_InvalidClass: an unknown class is rejected before
// touching cargo.
func TestUnit_LaunchTorpedo_InvalidClass(t *testing.T) {
	t.Parallel()
	fake := newFakeTorpedoCargo(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 5})
	srv, _ := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip()}, fake, nil)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     5, // no such class
	})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	_, consume, _, _ := fake.snapshot(api.TorpedoFirestormGoodsType)
	require.Equal(t, 0, consume, "request rejected before touching cargo")
}

// TestUnit_LaunchTorpedo_NoLauncher_422: a ship without up_torpedo_launcher is
// rejected by the worker with ErrEquipmentRequired → 422, and the debited
// ammunition is refunded (ЧТЗ AC-1).
func TestUnit_LaunchTorpedo_NoLauncher_422(t *testing.T) {
	t.Parallel()
	ship := torpedoTestShip()
	ship.Equipment = nil // strip the launcher

	fake := newFakeTorpedoCargo(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 1})
	srv, w := newTorpedoTestServer(t, []domain.Ship{ship}, fake, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	stock, consume, refund, _ := fake.snapshot(api.TorpedoFirestormGoodsType)
	require.EqualValues(t, 1, stock, "ammunition restored after worker rejection")
	require.Equal(t, 1, consume)
	require.Equal(t, 1, refund)
}

// TestUnit_LaunchTorpedo_WorkerRejectsRefunds: a self-aimed launch is rejected
// by the worker (ErrInvalidAttackTarget → 400); the handler refunds the
// ammunition it debited (ЧТЗ AC-4 + refund).
func TestUnit_LaunchTorpedo_WorkerRejectsRefunds(t *testing.T) {
	t.Parallel()
	fake := newFakeTorpedoCargo(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 3})
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip()}, fake, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 1}, // self
		Class:     2,
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, consume, refund, _ := fake.snapshot(api.TorpedoFirestormGoodsType)
	require.EqualValues(t, 3, stock, "ammunition restored after worker rejection")
	require.Equal(t, 1, consume)
	require.Equal(t, 1, refund)
}

// TestUnit_LaunchTorpedo_NotEnoughEnergy_422: with a wired up_torpedo_launcher
// energy cost and a drained pool, the worker rejects with ErrNotEnoughEnergy →
// 422, and the ammunition is refunded (ЧТЗ AC-3).
func TestUnit_LaunchTorpedo_NotEnoughEnergy_422(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ship := torpedoTestShip()
	ship.Energy = 0
	ship.MaxEnergy = 1000

	fake := newFakeTorpedoCargo(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 1})
	equip := fakeEquipCatalog{items: []balance.Equipment{{Type: "up_torpedo_launcher", EnergyUsage: 100}}}
	srv, w := newTorpedoTestServer(t, []domain.Ship{ship, target}, fake, equip)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	stock, _, refund, _ := fake.snapshot(api.TorpedoFirestormGoodsType)
	require.EqualValues(t, 1, stock, "ammunition restored when the launch is refused")
	require.Equal(t, 1, refund)
}

// TestUnit_LaunchTorpedo_NoCargoService_503: when TorpedoCargo is nil the
// endpoint returns 503.
func TestUnit_LaunchTorpedo_NoCargoService_503(t *testing.T) {
	t.Parallel()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): {torpedoTestShip()}},
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		// TorpedoCargo intentionally nil
	}, nil)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
