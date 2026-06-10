package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

func runWorker(t *testing.T, w *sector.Worker) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = w.Run(ctx) }()
}

func TestUnit_Move_SuccessReturnsOK(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, []domain.Ship{{ID: 1, SectorID: 1, MaxSpeed: 1}})
	runWorker(t, w)

	body, _ := json.Marshal(dto.MoveRequest{ShipID: 1, X: 50, Y: 25})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp dto.MoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Fatalf("OK = false, want true")
	}
}

func TestUnit_Move_ShipNotFoundReturns404(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, nil)
	runWorker(t, w)

	body, _ := json.Marshal(dto.MoveRequest{ShipID: 999, X: 1, Y: 2})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestUnit_Move_InvalidJSONReturns400(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/cmd/move", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

type stubWorker struct {
	sendErr  error
	snapshot sector.Snapshot
}

func (s *stubWorker) Send(_ domain.SectorID, _ sector.Command) error { return s.sendErr }
func (s *stubWorker) Snapshot(_ domain.SectorID) sector.Snapshot     { return s.snapshot }
func (s *stubWorker) Subscribe(_ context.Context, _ domain.SectorID, _ domain.PlayerID) (*sector.Subscription, func(), error) {
	return &sector.Subscription{}, func() {}, nil
}
func (s *stubWorker) LookupShipSector(id domain.ShipID) (domain.SectorID, bool) {
	if id == 0 {
		return 0, false
	}
	return 1, true
}
func (s *stubWorker) LookupPrimaryShipByPlayer(_ domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	return 0, 0, false
}

func TestUnit_Move_InboxFullReturns503(t *testing.T) {
	t.Parallel()

	stub := &stubWorker{sendErr: sector.ErrInboxFull}
	srv := api.NewServer(stub, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       50 * time.Millisecond,
		SectorID:         1,
	}, nil)

	body, _ := json.Marshal(dto.MoveRequest{ShipID: 1, X: 1, Y: 2})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestUnit_Move_AckTimeoutReturns504(t *testing.T) {
	t.Parallel()

	// silentWorker accepts the command but never delivers a reply.
	short := api.NewServer(&silentWorker{}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       30 * time.Millisecond,
		SectorID:         1,
	}, nil)

	body, _ := json.Marshal(dto.MoveRequest{ShipID: 1, X: 1, Y: 2})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	short.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

type silentWorker struct{}

func (silentWorker) Send(_ domain.SectorID, _ sector.Command) error { return nil }
func (silentWorker) Snapshot(_ domain.SectorID) sector.Snapshot     { return sector.Snapshot{} }
func (silentWorker) Subscribe(_ context.Context, _ domain.SectorID, _ domain.PlayerID) (*sector.Subscription, func(), error) {
	return &sector.Subscription{}, func() {}, nil
}
func (silentWorker) LookupShipSector(id domain.ShipID) (domain.SectorID, bool) {
	if id == 0 {
		return 0, false
	}
	return 1, true
}
func (silentWorker) LookupPrimaryShipByPlayer(_ domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	return 0, 0, false
}
