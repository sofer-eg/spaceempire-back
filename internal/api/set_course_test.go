package api_test

import (
	"bytes"
	"context"
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

// fakePathRouter is the minimal api.PathRouter implementation tests need.
type fakePathRouter struct {
	hops      int
	reachable bool
}

func (f fakePathRouter) Hops(_, _ domain.SectorID) (int, bool) { return f.hops, f.reachable }

func setCourseRequest(t *testing.T, srv *api.Server, body dto.SetCourseRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/set-course", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func newSetCourseServer(t *testing.T, initial []domain.Ship, router api.PathRouter) (*api.Server, *sector.Worker) {
	t.Helper()
	_, w := newTestServer(t, initial)
	// Re-wrap with PathRouter; existing newTestServer leaves it nil.
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		PathRouter:       router,
	}, nil)
	return srv, w
}

func TestUnit_SetCourse_Success(t *testing.T) {
	t.Parallel()

	srv, w := newSetCourseServer(t, []domain.Ship{{ID: 1, SectorID: 1, MaxSpeed: 1,
		Equipment: []domain.InstalledEquipment{{Type: "up_autopilot", Level: 1}}}},
		fakePathRouter{hops: 3, reachable: true})
	runWorker(t, w)

	rec := setCourseRequest(t, srv, dto.SetCourseRequest{ShipID: 1, SectorID: 5, X: 100, Y: 200})

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.SetCourseResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, 3, resp.Hops)
}

func TestUnit_SetCourse_NoAutopilotModule_Returns422(t *testing.T) {
	t.Parallel()

	// Ship without up_autopilot → SetCourseCommand gate (10.3.11) rejects
	// with ErrEquipmentRequired, surfaced as 422.
	srv, w := newSetCourseServer(t, []domain.Ship{{ID: 1, SectorID: 1, MaxSpeed: 1}}, fakePathRouter{hops: 3, reachable: true})
	runWorker(t, w)

	rec := setCourseRequest(t, srv, dto.SetCourseRequest{ShipID: 1, SectorID: 5, X: 100, Y: 200})

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_SetCourse_Unreachable_Returns400(t *testing.T) {
	t.Parallel()

	srv, w := newSetCourseServer(t, []domain.Ship{{ID: 1, SectorID: 1}}, fakePathRouter{reachable: false})
	runWorker(t, w)

	rec := setCourseRequest(t, srv, dto.SetCourseRequest{ShipID: 1, SectorID: 99, X: 0, Y: 0})

	assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_SetCourse_ShipNotFound_Returns404(t *testing.T) {
	t.Parallel()

	srv, w := newSetCourseServer(t, nil, fakePathRouter{hops: 1, reachable: true})
	runWorker(t, w)

	rec := setCourseRequest(t, srv, dto.SetCourseRequest{ShipID: 42, SectorID: 2, X: 0, Y: 0})

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnit_SetCourse_NoPathRouter_Returns503(t *testing.T) {
	t.Parallel()

	// newTestServer leaves PathRouter nil → endpoint must return 503.
	srv, _ := newTestServer(t, []domain.Ship{{ID: 1, SectorID: 1}})

	rec := setCourseRequest(t, srv, dto.SetCourseRequest{ShipID: 1, SectorID: 2})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUnit_SetCourse_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()

	srv, w := newSetCourseServer(t, nil, fakePathRouter{reachable: true})
	runWorker(t, w)

	req := httptest.NewRequest(http.MethodPost, "/api/cmd/set-course", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// stubRouterWithLookup implements api.SectorRouter for a forbidden test:
// a ship exists (so LookupShipSector hits), but its PlayerID differs from
// the request's player → SetCourseCommand returns ErrForbidden.
type stubRouterWithLookup struct {
	w *sector.Worker
}

func (s stubRouterWithLookup) Send(sectorID domain.SectorID, cmd sector.Command) error {
	return s.w.Send(sectorID, cmd)
}
func (s stubRouterWithLookup) Snapshot(sectorID domain.SectorID) sector.Snapshot {
	return s.w.Snapshot(sectorID)
}
func (s stubRouterWithLookup) Subscribe(ctx context.Context, sectorID domain.SectorID, playerID domain.PlayerID) (*sector.Subscription, func(), error) {
	return s.w.Subscribe(ctx, sectorID, playerID)
}
func (s stubRouterWithLookup) LookupShipSector(_ domain.ShipID) (domain.SectorID, bool) {
	return 1, true
}
func (s stubRouterWithLookup) LookupPrimaryShipByPlayer(_ domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	return 0, 0, false
}

func TestUnit_SetCourse_Forbidden_Returns403(t *testing.T) {
	t.Parallel()

	// Ship owned by player 7; the request runs without auth context (PlayerID 0)
	// → SetCourseCommand returns ErrForbidden.
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{ID: 1, PlayerID: 7, SectorID: 1}}},
	)
	// LookupShipSector via snapshot scan needs a published snapshot — but
	// newSectorState publishes it at construction, so workerRouter's lookup
	// works. Stub keeps the test self-contained even if that ever changes.
	srv := api.NewServer(stubRouterWithLookup{w: w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		PathRouter:       fakePathRouter{hops: 1, reachable: true},
	}, nil)
	runWorker(t, w)

	rec := setCourseRequest(t, srv, dto.SetCourseRequest{ShipID: 1, SectorID: 2})

	assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
}
