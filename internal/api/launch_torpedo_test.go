package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

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

// newTorpedoTestServer wires the launch-torpedo path: a real Worker whose launch
// runs through a fakeOrdnance, and an api.Server with no cargo dependency at all.
// equip is optional — pass nil to disable the energy gate (cost 0).
func newTorpedoTestServer(t *testing.T, initial []domain.Ship, ord *fakeOrdnance, equip api.EquipmentCatalog) (*api.Server, *sector.Worker) {
	t.Helper()
	w := ordnanceTestWorker(ord, initial)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
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

// TestUnit_LaunchTorpedo_OKClass2: a class-2 launch charges exactly one gt23
// (ЧТЗ AC-2), inside the worker's launch transaction.
func TestUnit_LaunchTorpedo_OKClass2(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 1})
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip(), target}, ord, nil)
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
	require.NotZero(t, resp.TorpedoID)

	st := ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.TorpedoFirestormGoodsType), "one gt23 charged")
	require.Equal(t, 1, st.debits)
	require.Equal(t, 1, st.torpedos)
	require.Equal(t, 0, st.refunds)
	// Literal 23, not the api constant: comparing it against itself would pass
	// even if class 2 pointed at gt24.
	require.Equal(t, []domain.GoodsTypeID{23}, ord.chargedGoods())
}

// TestUnit_LaunchTorpedo_OKClass3: a class-3 launch charges gt24, not gt23.
func TestUnit_LaunchTorpedo_OKClass3(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.TorpedoHolyGoodsType: 1})
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip(), target}, ord, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     3,
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.EqualValues(t, 0, ord.left(api.TorpedoHolyGoodsType), "one gt24 charged")
	require.Equal(t, 1, ord.snapshot().debits)
	require.Equal(t, []domain.GoodsTypeID{24}, ord.chargedGoods())
}

// TestUnit_LaunchTorpedo_NoCargo: no ammunition of the chosen class → the launch
// transaction refuses it (400) and nothing is spent (ЧТЗ AC-2).
func TestUnit_LaunchTorpedo_NoCargo(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ord := newFakeOrdnance(nil) // empty
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip(), target}, ord, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	st := ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.TorpedoFirestormGoodsType))
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.torpedos)
	require.Equal(t, 0, st.refunds, "no refund — the failed debit rolled back")
}

// TestUnit_LaunchTorpedo_InvalidClass: an unknown class is rejected at the
// handler boundary, before the command is built.
func TestUnit_LaunchTorpedo_InvalidClass(t *testing.T) {
	t.Parallel()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 5})
	srv, _ := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip()}, ord, nil)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     5, // no such class
	})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, ord.chargedGoods(), "request rejected before the ordnance is reached")
}

// TestUnit_LaunchTorpedo_NoLauncher_422: a ship without up_torpedo_launcher is
// rejected by the worker's capability gate (422) — which runs before the debit, so
// the ammunition is untouched rather than debited-then-refunded (ЧТЗ AC-1).
func TestUnit_LaunchTorpedo_NoLauncher_422(t *testing.T) {
	t.Parallel()
	ship := torpedoTestShip()
	ship.Equipment = nil // strip the launcher

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 1})
	srv, w := newTorpedoTestServer(t, []domain.Ship{ship}, ord, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	st := ord.snapshot()
	require.EqualValues(t, 1, ord.left(api.TorpedoFirestormGoodsType), "ammunition untouched")
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.refunds)
}

// TestUnit_LaunchTorpedo_WorkerRejectsKeepsCargo: a self-aimed launch is rejected
// by the worker (ErrInvalidAttackTarget → 400) before the debit, so there is
// nothing to refund.
func TestUnit_LaunchTorpedo_WorkerRejectsKeepsCargo(t *testing.T) {
	t.Parallel()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 3})
	srv, w := newTorpedoTestServer(t, []domain.Ship{torpedoTestShip()}, ord, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 1}, // self
		Class:     2,
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	st := ord.snapshot()
	require.EqualValues(t, 3, ord.left(api.TorpedoFirestormGoodsType), "ammunition untouched")
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.refunds)
}

// TestUnit_LaunchTorpedo_NotEnoughEnergy_422: with a wired up_torpedo_launcher
// energy cost and a drained pool the worker rejects with ErrNotEnoughEnergy → 422,
// and the ammunition is never charged (ЧТЗ AC-3: a refused launch spends nothing).
func TestUnit_LaunchTorpedo_NotEnoughEnergy_422(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ship := torpedoTestShip()
	ship.Energy = 0
	ship.MaxEnergy = 1000

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 1})
	equip := fakeEquipCatalog{items: []balance.Equipment{{Type: "up_torpedo_launcher", EnergyUsage: 100}}}
	srv, w := newTorpedoTestServer(t, []domain.Ship{ship, target}, ord, equip)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.EqualValues(t, 1, ord.left(api.TorpedoFirestormGoodsType), "ammunition untouched")
	require.Equal(t, 0, ord.snapshot().debits)
}

// TestUnit_LaunchTorpedo_NoOrdnanceWired: with no transactional ordnance the
// worker refuses the launch instead of firing a free torpedo, and the handler
// surfaces 503.
func TestUnit_LaunchTorpedo_NoOrdnanceWired(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	w := noOrdnanceWorker(t, []domain.Ship{torpedoTestShip(), target})
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	runWorker(t, w)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

// TestUnit_LaunchTorpedo_AckTimeoutChargesOnce is the TASK-147 regression test
// for launch-torpedo: the 504 makes no cargo call, the deferred command charges
// once and fires once, and the retry on an empty magazine gets nothing free.
func TestUnit_LaunchTorpedo_AckTimeoutChargesOnce(t *testing.T) {
	t.Parallel()
	target := torpedoTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.TorpedoFirestormGoodsType: 1})
	w := ordnanceTestWorker(ord, []domain.Ship{torpedoTestShip(), target})
	router := &deferredRouter{workerRouter: workerRouter{w}}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       20 * time.Millisecond,
		SectorID:         1,
	}, nil)

	rec := torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())

	st := ord.snapshot()
	require.EqualValues(t, 1, ord.left(api.TorpedoFirestormGoodsType), "504 alone must not move ammunition")
	require.Equal(t, 0, st.debits, "the HTTP layer makes no cargo call at all")
	require.Equal(t, 0, st.refunds)
	require.Equal(t, 0, st.torpedos)

	router.release(t)
	st = ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.TorpedoFirestormGoodsType))
	require.Equal(t, 1, st.debits, "charged exactly once")
	require.Equal(t, 1, st.torpedos, "exactly one torpedo launched")

	rec = torpedoRequest(t, srv, dto.LaunchTorpedoRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Class:     2,
	})
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	router.release(t)

	st = ord.snapshot()
	assert.Equal(t, 1, st.debits, "no second charge")
	assert.Equal(t, 1, st.torpedos, "no free second torpedo")
}
