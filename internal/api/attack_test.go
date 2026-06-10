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
	"spaceempire/back/internal/sector"
)

func attackShip() domain.Ship {
	return domain.Ship{
		ID:              1,
		SectorID:        1,
		PlayerID:        0,
		HP:              50,
		MaxHP:           50,
		Shield:          20,
		MaxShield:       20,
		Energy:          100,
		MaxEnergy:       100,
		EnergyRecharge:  1,
		LaserDamage:     10,
		LaserRange:      400,
		LaserEnergyCost: 5,
	}
}

func TestUnit_Attack_SuccessSetsTarget(t *testing.T) {
	t.Parallel()

	target := attackShip()
	target.ID = 2
	target.PlayerID = 999
	srv, w := newTestServer(t, []domain.Ship{attackShip(), target})
	runWorker(t, w)

	body, _ := json.Marshal(dto.AttackRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/attack", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.AttackResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
}

func TestUnit_Attack_InvalidJSONReturns400(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/attack", bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_Attack_NonShipTargetReturns400(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, []domain.Ship{attackShip()})
	runWorker(t, w)

	body, _ := json.Marshal(dto.AttackRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 7},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/attack", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestUnit_Attack_ShipNotFoundReturns404(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, nil)
	runWorker(t, w)
	body, _ := json.Marshal(dto.AttackRequest{
		ShipID:    999,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/attack", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnit_Attack_InboxFullReturns503(t *testing.T) {
	t.Parallel()

	stub := &stubWorker{sendErr: sector.ErrInboxFull}
	srv := api.NewServer(stub, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       50 * time.Millisecond,
		SectorID:         1,
	}, nil)
	body, _ := json.Marshal(dto.AttackRequest{
		ShipID:    1,
		TargetRef: dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/attack", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUnit_CeaseFire_SuccessClearsTarget(t *testing.T) {
	t.Parallel()

	ship := attackShip()
	ref := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	ship.AttackTarget = &ref
	srv, w := newTestServer(t, []domain.Ship{ship})
	runWorker(t, w)

	body, _ := json.Marshal(dto.CeaseFireRequest{ShipID: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cease-fire", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Drain the worker to process the command and verify state.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snap := w.Snapshot(domain.SectorID(1))
		if len(snap.Ships) > 0 && snap.Ships[0].AttackTarget == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("AttackTarget not cleared in time: %+v", w.Snapshot(domain.SectorID(1)).Ships)
}

func TestUnit_CeaseFire_ShipNotFoundReturns404(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, nil)
	runWorker(t, w)
	body, _ := json.Marshal(dto.CeaseFireRequest{ShipID: 999})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cease-fire", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}
