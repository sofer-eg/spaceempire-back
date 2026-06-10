package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
)

func TestUnit_WS_PushesDeltaSnapshots(t *testing.T) {
	t.Parallel()

	target := domain.Vec2{X: 50, Y: 0}
	// Per-tick kinematics (phase 3.18): MaxSpeed=5 means ~10 ticks to
	// arrive, so the broadcaster has time to emit Updated patches before
	// the ship snaps. Pre-3.18 fixtures used MaxSpeed=100 because dt=10ms
	// scaled it down to 1 unit/tick; under the SP-style port that value
	// would arrive in a single tick and the patch stream would dry up.
	initial := []domain.Ship{{ID: 1, Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 5, Target: &target}}
	srv, w := newTestServer(t, initial)
	runWorker(t, w)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	// The server always opens with a statics welcome frame carrying the
	// tick interval; the delta snapshot stream follows.
	readStatics(t, ctx, conn)

	// First snapshot after subscribe must carry the current world in Added.
	first := readSnapshot(t, ctx, conn)
	if first.Type != "snapshot" {
		t.Fatalf("Type = %q, want snapshot", first.Type)
	}
	if len(first.Added) != 1 || first.Added[0].ID != 1 {
		t.Fatalf("first.Added = %+v, want one ship id=1", first.Added)
	}
	if first.Added[0].PlayerID != 0 {
		t.Fatalf("Added[0].PlayerID = %d, want 0 (default)", first.Added[0].PlayerID)
	}

	// Subsequent snapshots must arrive as updates as the ship moves.
	deadline := time.Now().Add(2 * time.Second)
	var update dto.Snapshot
	for time.Now().Before(deadline) {
		s := readSnapshot(t, ctx, conn)
		if s.Tick != first.Tick && (len(s.Updated) > 0 || len(s.Removed) > 0) {
			update = s
			break
		}
	}
	if update.Tick == 0 {
		t.Fatal("never observed an Update patch following the initial Added")
	}
	if len(update.Added) != 0 {
		t.Fatalf("update.Added = %+v, want empty (ship was already in Added)", update.Added)
	}
}

func TestUnit_WS_ClientCloseTerminatesServer(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, nil)
	runWorker(t, w)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}

	_ = conn.Close(websocket.StatusNormalClosure, "bye")
	// No assertion required: the handler must return without panicking.
	// httptest.Close on defer would hang if WS handlers leaked.
}

func readSnapshot(t *testing.T, ctx context.Context, c *websocket.Conn) dto.Snapshot {
	t.Helper()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var snap dto.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, string(data))
	}
	return snap
}

func readStatics(t *testing.T, ctx context.Context, c *websocket.Conn) dto.StaticsMessage {
	t.Helper()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("ws read statics: %v", err)
	}
	var msg dto.StaticsMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode statics: %v (raw=%q)", err, string(data))
	}
	if msg.Type != "statics" {
		t.Fatalf("Type = %q, want statics (raw=%q)", msg.Type, string(data))
	}
	return msg
}

func TestUnit_WS_WelcomeIncludesTickIntervalMs(t *testing.T) {
	t.Parallel()

	srv, w := newTestServer(t, nil)
	runWorker(t, w)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	welcome := readStatics(t, ctx, conn)
	// newTestServer configures SnapshotInterval = 10ms, which the welcome
	// must echo back so the SPA can size its interpolation step.
	if welcome.TickIntervalMs != 10 {
		t.Fatalf("TickIntervalMs = %d, want 10", welcome.TickIntervalMs)
	}
}
