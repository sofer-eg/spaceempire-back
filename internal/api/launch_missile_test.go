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

func missileTestShip() domain.Ship {
	return domain.Ship{
		ID:        1,
		PlayerID:  0,
		SectorID:  domain.SectorID(1),
		Pos:       domain.Vec2{X: 0, Y: 0},
		Direction: domain.Vec2{X: 1, Y: 0},
		HP:        100,
		MaxHP:     100,
		Shield:    50,
		MaxShield: 50,
		// Capability modules so the phase-10.14b gates (up_launcher for
		// missiles, up_drone_control for drones) pass for the happy-path
		// handler tests. The gates themselves are covered in sector tests.
		Equipment: []domain.InstalledEquipment{
			{Type: "up_launcher", Level: 1},
			{Type: "up_drone_control", Level: 8},
		},
	}
}

// newMissileTestServer wires the launch-missile path: a real Worker whose launch
// runs through a fakeOrdnance, and an api.Server with no launch cargo at all.
func newMissileTestServer(t *testing.T, initial []domain.Ship, missileStock int64) (*api.Server, *sector.Worker, *fakeOrdnance) {
	t.Helper()
	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.MissileGoodsType: missileStock})
	w := ordnanceTestWorker(ord, initial)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	return srv, w, ord
}

func postLaunchMissile(t *testing.T, srv *api.Server, body dto.LaunchMissileRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnit_LaunchMissile_OK(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	srv, w, ord := newMissileTestServer(t, []domain.Ship{missileTestShip(), target}, 3)
	runWorker(t, w)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.LaunchMissileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.NotZero(t, resp.MissileID)

	st := ord.snapshot()
	require.EqualValues(t, 2, ord.left(api.MissileGoodsType), "magazine decremented by 1")
	require.Equal(t, 1, st.debits)
	require.Equal(t, 1, st.missiles)
	require.Equal(t, 0, st.refunds, "nothing to refund — the debit is inside the launch")

	// Assert the literal id, not api.MissileGoodsType — comparing the handler's
	// constant against itself would pass even if it pointed at the drone's 51.
	require.Equal(t, []domain.GoodsTypeID{50}, ord.chargedGoods())
}

// TestUnit_LaunchMissile_NoCargo: an empty magazine is refused inside the launch
// transaction and surfaces as 400 — no missile fired.
func TestUnit_LaunchMissile_NoCargo(t *testing.T) {
	t.Parallel()
	srv, w, ord := newMissileTestServer(t, []domain.Ship{missileTestShip()}, 0)
	runWorker(t, w)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	st := ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.MissileGoodsType))
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.missiles)
	require.Equal(t, 0, st.refunds, "no refund — the failed debit rolled back")
}

// TestUnit_LaunchMissile_NonTargetableKind: a kind outside the missile target set
// (a drone — a projectile, not a target) is rejected at the handler boundary,
// before the command is even built (TASK-113 FR-06 "прочие → 400"). Containers and
// gates left this list in TASK-111/110 — see the next test.
func TestUnit_LaunchMissile_NonTargetableKind(t *testing.T) {
	t.Parallel()
	srv, _, ord := newMissileTestServer(t, []domain.Ship{missileTestShip()}, 5)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindDrone), ID: 7},
	})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.EqualValues(t, 5, ord.left(api.MissileGoodsType))
	require.Empty(t, ord.chargedGoods(), "request rejected before the ordnance is reached")
}

// TASK-111: a container passes the handler boundary now — the crate is a missile
// target. With no such container in the sector the worker rejects it on its own
// target gate (400) and, as with a missing static, the magazine stays untouched;
// what this pins is that the handler no longer refuses the KIND.
func TestUnit_LaunchMissile_ContainerTargetForwarded(t *testing.T) {
	t.Parallel()
	srv, w, ord := newMissileTestServer(t, []domain.Ship{missileTestShip()}, 5)
	runWorker(t, w)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindContainer), ID: 7},
	})

	// 400 from the WORKER (no such container in the sector), which is what proves
	// the container kind crossed the handler's kind switch.
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "invalid missile target")
	require.NotContains(t, rec.Body.String(), "invalid target kind")
	require.EqualValues(t, 5, ord.left(api.MissileGoodsType), "a refused launch spends nothing")
}

// TestUnit_LaunchMissile_StaticTargetForwarded: a destructible-static kind passes
// the handler boundary (TASK-113 FR-06) and is forwarded to the worker. With no
// such static in the sector the worker rejects it on the target gate (400) — and
// since TASK-147 that gate runs BEFORE the debit, so the magazine is untouched
// rather than debited-then-refunded.
func TestUnit_LaunchMissile_StaticTargetForwarded(t *testing.T) {
	t.Parallel()
	srv, w, ord := newMissileTestServer(t,
		[]domain.Ship{missileTestShip()}, // no station 7 → worker rejects
		3,
	)
	runWorker(t, w)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 7},
	})

	// 400 from the worker (not from the handler boundary) is what proves the
	// static kind crossed it: the handler answers "invalid missile target" only
	// after the worker's resolve fails.
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "invalid missile target")
	st := ord.snapshot()
	require.EqualValues(t, 3, ord.left(api.MissileGoodsType), "magazine untouched")
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.refunds)
}

func TestUnit_LaunchMissile_SelfTarget(t *testing.T) {
	t.Parallel()
	srv, _, ord := newMissileTestServer(t, []domain.Ship{missileTestShip()}, 5)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 1},
	})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.EqualValues(t, 5, ord.left(api.MissileGoodsType))
}

// TestUnit_LaunchMissile_SectorRejectsKeepsCargo: the worker rejects the launch
// on a gate (target missing) — the ammunition is never touched, so there is
// nothing to refund. This replaces the pre-TASK-147 refund test: the handler no
// longer debits up front, so a rejection cannot leave the magazine short.
func TestUnit_LaunchMissile_SectorRejectsKeepsCargo(t *testing.T) {
	t.Parallel()
	srv, w, ord := newMissileTestServer(t,
		[]domain.Ship{missileTestShip()}, // no target ship 2 → ErrInvalidAttackTarget
		3,
	)
	runWorker(t, w)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	st := ord.snapshot()
	require.EqualValues(t, 3, ord.left(api.MissileGoodsType), "magazine untouched")
	require.Equal(t, 0, st.debits)
	require.Equal(t, 0, st.refunds)
}

// TestUnit_LaunchMissile_NoOrdnanceWired: the worker has no transactional
// ordnance, so it refuses to fire rather than launch a free missile. The handler
// must surface that as 503 (a misconfiguration the player can only retry), not as
// a 200 for a shot nobody paid for.
func TestUnit_LaunchMissile_NoOrdnanceWired(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	w := noOrdnanceWorker(t, []domain.Ship{missileTestShip(), target})
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	runWorker(t, w)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})

	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Empty(t, w.Snapshot(domain.SectorID(1)).Missiles, "no free missile")
}

// TestUnit_LaunchMissile_AckTimeoutChargesOnce is the TASK-147 regression test.
// The command is accepted but the ack does not arrive in time: the handler
// answers 504 and — crucially — performs NO cargo call of its own (it has no
// cargo dependency left). When the worker finally applies the queued command the
// debit and the launch land together: one missile, one payment. The player's retry
// then hits an empty magazine and gets nothing for free.
func TestUnit_LaunchMissile_AckTimeoutChargesOnce(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	ord := newFakeOrdnance(map[domain.GoodsTypeID]int64{api.MissileGoodsType: 1})
	w := ordnanceTestWorker(ord, []domain.Ship{missileTestShip(), target})
	router := &deferredRouter{workerRouter: workerRouter{w}}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       20 * time.Millisecond,
		SectorID:         1,
	}, nil)

	rec := postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())

	st := ord.snapshot()
	require.EqualValues(t, 1, ord.left(api.MissileGoodsType), "504 alone must not move ammunition")
	require.Equal(t, 0, st.debits, "the HTTP layer makes no cargo call at all")
	require.Equal(t, 0, st.refunds)
	require.Equal(t, 0, st.missiles)

	// The worker applies the command it was already holding.
	router.release(t)
	st = ord.snapshot()
	require.EqualValues(t, 0, ord.left(api.MissileGoodsType))
	require.Equal(t, 1, st.debits, "charged exactly once")
	require.Equal(t, 1, st.missiles, "exactly one missile launched")
	require.Len(t, w.Snapshot(domain.SectorID(1)).Missiles, 1)

	// Player retries after the 504: the magazine is empty, so no free duplicate.
	rec = postLaunchMissile(t, srv, dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	require.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	router.release(t)

	st = ord.snapshot()
	assert.Equal(t, 1, st.debits, "no second charge")
	assert.Equal(t, 1, st.missiles, "no free second missile")
	assert.Len(t, w.Snapshot(domain.SectorID(1)).Missiles, 1)
}

// TestUnit_LaunchMissile_InvalidJSON: malformed body → 400.
func TestUnit_LaunchMissile_InvalidJSON(t *testing.T) {
	t.Parallel()
	srv, _, _ := newMissileTestServer(t, nil, 5)
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
