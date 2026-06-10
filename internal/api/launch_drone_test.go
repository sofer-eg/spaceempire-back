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

// newDroneTestServer wires the drone endpoints over a real Worker and a
// fakeMissileCargo (the DroneCargo interface has the same Consume/Refund
// shape). Returns everything the tests act and assert on.
func newDroneTestServer(t *testing.T, initial []domain.Ship, stock int64) (*api.Server, *sector.Worker, *fakeMissileCargo) {
	t.Helper()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
	)
	cargo := &fakeMissileCargo{stock: stock}
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		DroneCargo:       cargo,
	}, nil)
	return srv, w, cargo
}

func TestUnit_LaunchDrone_OK(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	srv, w, fake := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 5)
	runWorker(t, w)

	body, _ := json.Marshal(dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     3,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-drone", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.LaunchDroneResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.Equal(t, 3, resp.Spawned)

	stock, consume, refund := fake.snapshot()
	require.EqualValues(t, 2, stock, "cargo decremented by Count")
	require.Equal(t, 1, consume)
	require.Equal(t, 0, refund)
}

func TestUnit_LaunchDrone_NotEnoughCargo(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999

	srv, w, fake := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 1)
	runWorker(t, w)

	body, _ := json.Marshal(dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     3,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-drone", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, _, refund := fake.snapshot()
	require.EqualValues(t, 1, stock, "stock untouched")
	require.Equal(t, 0, refund)
}

func TestUnit_RecallDrones_OK(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	srv, w, fake := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 5)
	runWorker(t, w)

	launch, _ := json.Marshal(dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     2,
	})
	lreq := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-drone", bytes.NewReader(launch))
	lrec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(lrec, lreq)
	require.Equal(t, http.StatusOK, lrec.Code, lrec.Body.String())

	recall, _ := json.Marshal(dto.RecallDronesRequest{ShipID: 1})
	rreq := httptest.NewRequest(http.MethodPost, "/api/cmd/recall-drones", bytes.NewReader(recall))
	rrec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rrec, rreq)

	require.Equal(t, http.StatusOK, rrec.Code, rrec.Body.String())
	var resp dto.RecallDronesResponse
	require.NoError(t, json.Unmarshal(rrec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.Equal(t, 2, resp.Recalled)

	stock, _, refund := fake.snapshot()
	require.EqualValues(t, 5, stock, "recall refunds both launched drones (3 left + 2 back)")
	require.Equal(t, 1, refund)
}

func TestUnit_LaunchDrone_NoCargoService_503(t *testing.T) {
	t.Parallel()
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): {missileTestShip()}})
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
	}, nil)

	body, _ := json.Marshal(dto.LaunchDroneRequest{
		ShipID: 1, TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2}, Count: 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-drone", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
