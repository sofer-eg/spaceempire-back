package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
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

func TestUnit_State_IncludesAsteroids(t *testing.T) {
	t.Parallel()

	asteroid := domain.Asteroid{
		ID: 5, SectorID: 1, Pos: domain.Vec2{X: 12, Y: -3}, Mass: 250, OreType: 2,
	}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): nil},
		sector.WithAsteroids(nil, map[domain.SectorID][]domain.Asteroid{1: {asteroid}}),
	)
	w.Tick(context.Background()) // publish a snapshot with the asteroid

	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
	}, nil)

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
	if len(snap.Asteroids) != 1 {
		t.Fatalf("asteroids count = %d, want 1", len(snap.Asteroids))
	}
	got := snap.Asteroids[0]
	if got.ID != 5 || got.X != 12 || got.Y != -3 || got.Mass != 250 || got.OreType != 2 {
		t.Fatalf("asteroid = %+v, want {ID:5 X:12 Y:-3 Mass:250 OreType:2}", got)
	}
}
