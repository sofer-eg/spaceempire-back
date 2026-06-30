package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeMissileCargo is an in-memory api.MissileCargo. It records every
// Consume/Refund call so tests can assert the cargo lifecycle around
// the worker reply.
type fakeMissileCargo struct {
	mu      sync.Mutex
	stock   int64 // missiles currently in cargo (for the test ship)
	consume int   // call count
	refund  int   // call count
}

func (f *fakeMissileCargo) Consume(_ context.Context, _ domain.EntityRef, _ domain.GoodsTypeID, qty int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consume++
	if f.stock < qty {
		return cargo.ErrInsufficientQuantity
	}
	f.stock -= qty
	return nil
}

func (f *fakeMissileCargo) Refund(_ context.Context, _ domain.EntityRef, _ domain.GoodsTypeID, qty int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refund++
	f.stock += qty
	return nil
}

func (f *fakeMissileCargo) snapshot() (stock int64, consume, refund int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stock, f.consume, f.refund
}

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

// newMissileTestServer wires the launch-missile path: a real Worker, a
// fakeMissileCargo, and an api.Server with MissileCargo populated. Returns
// everything the tests need to act and assert.
func newMissileTestServer(t *testing.T, initial []domain.Ship, missileStock int64) (*api.Server, *sector.Worker, *fakeMissileCargo) {
	t.Helper()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
	)
	cargo := &fakeMissileCargo{stock: missileStock}
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		MissileCargo:     cargo,
	}, nil)
	return srv, w, cargo
}

func TestUnit_LaunchMissile_OK(t *testing.T) {
	t.Parallel()
	target := missileTestShip()
	target.ID = 2
	target.PlayerID = 999
	target.Pos = domain.Vec2{X: 100, Y: 0}

	srv, w, fake := newMissileTestServer(t, []domain.Ship{missileTestShip(), target}, 3)
	runWorker(t, w)

	body, _ := json.Marshal(dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.LaunchMissileResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	require.NotZero(t, resp.MissileID)

	stock, consume, refund := fake.snapshot()
	require.EqualValues(t, 2, stock, "cargo decremented by 1")
	require.Equal(t, 1, consume)
	require.Equal(t, 0, refund)
}

func TestUnit_LaunchMissile_NoCargo(t *testing.T) {
	t.Parallel()
	srv, w, fake := newMissileTestServer(t,
		[]domain.Ship{missileTestShip()},
		0, // empty cargo
	)
	runWorker(t, w)

	body, _ := json.Marshal(dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, _, refund := fake.snapshot()
	require.EqualValues(t, 0, stock)
	require.Equal(t, 0, refund, "no refund when Consume itself failed")
}

// TestUnit_LaunchMissile_NonTargetableKind: a kind that is neither a ship nor a
// destructible static (e.g. a container) is rejected at the handler boundary
// before any cargo is touched (TASK-113 FR-06 "прочие → 400").
func TestUnit_LaunchMissile_NonTargetableKind(t *testing.T) {
	t.Parallel()
	srv, _, fake := newMissileTestServer(t,
		[]domain.Ship{missileTestShip()},
		5,
	)
	body, _ := json.Marshal(dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindContainer), ID: 7},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	stock, consume, _ := fake.snapshot()
	require.EqualValues(t, 5, stock)
	require.Equal(t, 0, consume, "request rejected before touching cargo")
}

// TestUnit_LaunchMissile_StaticTargetForwarded: a destructible-static kind now
// passes the handler boundary (TASK-113 FR-06) and is forwarded to the worker.
// With no such static in the sector the worker rejects it (ErrInvalidAttackTarget
// → 400), so the handler must have debited the ammo and then refunded it — proof
// it crossed the boundary rather than being rejected at it.
func TestUnit_LaunchMissile_StaticTargetForwarded(t *testing.T) {
	t.Parallel()
	srv, w, fake := newMissileTestServer(t,
		[]domain.Ship{missileTestShip()}, // no station 7 → worker rejects
		3,
	)
	runWorker(t, w)

	body, _ := json.Marshal(dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 7},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, consume, refund := fake.snapshot()
	require.EqualValues(t, 3, stock, "ammo restored after the worker rejected the phantom static")
	require.Equal(t, 1, consume, "static kind crossed the boundary and debited ammo")
	require.Equal(t, 1, refund)
}

func TestUnit_LaunchMissile_SelfTarget(t *testing.T) {
	t.Parallel()
	srv, _, fake := newMissileTestServer(t,
		[]domain.Ship{missileTestShip()},
		5,
	)
	body, _ := json.Marshal(dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	stock, _, _ := fake.snapshot()
	require.EqualValues(t, 5, stock)
}

// TestUnit_LaunchMissile_SectorRejectsRefundsCargo: the sector worker
// rejects the launch (target missing) — handler must refund the missile
// it debited.
func TestUnit_LaunchMissile_SectorRejectsRefundsCargo(t *testing.T) {
	t.Parallel()
	srv, w, fake := newMissileTestServer(t,
		[]domain.Ship{missileTestShip()}, // no target ship 2 → worker replies ErrInvalidAttackTarget
		3,
	)
	runWorker(t, w)

	body, _ := json.Marshal(dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	stock, consume, refund := fake.snapshot()
	require.EqualValues(t, 3, stock, "cargo restored after worker rejection")
	require.Equal(t, 1, consume)
	require.Equal(t, 1, refund)
}

// TestUnit_LaunchMissile_NoCargoService_503: when MissileCargo is nil the
// endpoint returns 503 (legacy bring-up path).
func TestUnit_LaunchMissile_NoCargoService_503(t *testing.T) {
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
		// MissileCargo intentionally nil
	}, nil)

	body, _ := json.Marshal(dto.LaunchMissileRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/launch-missile", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
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

// guard against compiler warnings on errors imported only by handler tests.
var _ = errors.New
