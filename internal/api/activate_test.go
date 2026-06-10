package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// stubActiveShipWriter records the last SetActiveShip call for assertions.
type stubActiveShipWriter struct {
	gotPlayer domain.PlayerID
	gotShip   domain.ShipID
	called    bool
}

func (s *stubActiveShipWriter) SetActiveShip(_ context.Context, p domain.PlayerID, sh domain.ShipID) error {
	s.gotPlayer, s.gotShip, s.called = p, sh, true
	return nil
}

func waitForShip(t *testing.T, r multiSectorRouter, id domain.ShipID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := r.LookupShipSector(id); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ship %d never appeared in snapshot", id)
}

func activateTestServer(t *testing.T, playerID domain.PlayerID, initial map[domain.SectorID][]domain.Ship) (*api.Server, multiSectorRouter, *stubActiveShipWriter, *bus.InMemory) {
	t.Helper()
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil, nil, initial,
	)
	router := multiSectorRouter{w: w}
	b := bus.NewInMemory(8)
	writer := &stubActiveShipWriter{}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		AuthMiddleware:   fakePlayerMiddleware(playerID),
		ActiveShipWriter: writer,
		HandoffPublisher: b,
	}, nil)
	runWorker(t, w)
	return srv, router, writer, b
}

func TestUnit_ActivateShip_SuccessSetsActiveAndPublishesHandoff(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	srv, router, writer, b := activateTestServer(t, playerID, map[domain.SectorID][]domain.Ship{
		1: {{ID: 10, PlayerID: playerID, SectorID: 1}},
		2: {{ID: 99, PlayerID: playerID, SectorID: 2}},
	})
	waitForShip(t, router, 99)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	evCh := make(chan sector.PlayerHandoffEvent, 1)
	require.NoError(t, b.Subscribe(ctx, sector.PlayerHandoffTopic(playerID), func(p []byte) {
		var e sector.PlayerHandoffEvent
		if json.Unmarshal(p, &e) == nil {
			evCh <- e
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/ship/99/activate", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, writer.called)
	assert.Equal(t, playerID, writer.gotPlayer)
	assert.Equal(t, domain.ShipID(99), writer.gotShip)

	select {
	case e := <-evCh:
		assert.Equal(t, domain.SectorID(2), e.TargetSector)
		assert.Equal(t, domain.ShipID(99), e.ShipID)
	case <-time.After(2 * time.Second):
		t.Fatal("handoff event not published")
	}
}

func TestUnit_ActivateShip_OtherPlayersShipReturns403(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	srv, router, writer, _ := activateTestServer(t, playerID, map[domain.SectorID][]domain.Ship{
		1: {{ID: 50, PlayerID: domain.PlayerID(8), SectorID: 1}},
	})
	waitForShip(t, router, 50)

	req := httptest.NewRequest(http.MethodPost, "/api/ship/50/activate", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, writer.called)
}

func TestUnit_ActivateShip_UnknownShipReturns404(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	srv, _, writer, _ := activateTestServer(t, playerID, map[domain.SectorID][]domain.Ship{
		1: {{ID: 10, PlayerID: playerID, SectorID: 1}},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/ship/999/activate", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.False(t, writer.called)
}
