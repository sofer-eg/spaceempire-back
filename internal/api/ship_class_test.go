package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TestUnit_State_HullCategory_FromCatalog proves the whole 10.13 backend path:
// the ship-class catalog wired into the Server builds the id→category index,
// and a ship carrying ShipClassID surfaces the right hullCategory in the
// /api/state snapshot. Class 5 → "M5" (scout) per balance.categoryByClass.
func TestUnit_State_HullCategory_FromCatalog(t *testing.T) {
	t.Parallel()

	classes, err := balance.NewShipClasses([]balance.ShipClass{
		{ID: 81, Race: 1, Type: 5, Class: 5, Name: "Разведчик"},
	})
	require.NoError(t, err)

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{
			domain.SectorID(1): {{ID: 7, ShipClassID: 81, Pos: domain.Vec2{X: 1, Y: 2}, MaxSpeed: 1}},
		},
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		ShipClasses:      classes,
	}, nil)

	w.Tick(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var snap dto.Snapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	require.Len(t, snap.Ships, 1)
	require.Equal(t, "M5", snap.Ships[0].HullCategory)
}
