package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

func newTestServer(t *testing.T, initial []domain.Ship) (*api.Server, *sector.Worker) {
	t.Helper()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): initial},
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	return srv, w
}

// workerRouter adapts a single sector.Worker to api.SectorRouter so
// in-process tests can drive the HTTP handlers without spinning up a Pool.
type workerRouter struct{ w *sector.Worker }

func (r workerRouter) Send(sectorID domain.SectorID, cmd sector.Command) error {
	return r.w.Send(sectorID, cmd)
}
func (r workerRouter) Snapshot(sectorID domain.SectorID) sector.Snapshot {
	return r.w.Snapshot(sectorID)
}
func (r workerRouter) Subscribe(ctx context.Context, sectorID domain.SectorID, playerID domain.PlayerID) (*sector.Subscription, func(), error) {
	return r.w.Subscribe(ctx, sectorID, playerID)
}
func (r workerRouter) LookupShipSector(shipID domain.ShipID) (domain.SectorID, bool) {
	for _, sectorID := range r.w.Sectors() {
		snap := r.w.Snapshot(sectorID)
		for i := range snap.Ships {
			if snap.Ships[i].ID == shipID {
				return snap.Ships[i].SectorID, true
			}
		}
	}
	return 0, false
}

func (r workerRouter) LookupPrimaryShipByPlayer(playerID domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	var (
		best    domain.ShipID
		bestSec domain.SectorID
		set     bool
	)
	for _, sectorID := range r.w.Sectors() {
		snap := r.w.Snapshot(sectorID)
		for i := range snap.Ships {
			if snap.Ships[i].PlayerID != playerID {
				continue
			}
			if !set || snap.Ships[i].ID < best {
				best = snap.Ships[i].ID
				bestSec = snap.Ships[i].SectorID
				set = true
			}
		}
	}
	if !set {
		return 0, 0, false
	}
	return best, bestSec, true
}

func TestUnit_Healthz_ReturnsOK(t *testing.T) {
	t.Parallel()

	srv, _ := newTestServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw=%q)", err, rec.Body.String())
	}
	if body["status"] != "ok" {
		t.Fatalf(`body["status"] = %q, want "ok"`, body["status"])
	}
}
