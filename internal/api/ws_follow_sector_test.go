package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakePlayerMiddleware injects playerID into the request context as if the
// real RequireAuth middleware had run, but without requiring a session
// cookie. Used by tests that need an authenticated WS connection without
// wiring the full auth stack.
func fakePlayerMiddleware(pid domain.PlayerID) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.ContextWithPlayerID(r.Context(), pid)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TestUnit_WS_FollowsPlayerSector verifies that a WS connection subscribes
// to the sector where the player's ship currently lives, not the static
// cfg.SectorID. Regression test for phase 3.14 — before the fix, a player
// whose ship had handoff'd to sector 2 was stuck receiving sector 1
// patches and never saw their own ship.
func TestUnit_WS_FollowsPlayerSector(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	initial := map[domain.SectorID][]domain.Ship{
		1: nil,
		2: {{ID: 42, PlayerID: playerID, SectorID: 2, Pos: domain.Vec2{X: 10, Y: 20}}},
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
		// cfg.SectorID intentionally points at sector 1 — the player's
		// ship is in sector 2, so the WS must pick sector 2.
		SectorID:       1,
		AuthMiddleware: fakePlayerMiddleware(playerID),
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
	assert.Equal(t, int64(2), welcome.SectorID, "WS welcome must carry the ship's sector, not cfg.SectorID")

	snap := readSnapshot(t, ctx, conn)
	assert.Equal(t, int64(2), snap.SectorID, "snapshot.sectorID must match the player's sector")
	require.NotEmpty(t, snap.Added, "first snapshot must carry the player's ship in Added")
	assert.Equal(t, int64(42), snap.Added[0].ID)
}

// TestUnit_WS_HandoffReSubscribes verifies that a PlayerHandoffEvent
// published on the bus causes the open WS connection to drop its current
// sector subscription and rebind to the target sector, sending a fresh
// statics frame for the new sector. The socket stays open across the hop.
func TestUnit_WS_HandoffReSubscribes(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	initial := map[domain.SectorID][]domain.Ship{
		1: {{ID: 42, PlayerID: playerID, SectorID: 1, Pos: domain.Vec2{X: 10, Y: 20}}},
		2: nil,
	}
	b := bus.NewInMemory(8)
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
		HandoffBus:       b,
	}, nil)
	runWorker(t, w)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	welcome := readStatics(t, ctx, conn)
	require.Equal(t, int64(1), welcome.SectorID, "WS must start in sector 1")

	// Drain the initial sector-1 snapshot so the next read after handoff is
	// definitively the new sector. Test doesn't care about its contents.
	_ = readSnapshot(t, ctx, conn)

	// Simulate a handoff: the worker normally publishes PlayerHandoffEvent
	// from executeJump after intake; here we publish it directly so the
	// test doesn't depend on the full jump command pipeline.
	payload, err := json.Marshal(sector.PlayerHandoffEvent{
		PlayerID:     playerID,
		ShipID:       42,
		SourceSector: 1,
		TargetSector: 2,
	})
	require.NoError(t, err)
	require.NoError(t, b.Publish(ctx, sector.PlayerHandoffTopic(playerID), payload))

	// After the hop the server must emit a fresh statics frame with the new
	// sectorID. The next frame after that is sector-2's snapshot.
	deadline := time.Now().Add(3 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		_, raw, err := conn.Read(ctx)
		require.NoError(t, err)
		var probe struct {
			Type     string `json:"type"`
			SectorID int64  `json:"sectorID"`
		}
		require.NoError(t, json.Unmarshal(raw, &probe))
		if probe.Type == "statics" && probe.SectorID == 2 {
			got = probe.SectorID
			break
		}
	}
	assert.Equal(t, int64(2), got, "after handoff WS must send a statics frame for the target sector")
}

// multiSectorRouter adapts a sector.Worker into api.SectorRouter so the WS
// tests above can drive a worker that owns multiple sectors without
// pulling the full sector.Pool. Mirrors workerRouter from healthz_test
// but the lookups are required for follow-sector to work.
type multiSectorRouter struct {
	w *sector.Worker
}

func (r multiSectorRouter) Send(sectorID domain.SectorID, cmd sector.Command) error {
	return r.w.Send(sectorID, cmd)
}
func (r multiSectorRouter) Snapshot(sectorID domain.SectorID) sector.Snapshot {
	return r.w.Snapshot(sectorID)
}
func (r multiSectorRouter) Subscribe(ctx context.Context, sectorID domain.SectorID, playerID domain.PlayerID) (*sector.Subscription, func(), error) {
	return r.w.Subscribe(ctx, sectorID, playerID)
}
func (r multiSectorRouter) LookupShipSector(shipID domain.ShipID) (domain.SectorID, bool) {
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
func (r multiSectorRouter) LookupPrimaryShipByPlayer(playerID domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
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
