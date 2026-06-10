package api_test

import (
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

type stubFleet struct{ ships []domain.Ship }

func (s stubFleet) ShipsByPlayer(_ domain.PlayerID) []domain.Ship { return s.ships }

func TestUnit_Fleet_ListsOwnShips(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, InboxCapacity: 8},
		clock.NewRealClock(),
		nil, nil, nil,
	)
	srv := api.NewServer(multiSectorRouter{w: w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		AuthMiddleware:   fakePlayerMiddleware(playerID),
		Fleet: stubFleet{ships: []domain.Ship{
			{ID: 10, PlayerID: playerID, SectorID: 1, Name: "Разведчик"},
			{ID: 99, PlayerID: playerID, SectorID: 2, IsSpacesuit: true},
		}},
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/player/ships", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.FleetResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Ships, 2)

	byID := make(map[int64]dto.Ship, len(resp.Ships))
	for _, s := range resp.Ships {
		byID[s.ID] = s
	}
	assert.Equal(t, "Разведчик", byID[10].Name)
	assert.True(t, byID[99].IsSpacesuit)
}

func TestUnit_Fleet_DisabledReturns503(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, InboxCapacity: 8},
		clock.NewRealClock(),
		nil, nil, nil,
	)
	srv := api.NewServer(multiSectorRouter{w: w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		AuthMiddleware:   fakePlayerMiddleware(domain.PlayerID(7)),
		// Fleet intentionally nil.
	}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/player/ships", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
