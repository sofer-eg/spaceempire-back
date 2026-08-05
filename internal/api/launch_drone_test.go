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

// newDroneTestServer wires the drone endpoints over a real Worker whose launch
// AND recall run through a fakeOrdnance, so both move one shared stock and the
// round trip is assertable. Since TASK-152 the server itself owns no cargo at
// all — the credit happens inside the worker, like the debit.
func newDroneTestServer(t *testing.T, initial []domain.Ship, stock int64) (*api.Server, *sector.Worker, *fakeOrdnance) {
	t.Helper()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.DroneGoodsType: stock})
	w := ordnanceTestWorker(ord, initial)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
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

	// No count in the request: the server decides the salvo (TASK-176). The ship
	// carries up_drone_control level 8 and 5 units, so the hold is what limits it.
	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.LaunchDroneResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.Equal(t, 5, resp.Spawned, "everything the hold could pay for")

	st := ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.DroneGoodsType), "magazine decremented by the salvo")
	require.Equal(t, 1, st.debits, "one charge for the whole salvo")
	require.Equal(t, 5, st.drones)
	require.Equal(t, 0, st.refunds, "no shortfall to refund any more")

	// 21 is «Боевой дрон» — assert the literal so a constant pointing at the
	// missile's 10 would fail.
	require.Equal(t, []domain.GoodsTypeID{21}, ord.chargedGoods())
}

// TestUnit_LaunchDrone_ShortHoldLaunchesWhatItHas is the defect TASK-176 fixes, at
// the HTTP boundary: a single drone aboard a level-8 module launches ONE drone and
// answers 200, where it used to be a flat 400 ("3 requested, 1 in the hold"). Both
// SPA entry points reach this handler, so neither can go back to disagreeing about
// it — the canvas menu had no cargo to pre-clamp with and always got the 400.
func TestUnit_LaunchDrone_ShortHoldLaunchesWhatItHas(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	srv, w, ord := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 1)
	runWorker(t, w)

	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.LaunchDroneResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 1, resp.Spawned, "the hold is the salvo")

	st := ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.DroneGoodsType), "the one unit is paid for")
	require.Equal(t, 1, st.debits)
	require.Equal(t, 1, st.drones, "one drone created, not the three the salvo prepared")
	require.Equal(t, 0, st.refunds)
	// The published Snapshot lags one SnapshotInterval behind the ack, so what
	// landed in RAM is asserted in the sector suite
	// (TestUnit_LaunchDrone_ShortHoldLaunchesWhatItHas) instead of raced here.
}

// TestUnit_LaunchDrone_EmptyHoldIsBadRequest: nothing aboard is still a 400 with
// "not enough drones in cargo", not a cheerful 200 / spawned 0 the player would have
// to interpret.
func TestUnit_LaunchDrone_EmptyHoldIsBadRequest(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999

	srv, w, ord := newDroneTestServer(t, []domain.Ship{missileTestShip(), target}, 0)
	runWorker(t, w)

	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	st := ord.snapshot()
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.drones)
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

	// The salvo empties the 5-unit hold (level-8 module), so all five come back.
	lrec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
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
	require.Equal(t, 5, resp.Recalled, "the recall takes back every drone the ship has out")

	// Debit and credit both run in the worker against the same stock, so the
	// round trip nets out.
	require.EqualValues(t, 5, ord.left(api.DroneGoodsType),
		"recall credits every launched drone")
	require.Equal(t, 1, ord.snapshot().refunds)
}

// TestUnit_RecallDrones_NoOrdnanceWired: with no transactional ordnance the
// worker refuses the recall instead of deleting the drones with nobody to credit
// the player, and the handler surfaces 503 (TASK-152). Before it, the same
// misconfiguration was a nil DroneCargo on the HTTP layer.
func TestUnit_RecallDrones_NoOrdnanceWired(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	live := []domain.Drone{{
		ID: 7, SectorID: 1, OwnerShipID: 1, PlayerID: missileTestShip().PlayerID,
		HP: 20, ExpiresAt: time.Now().Add(time.Hour),
		Target: domain.EntityRef{Kind: domain.EntityKindShip, ID: 2},
	}}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): {missileTestShip(), target}},
		sector.WithDrones(nil, map[domain.SectorID][]domain.Drone{domain.SectorID(1): live}),
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
	}, nil)
	runWorker(t, w)

	body, _ := json.Marshal(dto.RecallDronesRequest{ShipID: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/recall-drones", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Len(t, w.Snapshot(domain.SectorID(1)).Drones, 1, "the drone is not deleted uncredited")
}

// TestUnit_RecallDrones_AckTimeoutCreditsCargo is the TASK-152 regression test at
// the HTTP boundary, the mirror of TestUnit_LaunchDrone_AckTimeoutChargesOnce:
// the handler gives up with a 504 and the deferred command still applies — and
// because the credit rides inside it, the player gets the units back. Before the
// fix the departed handler was the only thing that would have credited them, so
// the drones were deleted and the consumable lost.
func TestUnit_RecallDrones_AckTimeoutCreditsCargo(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.DroneGoodsType: 2})
	w := ordnanceTestWorker(ord, []domain.Ship{missileTestShip(), target})
	// A router that holds every command back past AckTimeout: the handler 504s and
	// walks away, then release() applies the command exactly as a delayed tick
	// would. The worker is driven by release() alone, never by a Run loop.
	router := &deferredRouter{workerRouter: workerRouter{w}}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       20 * time.Millisecond,
		SectorID:         1,
	}, nil)

	lrec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	require.Equal(t, http.StatusGatewayTimeout, lrec.Code, lrec.Body.String())
	router.release(t)
	require.EqualValues(t, 0, ord.left(api.DroneGoodsType), "the salvo emptied the hold")
	require.Len(t, w.Snapshot(domain.SectorID(1)).Drones, 2)

	body, _ := json.Marshal(dto.RecallDronesRequest{ShipID: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/recall-drones", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	require.EqualValues(t, 0, ord.left(api.DroneGoodsType), "504 alone credits nothing")
	require.Equal(t, 0, ord.snapshot().refunds, "the HTTP layer makes no cargo call at all")

	router.release(t)

	assert.EqualValues(t, 2, ord.left(api.DroneGoodsType),
		"the abandoned recall still credited the units")
	assert.Equal(t, 1, ord.snapshot().refunds)
	assert.Empty(t, w.Snapshot(domain.SectorID(1)).Drones, "the drones were recalled")
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
	}, nil)

	rec := postLaunchDrone(t, srv, dto.LaunchDroneRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
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
	})
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	router.release(t)

	st = ord.snapshot()
	assert.Equal(t, 1, st.debits, "no second charge")
	assert.Equal(t, 2, st.drones, "no free extra drone")
	assert.Len(t, w.Snapshot(domain.SectorID(1)).Drones, 2)
}
