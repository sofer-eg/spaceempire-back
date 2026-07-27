package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// newJammerTestServer wires the install-jammer path: a real Worker, a
// fakeMissileCargo (the same Consume/Refund recorder — api.JammerCargo has the
// identical shape), and an api.Server with JammerCargo populated.
func newJammerTestServer(t *testing.T, initial []domain.Ship, stock int64) (*api.Server, *sector.Worker, *fakeMissileCargo) {
	t.Helper()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
	)
	c := &fakeMissileCargo{stock: stock}
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		JammerCargo:      c,
	}, nil)
	return srv, w, c
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
// and returns the new jammer id.
func TestUnit_InstallJammer_OK(t *testing.T) {
	t.Parallel()
	srv, w, fake := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 3)
	runWorker(t, w)

	rec := postInstallJammer(t, srv, 1)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.InstallJammerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.NotZero(t, resp.JammerID)

	stock, consume, refund := fake.snapshot()
	require.EqualValues(t, 2, stock, "cargo decremented by 1")
	require.Equal(t, 1, consume)
	require.Equal(t, 0, refund)
}

// TestUnit_InstallJammer_NoCargo: an empty hold is rejected before the worker
// is touched, and nothing is refunded (Consume itself failed).
func TestUnit_InstallJammer_NoCargo(t *testing.T) {
	t.Parallel()
	srv, w, fake := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 0)
	runWorker(t, w)

	rec := postInstallJammer(t, srv, 1)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, _, refund := fake.snapshot()
	require.EqualValues(t, 0, stock)
	require.Equal(t, 0, refund, "no refund when Consume itself failed")
}

// TestUnit_InstallJammer_SectorRejectsRefundsCargo: the worker rejects the
// install (no such ship) — the handler must refund the generator it debited.
func TestUnit_InstallJammer_SectorRejectsRefundsCargo(t *testing.T) {
	t.Parallel()
	srv, w, fake := newJammerTestServer(t, nil, 3) // no ship 1 in the sector
	runWorker(t, w)

	rec := postInstallJammer(t, srv, 1)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	stock, consume, refund := fake.snapshot()
	require.EqualValues(t, 3, stock, "cargo restored after worker rejection")
	require.Equal(t, 1, consume)
	require.Equal(t, 1, refund)
}

// TestUnit_InstallJammer_NoCargoService_503: without JammerCargo the endpoint
// reports unavailable (legacy bring-up path).
func TestUnit_InstallJammer_NoCargoService_503(t *testing.T) {
	t.Parallel()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): {missileTestShip()}},
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		// JammerCargo intentionally nil
	}, nil)

	rec := postInstallJammer(t, srv, 1)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
