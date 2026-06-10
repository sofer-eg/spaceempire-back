package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// stubActiveShips is a fixed ActiveShipReader for WS subscribe tests.
type stubActiveShips struct {
	ship domain.ShipID
	ok   bool
}

func (s stubActiveShips) ActiveShip(_ context.Context, _ domain.PlayerID) (domain.ShipID, bool, error) {
	return s.ship, s.ok, nil
}

func (s stubActiveShips) PassengerHost(_ context.Context, _ domain.PlayerID) (domain.ShipID, bool, error) {
	return 0, false, nil
}

// TestUnit_WS_FollowsActiveShipSector verifies that when the player has an
// explicit active ship (10.14a), the WS subscribes to that ship's sector even
// when it is not the lowest-id ship. Player owns ship 10 (sector 1, min-id) and
// ship 99 (sector 2); active_ship_id = 99 → the WS must lock on to sector 2.
func TestUnit_WS_FollowsActiveShipSector(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	initial := map[domain.SectorID][]domain.Ship{
		1: {{ID: 10, PlayerID: playerID, SectorID: 1, Pos: domain.Vec2{X: 1, Y: 2}}},
		2: {{ID: 99, PlayerID: playerID, SectorID: 2, Pos: domain.Vec2{X: 3, Y: 4}}},
	}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil, nil, initial,
	)
	router := multiSectorRouter{w: w}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		AuthMiddleware:   fakePlayerMiddleware(playerID),
		ActiveShips:      stubActiveShips{ship: 99, ok: true},
	}, nil)
	runWorker(t, w)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	welcome := readStatics(t, ctx, conn)
	assert.Equal(t, int64(2), welcome.SectorID, "WS must follow the active ship's sector, not the min-id ship")
}

// TestUnit_WS_ActiveShipFallsBackToMinID verifies that with no explicit active
// ship (active_ship_id NULL) the WS keeps the legacy lowest-id behaviour.
func TestUnit_WS_ActiveShipFallsBackToMinID(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	initial := map[domain.SectorID][]domain.Ship{
		1: {{ID: 10, PlayerID: playerID, SectorID: 1, Pos: domain.Vec2{X: 1, Y: 2}}},
		2: {{ID: 99, PlayerID: playerID, SectorID: 2, Pos: domain.Vec2{X: 3, Y: 4}}},
	}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil, nil, initial,
	)
	router := multiSectorRouter{w: w}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		AuthMiddleware:   fakePlayerMiddleware(playerID),
		ActiveShips:      stubActiveShips{ok: false},
	}, nil)
	runWorker(t, w)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	welcome := readStatics(t, ctx, conn)
	assert.Equal(t, int64(1), welcome.SectorID, "no active ship → lowest-id ship's sector")
}
