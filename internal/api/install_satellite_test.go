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
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// newSatelliteTestServer wires the install-satellite path: a real Worker whose
// install runs through a fakeInstaller (see install_jammer_test.go), and an
// api.Server with no cargo dependency at all.
func newSatelliteTestServer(t *testing.T, initial []domain.Ship, stock int64) (*api.Server, *fakeInstaller) {
	t.Helper()
	inst := &fakeInstaller{stock: stock}
	w := installTestWorker(inst, initial)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	runWorker(t, w)
	return srv, inst
}

func postInstallSatellite(t *testing.T, srv *api.Server, shipID int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.InstallSatelliteRequest{ShipID: shipID})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/install-satellite", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestUnit_InstallSatellite_OK: the happy path debits one satellite from the
// hold (inside the worker, in the install transaction) and returns the new id.
func TestUnit_InstallSatellite_OK(t *testing.T) {
	t.Parallel()
	srv, inst := newSatelliteTestServer(t, []domain.Ship{missileTestShip()}, 3)

	rec := postInstallSatellite(t, srv, 1)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.InstallSatelliteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.NotZero(t, resp.SatelliteID)

	stock, debits, _, satellites := inst.snapshot()
	require.EqualValues(t, 2, stock, "cargo decremented by 1")
	require.Equal(t, 1, debits)
	require.Equal(t, 1, satellites)

	// Literal id, not api.SatelliteGoodsType: 26 is «Навигационный спутник» in
	// configs/balance.yaml — a constant pointing at the jammer's 27 must fail.
	require.Equal(t, []domain.GoodsTypeID{26}, inst.installedGoods())
}

// TestUnit_InstallSatellite_NoCargo: an empty hold is refused inside the install
// transaction and surfaces as 400 — nothing is created.
func TestUnit_InstallSatellite_NoCargo(t *testing.T) {
	t.Parallel()
	srv, inst := newSatelliteTestServer(t, []domain.Ship{missileTestShip()}, 0)

	rec := postInstallSatellite(t, srv, 1)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, debits, _, satellites := inst.snapshot()
	require.EqualValues(t, 0, stock)
	require.Equal(t, 0, debits)
	require.Equal(t, 0, satellites)
}

// TestUnit_InstallSatellite_SectorRejectsKeepsCargo: a gate rejection (no such
// ship) never reaches the installer, so the hold cannot be left short.
func TestUnit_InstallSatellite_SectorRejectsKeepsCargo(t *testing.T) {
	t.Parallel()
	srv, inst := newSatelliteTestServer(t, nil, 3) // no ship 1 in the sector

	rec := postInstallSatellite(t, srv, 1)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	stock, debits, _, satellites := inst.snapshot()
	require.EqualValues(t, 3, stock, "hold untouched by a rejected install")
	require.Equal(t, 0, debits)
	require.Equal(t, 0, satellites)
	require.Empty(t, inst.installedGoods(), "installer never reached")
}

// TestUnit_InstallSatellite_NoInstallerWired mirrors
// TestUnit_InstallJammer_NoInstallerWired: with no transactional installer the
// worker refuses to deploy instead of building a free satellite, and the handler
// must surface that as 503 (a misconfiguration, not a player error). Without this
// the ErrInstallerUnavailable branch here is deletable — the response silently
// becomes a 500 and nothing fails.
func TestUnit_InstallSatellite_NoInstallerWired(t *testing.T) {
	t.Parallel()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): {missileTestShip()}},
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	runWorker(t, w)

	rec := postInstallSatellite(t, srv, 1)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Empty(t, w.Snapshot(domain.SectorID(1)).Statics.Satellites, "no free satellite")
}

// TestUnit_InstallSatellite_AckTimeoutChargesOnce is the TASK-144 regression
// test for install-satellite (same hole, same fix as install-jammer): 504 makes
// no cargo call, the deferred command charges once and deploys once, and the
// retry on an empty hold yields no free satellite.
func TestUnit_InstallSatellite_AckTimeoutChargesOnce(t *testing.T) {
	t.Parallel()
	inst := &fakeInstaller{stock: 1}
	w := installTestWorker(inst, []domain.Ship{missileTestShip()})
	router := &deferredRouter{workerRouter: workerRouter{w}}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       20 * time.Millisecond,
		SectorID:         1,
	}, nil)

	rec := postInstallSatellite(t, srv, 1)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())

	stock, debits, _, satellites := inst.snapshot()
	require.EqualValues(t, 1, stock, "504 alone must not move goods")
	require.Equal(t, 0, debits, "the HTTP layer makes no cargo call at all")
	require.Equal(t, 0, satellites)

	router.release(t)
	stock, debits, _, satellites = inst.snapshot()
	require.EqualValues(t, 0, stock)
	require.Equal(t, 1, debits, "charged exactly once")
	require.Equal(t, 1, satellites, "exactly one satellite deployed")

	rec = postInstallSatellite(t, srv, 1)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	router.release(t)

	_, debits, _, satellites = inst.snapshot()
	assert.Equal(t, 1, debits, "no second charge")
	assert.Equal(t, 1, satellites, "no free second satellite")
	assert.Len(t, w.Snapshot(domain.SectorID(1)).Statics.Satellites, 1)
}
