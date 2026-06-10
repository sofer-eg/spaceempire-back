package api_test

import (
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
	"spaceempire/back/internal/world"
)

func newServerWithTopology(t *testing.T, topo *world.Topology) *api.Server {
	t.Helper()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 4},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): nil},
	)
	return api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		Topology:         topo,
	}, nil)
}

func TestUnit_World_ReturnsSectorsAndGates(t *testing.T) {
	t.Parallel()

	sectors := []domain.Sector{
		{ID: 1, Name: "Alpha", Bounds: domain.Rect{
			Min: domain.Vec2{X: -10, Y: -10}, Max: domain.Vec2{X: 10, Y: 10},
		}},
		{ID: 2, Name: "Beta"},
	}
	gates := []domain.Gate{
		{ID: 100, SectorA: 1, PosA: domain.Vec2{X: 5, Y: 0}, SectorB: 2, PosB: domain.Vec2{X: -5, Y: 0}},
	}
	srv := newServerWithTopology(t, world.New(sectors, gates))

	req := httptest.NewRequest(http.MethodGet, "/api/world", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var got dto.WorldResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))

	require.Len(t, got.Sectors, 2)
	assert.Equal(t, int64(1), got.Sectors[0].ID)
	assert.Equal(t, "Alpha", got.Sectors[0].Name)
	assert.Equal(t, dto.Rect{MinX: -10, MinY: -10, MaxX: 10, MaxY: 10}, got.Sectors[0].Bounds)

	require.Len(t, got.Gates, 1)
	assert.Equal(t, int64(100), got.Gates[0].ID)
	assert.Equal(t, int64(1), got.Gates[0].SectorA)
	assert.Equal(t, int64(2), got.Gates[0].SectorB)
	assert.Equal(t, 5.0, got.Gates[0].PosAX)
	assert.Equal(t, -5.0, got.Gates[0].PosBX)
}

func TestUnit_World_Returns503WhenTopologyNotLoaded(t *testing.T) {
	t.Parallel()

	srv := newServerWithTopology(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/world", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
