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
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// chargingStation is a station whose live combat state provably diverges from
// the layout the client is handed: it spawns with an empty shield and a
// recharge equal to its maximum, so one engine tick lifts the live shield to
// 500 while the immutable layout keeps saying 0. Damage would produce the same
// divergence in HP (and is what the player actually sees, TASK-186), but the
// shield reaches it without a fight — and through the same map,
// sectorState.destructibles, that TakeDamage writes to.
func chargingStation(id domain.StationID, sectorID domain.SectorID) domain.Station {
	return domain.Station{
		ID:             id,
		SectorID:       sectorID,
		Type:           7,
		Pos:            domain.Vec2{X: 100, Y: 200},
		HP:             7500,
		Shield:         0,
		MaxShield:      500,
		ShieldRecharge: 500,
		Built:          true,
	}
}

// waitForLiveShield blocks until the worker's published snapshot shows the
// static's live shield charged, i.e. until live state and layout disagree.
func waitForLiveShield(t *testing.T, w *sector.Worker, sectorID domain.SectorID, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, d := range w.Snapshot(sectorID).Destructibles {
			if d.Shield == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("static shield never reached %d in sector %d", want, int64(sectorID))
}

// TestUnit_WS_WelcomeCarriesLiveStaticCombat is the AC#1/AC#2 regression: a
// freshly-opened socket — which is what a page reload is — must carry the
// sector's live static combat state, not only the spawn layout. Before
// TASK-186 the welcome frame held cloneStatics(s.statics) alone, so a client
// that reconnected after a fight read the spawn figures until something
// damaged or recharged the object again.
func TestUnit_WS_WelcomeCarriesLiveStaticCombat(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: nil},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			1: {Stations: []domain.Station{chargingStation(11, 1)}},
		}),
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	runWorker(t, w)
	waitForLiveShield(t, w, 1, 500)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	welcome := readStatics(t, ctx, conn)

	require.Len(t, welcome.Destructibles, 1, "welcome must carry the sector's live static combat state")
	live := welcome.Destructibles[0]
	assert.Equal(t, int(domain.EntityKindStation), live.Ref.Kind)
	assert.Equal(t, int64(11), live.Ref.ID)
	assert.Equal(t, 500, live.Shield, "live shield must be the charged value, not the spawn 0")
	assert.Equal(t, 500, live.MaxShield)
	assert.Equal(t, 7500, live.HP)

	// The layout still carries the spawn figures — and must, because AC#4 uses
	// its hp as the de-facto maximum for the hull bar.
	require.Len(t, welcome.Statics.Stations, 1)
	assert.Equal(t, 0, welcome.Statics.Stations[0].Shield)
	assert.Equal(t, 7500, welcome.Statics.Stations[0].HP)
}

// TestUnit_WS_WelcomeOmitsStaticsWithoutLiveState pins which of the two sets
// the welcome combat state is built from. The fixture is deliberately skewed —
// station 12 sits in the layout with no live record, which killStatic never
// leaves behind (it drops the object from both) — because that is the only way
// to tell "read the live map" from "read the layout" in one assertion. A
// destroyed static must not come back as a live one.
func TestUnit_WS_WelcomeOmitsStaticsWithoutLiveState(t *testing.T) {
	t.Parallel()

	router := staticCombatRouter{snap: sector.Snapshot{
		SectorID: 1,
		Statics: domain.SectorStatics{Stations: []domain.Station{
			{ID: 11, SectorID: 1, Type: 7, HP: 7500, Built: true},
			{ID: 12, SectorID: 1, Type: 7, HP: 7500, Built: true},
		}},
		Destructibles: []domain.DestructibleStatic{
			{Ref: domain.EntityRef{Kind: domain.EntityKindStation, ID: 11}, HP: 3000},
		},
	}}
	srv := api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	welcome := readStatics(t, ctx, conn)

	require.Len(t, welcome.Destructibles, 1, "only the static with a live record may ship as live")
	assert.Equal(t, int64(11), welcome.Destructibles[0].Ref.ID)
	assert.Equal(t, 3000, welcome.Destructibles[0].HP)
}

// TestUnit_WS_HandoffWelcomeCarriesLiveStaticCombat covers the second half of
// AC#2: the jump. wsPushLoop re-sends the statics frame on every
// PlayerHandoffEvent, so the frame for the target sector must carry that
// sector's live combat state too — otherwise "jump to the neighbour and back"
// still resets the client to spawn figures.
func TestUnit_WS_HandoffWelcomeCarriesLiveStaticCombat(t *testing.T) {
	t.Parallel()

	const playerID = domain.PlayerID(7)
	b := bus.NewInMemory(8)
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{
			1: {{ID: 42, PlayerID: playerID, SectorID: 1, Pos: domain.Vec2{X: 10, Y: 20}}},
			2: nil,
		},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			2: {Stations: []domain.Station{chargingStation(22, 2)}},
		}),
	)
	srv := api.NewServer(multiSectorRouter{w: w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		AuthMiddleware:   fakePlayerMiddleware(playerID),
		HandoffBus:       b,
	}, nil)
	runWorker(t, w)
	waitForLiveShield(t, w, 2, 500)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	require.Equal(t, int64(1), readStatics(t, ctx, conn).SectorID, "WS must start in sector 1")

	payload, err := json.Marshal(sector.PlayerHandoffEvent{
		PlayerID:     playerID,
		ShipID:       42,
		SourceSector: 1,
		TargetSector: 2,
	})
	require.NoError(t, err)
	require.NoError(t, b.Publish(ctx, sector.PlayerHandoffTopic(playerID), payload))

	// Read until the target sector's statics frame shows up. A read error means
	// the deadline passed with no such frame; fall through to the assertion
	// below so the failure names that rather than the raw context error.
	deadline := time.Now().Add(3 * time.Second)
	var welcome dto.StaticsMessage
	for time.Now().Before(deadline) {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			break
		}
		var probe dto.StaticsMessage
		require.NoError(t, json.Unmarshal(raw, &probe))
		if probe.Type == "statics" && probe.SectorID == 2 {
			welcome = probe
			break
		}
	}
	require.Equal(t, int64(2), welcome.SectorID, "no statics frame for the target sector arrived")
	require.Len(t, welcome.Destructibles, 1, "the post-jump frame must carry the target sector's live combat state")
	assert.Equal(t, int64(22), welcome.Destructibles[0].Ref.ID)
	assert.Equal(t, 500, welcome.Destructibles[0].Shield)
	require.Len(t, welcome.Statics.Stations, 1)
	assert.Equal(t, 0, welcome.Statics.Stations[0].Shield, "layout keeps the spawn figure")
}

// TestUnit_State_CarriesLiveStaticCombat is AC#5: the HTTP snapshot reads the
// same live source as the WS welcome instead of the spawn layout alone.
func TestUnit_State_CarriesLiveStaticCombat(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: nil},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			1: {Stations: []domain.Station{chargingStation(11, 1)}},
		}),
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	w.Tick(context.Background()) // charges the shield and publishes the snapshot

	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	var snap dto.Snapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	require.Len(t, snap.Destructibles, 1, "/api/state must carry live static combat state")
	assert.Equal(t, int64(11), snap.Destructibles[0].Ref.ID)
	assert.Equal(t, 500, snap.Destructibles[0].Shield, "live shield, not the spawn 0")
	require.NotNil(t, snap.Statics)
	require.Len(t, snap.Statics.Stations, 1)
	assert.Equal(t, 0, snap.Statics.Stations[0].Shield)
}

// staticCombatRouter serves one hand-built snapshot to every lookup. Enough for
// the welcome frame, which reads nothing but Snapshot(sectorID); Subscribe
// hands back a subscription with a nil patch channel, so no delta ever follows.
type staticCombatRouter struct{ snap sector.Snapshot }

func (staticCombatRouter) Send(_ domain.SectorID, _ sector.Command) error { return nil }
func (r staticCombatRouter) Snapshot(_ domain.SectorID) sector.Snapshot   { return r.snap }
func (staticCombatRouter) Subscribe(_ context.Context, _ domain.SectorID, _ domain.PlayerID) (*sector.Subscription, func(), error) {
	return &sector.Subscription{SectorID: 1}, func() {}, nil
}
func (staticCombatRouter) LookupShipSector(_ domain.ShipID) (domain.SectorID, bool) {
	return 0, false
}
func (staticCombatRouter) LookupPrimaryShipByPlayer(_ domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	return 0, 0, false
}
