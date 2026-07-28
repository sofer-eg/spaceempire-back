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
	"spaceempire/back/internal/sector"
)

// newDroneTestServer wires the drone endpoints over a real Worker whose launch
// runs through a fakeOrdnance. The same fake is also the server's DroneCargo, so
// a launch (debited inside the worker) and a recall (credited by the handler)
// move one shared stock and the round trip is assertable.
func newDroneTestServer(t *testing.T, initial []domain.Ship, stock int64) (*api.Server, *sector.Worker, *fakeOrdnance) {
	t.Helper()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.DroneGoodsType: stock})
	w := ordnanceTestWorker(ord, initial)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		DroneCargo:       ord,
	}, nil)
	return srv, w, ord
}

func postLaunchDrone(t *testing.T, srv *api.Server, body dto.LaunchDroneRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-drone", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnit_LaunchDrone_OK(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	srv, w, ord := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 5)
	runWorker(t, w)

	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     3,
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.LaunchDroneResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.Equal(t, 3, resp.Spawned)

	st := ord.snapshot()
	require.EqualValues(t, 2, ord.left(api.DroneGoodsType), "magazine decremented by the salvo")
	require.Equal(t, 1, st.debits, "one all-or-nothing charge for the whole salvo")
	require.Equal(t, 3, st.drones)
	require.Equal(t, 0, st.refunds, "no shortfall to refund any more")

	// 51 is «Боевой дрон» — assert the literal so a constant pointing at the
	// missile's 50 would fail.
	require.Equal(t, []domain.GoodsTypeID{51}, ord.chargedGoods())
}

// TestUnit_LaunchDrone_NotEnoughCargo: fewer units in the hold than the salvo
// needs → the whole launch is refused inside the transaction (400), nothing
// spawned, nothing charged.
func TestUnit_LaunchDrone_NotEnoughCargo(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999

	srv, w, ord := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 1)
	runWorker(t, w)

	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     3,
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	st := ord.snapshot()
	require.EqualValues(t, 1, ord.left(api.DroneGoodsType), "stock untouched")
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.drones)
	require.Equal(t, 0, st.refunds)
	require.Empty(t, w.Snapshot(domain.SectorID(1)).Drones)
}

func TestUnit_RecallDrones_OK(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	srv, w, ord := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 5)
	runWorker(t, w)

	lrec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     2,
	})
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

	// The launch debit runs in the worker, the recall credit in the handler —
	// against the same stock, so the round trip nets out.
	require.EqualValues(t, 5, ord.left(api.DroneGoodsType),
		"recall refunds both launched drones (3 left + 2 back)")
	require.Equal(t, 1, ord.snapshot().refunds)
}

// TestUnit_RecallDrones_NoCargoService_503: recall is the one drone endpoint that
// still needs cargo (to credit the returned drones), so a nil DroneCargo disables
// it. The launch endpoint has no such dependency any more.
func TestUnit_RecallDrones_NoCargoService_503(t *testing.T) {
	t.Parallel()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.DroneGoodsType: 5})
	w := ordnanceTestWorker(ord, []domain.Ship{missileTestShip()})
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
		// DroneCargo intentionally nil
	}, nil)

	body, _ := json.Marshal(dto.RecallDronesRequest{ShipID: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/recall-drones", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestUnit_LaunchDrone_NoOrdnanceWired: with no transactional ordnance the worker
// refuses the salvo instead of spawning free drones, and the handler surfaces 503.
func TestUnit_LaunchDrone_NoOrdnanceWired(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	w := noOrdnanceWorker(t, []domain.Ship{missileTestShip(), target})
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
	}, nil)
	runWorker(t, w)

	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     1,
	})

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Empty(t, w.Snapshot(domain.SectorID(1)).Drones, "no free drones")
}

// TestUnit_LaunchDrone_AckTimeoutChargesOnce is the TASK-147 regression test for
// the drone salvo: the 504 makes no cargo call, the deferred command charges once
// and spawns once, and the retry on an empty magazine yields no free drones.
func TestUnit_LaunchDrone_AckTimeoutChargesOnce(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.DroneGoodsType: 2})
	w := ordnanceTestWorker(ord, []domain.Ship{missileTestShip(), target})
	router := &deferredRouter{workerRouter: workerRouter{w}}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       20 * time.Millisecond,
		SectorID:         1,
		DroneCargo:       ord,
	}, nil)

	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     2,
	})
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())

	st := ord.snapshot()
	require.EqualValues(t, 2, ord.left(api.DroneGoodsType), "504 alone must not move ammunition")
	require.Equal(t, 0, st.debits, "the HTTP layer makes no cargo call at all")
	require.Equal(t, 0, st.refunds)
	require.Equal(t, 0, st.drones)

	router.release(t)
	st = ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.DroneGoodsType))
	require.Equal(t, 1, st.debits, "charged exactly once")
	require.Equal(t, 2, st.drones, "exactly two drones launched")
	require.Len(t, w.Snapshot(domain.SectorID(1)).Drones, 2)

	rec = postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		Count:     1,
	})
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	router.release(t)

	st = ord.snapshot()
	assert.Equal(t, 1, st.debits, "no second charge")
	assert.Equal(t, 2, st.drones, "no free extra drone")
	assert.Len(t, w.Snapshot(domain.SectorID(1)).Drones, 2)
}
