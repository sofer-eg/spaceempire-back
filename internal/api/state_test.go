package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
)

func TestUnit_State_ReturnsSnapshot(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, []domain.Ship{
		{ID: 7, Pos: domain.Vec2{X: 3, Y: 4}, MaxSpeed: 1},
	})

	// Drive one tick so Tick > 0 and snapshot is published.
	w.Tick(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var snap dto.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, rec.Body.String())
	}
	if snap.Type != "snapshot" {
		t.Fatalf("Type = %q, want snapshot", snap.Type)
	}
	if snap.SectorID != 1 {
		t.Fatalf("SectorID = %d, want 1", snap.SectorID)
	}
	if snap.Tick < 1 {
		t.Fatalf("Tick = %d, want >= 1", snap.Tick)
	}
	if len(snap.Ships) != 1 {
		t.Fatalf("ships count = %d, want 1", len(snap.Ships))
	}
	if snap.Ships[0].ID != 7 || snap.Ships[0].X != 3 || snap.Ships[0].Y != 4 {
		t.Fatalf("ship = %+v, want {ID:7 X:3 Y:4}", snap.Ships[0])
	}
}

func TestUnit_State_EmptySector(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var snap dto.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snap.Ships) != 0 {
		t.Fatalf("ships count = %d, want 0", len(snap.Ships))
	}
}
