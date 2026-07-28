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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeInstaller is an in-memory sector.StaticInstaller: it stands in for the
// app-side adapter that debits the hold and INSERTs the object in ONE
// transaction (TASK-144). Both halves succeed or neither does, which is exactly
// the invariant the handler now relies on.
//
// Note there is no api-side cargo dependency any more: api.Config carries no
// JammerCargo/SatelliteCargo, so the HTTP layer cannot touch goods even in
// principle. Every debit these tests observe was made inside the worker.
type fakeInstaller struct {
	mu         sync.Mutex
	stock      int64
	debits     int
	jammers    int
	satellites int
	goodsTypes []domain.GoodsTypeID
}

func (f *fakeInstaller) consume(gtype domain.GoodsTypeID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goodsTypes = append(f.goodsTypes, gtype)
	if f.stock < 1 {
		return cargo.ErrInsufficientQuantity
	}
	f.stock--
	f.debits++
	return nil
}

func (f *fakeInstaller) InstallJammer(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, _ domain.Jammer) (domain.JammerID, error) {
	if err := f.consume(gtype); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jammers++
	return domain.JammerID(f.jammers), nil
}

func (f *fakeInstaller) InstallSatellite(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, _ domain.Satellite) (domain.SatelliteID, error) {
	if err := f.consume(gtype); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.satellites++
	return domain.SatelliteID(f.satellites), nil
}

func (f *fakeInstaller) snapshot() (stock int64, debits, jammers, satellites int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stock, f.debits, f.jammers, f.satellites
}

func (f *fakeInstaller) installedGoods() []domain.GoodsTypeID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.GoodsTypeID(nil), f.goodsTypes...)
}

// deferredRouter accepts commands into a holding pen instead of handing them
// straight to the worker, reproducing the TASK-144 race: the handler's ack
// deadline expires (504) while the command is still queued, and the worker
// applies it afterwards. release() plays the queue into the worker.
type deferredRouter struct {
	workerRouter
	mu   sync.Mutex
	held []deferredCmd
}

type deferredCmd struct {
	sectorID domain.SectorID
	cmd      sector.Command
}

func (r *deferredRouter) Send(sectorID domain.SectorID, cmd sector.Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held = append(r.held, deferredCmd{sectorID: sectorID, cmd: cmd})
	return nil
}

// release forwards every held command to the worker and ticks once, applying
// them exactly as a delayed tick would.
func (r *deferredRouter) release(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	held := r.held
	r.held = nil
	r.mu.Unlock()
	for _, h := range held {
		require.NoError(t, r.w.Send(h.sectorID, h.cmd))
	}
	r.w.Tick(context.Background())
}

// installTestWorker builds a worker whose install path runs through inst.
func installTestWorker(inst *fakeInstaller, initial []domain.Ship) *sector.Worker {
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
		sector.WithStaticInstaller(inst),
	)
}

// newJammerTestServer wires the install-jammer path: a real Worker whose
// install runs through a fakeInstaller, and an api.Server with no cargo
// dependency at all.
func newJammerTestServer(t *testing.T, initial []domain.Ship, stock int64) (*api.Server, *sector.Worker, *fakeInstaller) {
	t.Helper()
	inst := &fakeInstaller{stock: stock}
	w := installTestWorker(inst, initial)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	return srv, w, inst
}

func postInstallJammer(t *testing.T, srv *api.Server, shipID int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.InstallJammerRequest{ShipID: shipID})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/install-jammer", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestUnit_InstallJammer_OK: the happy path debits one generator from the hold
// (inside the worker, in the install transaction) and returns the new id.
func TestUnit_InstallJammer_OK(t *testing.T) {
	t.Parallel()
	srv, w, inst := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 3)
	runWorker(t, w)

	rec := postInstallJammer(t, srv, 1)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.InstallJammerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.NotZero(t, resp.JammerID)

	stock, debits, jammers, _ := inst.snapshot()
	require.EqualValues(t, 2, stock, "cargo decremented by 1")
	require.Equal(t, 1, debits)
	require.Equal(t, 1, jammers)

	// Assert the literal id, not api.JammerGoodsType — comparing the handler's
	// constant against itself would pass even if it pointed at the satellite's
	// goods 26. 27 is «Генератор гипер-помех» in configs/balance.yaml.
	require.Equal(t, []domain.GoodsTypeID{27}, inst.installedGoods(),
		"one Генератор гипер-помех (goods 27) debited")
}

// TestUnit_InstallJammer_NoCargo: an empty hold is refused inside the install
// transaction and surfaces as 400 — nothing is created.
func TestUnit_InstallJammer_NoCargo(t *testing.T) {
	t.Parallel()
	srv, w, inst := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 0)
	runWorker(t, w)

	rec := postInstallJammer(t, srv, 1)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, debits, jammers, _ := inst.snapshot()
	require.EqualValues(t, 0, stock)
	require.Equal(t, 0, debits)
	require.Equal(t, 0, jammers, "no generator for an empty hold")
}

// TestUnit_InstallJammer_SectorRejectsKeepsCargo: the worker rejects the install
// on a gate (no such ship) — the goods are never touched, so there is nothing to
// refund. This replaces the pre-TASK-144 refund test: the handler no longer
// debits up front, so a rejection cannot leave the hold short.
func TestUnit_InstallJammer_SectorRejectsKeepsCargo(t *testing.T) {
	t.Parallel()
	srv, w, inst := newJammerTestServer(t, nil, 3) // no ship 1 in the sector
	runWorker(t, w)

	rec := postInstallJammer(t, srv, 1)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	stock, debits, jammers, _ := inst.snapshot()
	require.EqualValues(t, 3, stock, "hold untouched by a rejected install")
	require.Equal(t, 0, debits)
	require.Equal(t, 0, jammers)
	require.Empty(t, inst.installedGoods(), "installer never reached")
}

// TestUnit_InstallJammer_AckTimeoutChargesOnce is the TASK-144 regression test.
// The command is accepted but the ack does not arrive in time: the handler
// answers 504 and — crucially — performs NO cargo call of its own (it has no
// cargo dependency left). When the worker finally applies the queued command,
// the debit and the INSERT land together: one generator, one payment. The
// player's retry then hits an empty hold and gets nothing for free.
func TestUnit_InstallJammer_AckTimeoutChargesOnce(t *testing.T) {
	t.Parallel()
	inst := &fakeInstaller{stock: 1}
	w := installTestWorker(inst, []domain.Ship{missileTestShip()})
	router := &deferredRouter{workerRouter: workerRouter{w}}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       20 * time.Millisecond,
		SectorID:         1,
	}, nil)

	rec := postInstallJammer(t, srv, 1)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())

	stock, debits, jammers, _ := inst.snapshot()
	require.EqualValues(t, 1, stock, "504 alone must not move goods")
	require.Equal(t, 0, debits, "the HTTP layer makes no cargo call at all")
	require.Equal(t, 0, jammers)

	// The worker applies the command it was already holding.
	router.release(t)
	stock, debits, jammers, _ = inst.snapshot()
	require.EqualValues(t, 0, stock)
	require.Equal(t, 1, debits, "charged exactly once")
	require.Equal(t, 1, jammers, "exactly one generator deployed")

	// Player retries after the 504: the hold is empty, so no free duplicate.
	rec = postInstallJammer(t, srv, 1)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	router.release(t)

	_, debits, jammers, _ = inst.snapshot()
	assert.Equal(t, 1, debits, "no second charge")
	assert.Equal(t, 1, jammers, "no free second generator")
	assert.Len(t, w.Snapshot(domain.SectorID(1)).Statics.Jammers, 1)
}
