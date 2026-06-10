package api_test

import (
	"bytes"
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

// always401 is a stand-in for auth.RequireAuth that unconditionally rejects.
// The api package only cares that some middleware was wired — it does not
// import auth, so we use the simplest possible blocker here.
func always401(_ http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
}

func TestUnit_Move_RequiresAuthWhenMiddlewareWired(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): nil},
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		AuthMiddleware:   always401,
	}, nil)

	body, _ := json.Marshal(dto.MoveRequest{ShipID: 1, X: 1, Y: 2})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (middleware should have blocked)", rec.Code)
	}
}
