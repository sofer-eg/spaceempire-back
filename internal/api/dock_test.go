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

const stationDockKind = 2 // domain.EntityKindStation

// newDockServer mounts a server backed by one worker pre-seeded with a
// ship and a station at the same position so DockRange=3 holds.
func newDockServer(t *testing.T) (*api.Server, *sector.Worker, domain.Vec2) {
	t.Helper()
	pos := domain.Vec2{X: 10, Y: 20}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64, DockRange: 3},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, PlayerID: 0, SectorID: 1, Pos: pos,
		}}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			1: {Stations: []domain.Station{{ID: 5, SectorID: 1, Pos: pos, Built: true}}},
		}),
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	return srv, w, pos
}

func TestUnit_Dock_Success(t *testing.T) {
	t.Parallel()
	srv, w, _ := newDockServer(t)
	runWorker(t, w)

	body, _ := json.Marshal(dto.DockRequest{
		ShipID: 1,
		Target: dto.EntityRef{Kind: stationDockKind, ID: 5},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/dock", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.DockResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
}

func TestUnit_Dock_OutOfRange_Returns400(t *testing.T) {
	t.Parallel()
	// Position the ship far from the station.
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64, DockRange: 3},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{
			ID: 1, SectorID: 1, Pos: domain.Vec2{X: 5000, Y: 0},
		}}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			1: {Stations: []domain.Station{{ID: 5, SectorID: 1, Pos: domain.Vec2{}, Built: true}}},
		}),
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	runWorker(t, w)

	body, _ := json.Marshal(dto.DockRequest{
		ShipID: 1, Target: dto.EntityRef{Kind: stationDockKind, ID: 5},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/dock", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_Dock_TargetNotFound_Returns404(t *testing.T) {
	t.Parallel()
	srv, w, _ := newDockServer(t)
	runWorker(t, w)

	body, _ := json.Marshal(dto.DockRequest{
		ShipID: 1, Target: dto.EntityRef{Kind: stationDockKind, ID: 999},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/dock", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_Dock_ShipNotFound_Returns404(t *testing.T) {
	t.Parallel()
	srv, w, _ := newDockServer(t)
	runWorker(t, w)

	body, _ := json.Marshal(dto.DockRequest{
		ShipID: 999, Target: dto.EntityRef{Kind: stationDockKind, ID: 5},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/dock", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_Dock_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()
	srv, _, _ := newDockServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/dock", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_Undock_Success(t *testing.T) {
	t.Parallel()
	srv, w, _ := newDockServer(t)
	runWorker(t, w)

	// First, dock.
	dockBody, _ := json.Marshal(dto.DockRequest{
		ShipID: 1, Target: dto.EntityRef{Kind: stationDockKind, ID: 5},
	})
	dockReq := httptest.NewRequest(http.MethodPost, "/api/cmd/dock", bytes.NewReader(dockBody))
	dockRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(dockRec, dockReq)
	require.Equal(t, http.StatusOK, dockRec.Code)

	// Then, undock.
	body, _ := json.Marshal(dto.UndockRequest{ShipID: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/undock", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.UndockResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.OK)
}

func TestUnit_Undock_NotDocked_Returns409(t *testing.T) {
	t.Parallel()
	srv, w, _ := newDockServer(t)
	runWorker(t, w)

	body, _ := json.Marshal(dto.UndockRequest{ShipID: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/undock", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
}
