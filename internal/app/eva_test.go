package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

// --- fakes -----------------------------------------------------------------

type fakeEvaPool struct {
	shipSector map[domain.ShipID]domain.SectorID
	ships      map[domain.SectorID][]domain.Ship
	sent       []sector.Command
}

func (f *fakeEvaPool) LookupShipSector(id domain.ShipID) (domain.SectorID, bool) {
	s, ok := f.shipSector[id]
	return s, ok
}

func (f *fakeEvaPool) LookupPrimaryShipByPlayer(player domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	var best domain.ShipID
	var bestSec domain.SectorID
	set := false
	for sec, ships := range f.ships {
		for _, sh := range ships {
			if sh.PlayerID == player && (!set || sh.ID < best) {
				best, bestSec, set = sh.ID, sec, true
			}
		}
	}
	return best, bestSec, set
}

func (f *fakeEvaPool) Snapshot(sec domain.SectorID) sector.Snapshot {
	return sector.Snapshot{Ships: f.ships[sec]}
}

func (f *fakeEvaPool) Send(_ domain.SectorID, cmd sector.Command) error {
	f.sent = append(f.sent, cmd)
	// Reply to commands that carry a reply channel so callers don't block.
	switch c := cmd.(type) {
	case sector.RemoveShipCommand:
		if c.Reply != nil {
			c.Reply <- sector.CmdResult{}
		}
	case sector.AddPassengerCommand:
		if c.Reply != nil {
			c.Reply <- sector.CmdResult{}
		}
	case sector.RemovePassengerCommand:
		if c.Reply != nil {
			c.Reply <- sector.CmdResult{}
		}
	}
	return nil
}

type suitCall struct {
	player domain.PlayerID
	sector domain.SectorID
	pos    domain.Vec2
	docked *domain.EntityRef
}

type fakeEvaSuits struct {
	nextID domain.ShipID
	calls  []suitCall
}

func (f *fakeEvaSuits) SpawnSpacesuit(_ context.Context, p domain.PlayerID, s domain.SectorID, pos domain.Vec2, docked *domain.EntityRef) (domain.ShipID, error) {
	f.calls = append(f.calls, suitCall{p, s, pos, docked})
	return f.nextID, nil
}

type fakeEvaPlayers struct {
	active    map[domain.PlayerID]domain.ShipID
	passenger map[domain.PlayerID]domain.ShipID
}

func newFakeEvaPlayers() *fakeEvaPlayers {
	return &fakeEvaPlayers{active: map[domain.PlayerID]domain.ShipID{}, passenger: map[domain.PlayerID]domain.ShipID{}}
}

func (f *fakeEvaPlayers) ActiveShip(_ context.Context, p domain.PlayerID) (domain.ShipID, bool, error) {
	id, ok := f.active[p]
	return id, ok && id != 0, nil
}

func (f *fakeEvaPlayers) SetActiveShip(_ context.Context, p domain.PlayerID, id domain.ShipID) error {
	f.active[p] = id
	return nil
}

func (f *fakeEvaPlayers) PassengerHost(_ context.Context, p domain.PlayerID) (domain.ShipID, bool, error) {
	id, ok := f.passenger[p]
	return id, ok && id != 0, nil
}

func (f *fakeEvaPlayers) SetPassengerHost(_ context.Context, p domain.PlayerID, host domain.ShipID) error {
	f.passenger[p] = host
	return nil
}

type fakeEvaBus struct{ topics []string }

func (f *fakeEvaBus) Publish(_ context.Context, topic string, _ []byte) error {
	f.topics = append(f.topics, topic)
	return nil
}

// --- harness ---------------------------------------------------------------

const testNPC = domain.PlayerID(1)

func newEvaTest(pool *fakeEvaPool, suits *fakeEvaSuits, players *fakeEvaPlayers, bus *fakeEvaBus) *evaServer {
	return newEvaServer(pool, suits, players, bus, testNPC, EVAConfig{}, slog.New(slog.DiscardHandler))
}

func doExit(t *testing.T, srv *evaServer, player domain.PlayerID, shipID int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(exitShipRequest{ShipID: shipID})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/exit-ship", bytes.NewReader(body))
	req = req.WithContext(auth.ContextWithPlayerID(req.Context(), player))
	rr := httptest.NewRecorder()
	srv.handleExitShip(rr, req)
	return rr
}

// --- tests -----------------------------------------------------------------

func TestUnit_Eva_ExitShip_InDock_SuitInheritsDock(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	dock := &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 3},
		ships: map[domain.SectorID][]domain.Ship{3: {{
			ID: 42, PlayerID: player, SectorID: 3, Pos: domain.Vec2{X: 7, Y: 8}, Docked: dock,
		}}},
	}
	suits := &fakeEvaSuits{nextID: 99}
	players := newFakeEvaPlayers()
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	rr := doExit(t, srv, player, 42)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Len(t, suits.calls, 1)
	assert.Equal(t, domain.SectorID(3), suits.calls[0].sector)
	assert.Equal(t, domain.Vec2{X: 7, Y: 8}, suits.calls[0].pos)
	require.NotNil(t, suits.calls[0].docked)
	assert.Equal(t, *dock, *suits.calls[0].docked, "suit stays docked at the same station")
	assert.Equal(t, domain.ShipID(99), players.active[player], "spacesuit becomes active")
}

func TestUnit_Eva_ExitShip_InSpace_SuitNotDocked(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 42, PlayerID: player, SectorID: 2, Pos: domain.Vec2{X: 1, Y: 1}}}},
	}
	suits := &fakeEvaSuits{nextID: 77}
	srv := newEvaTest(pool, suits, newFakeEvaPlayers(), &fakeEvaBus{})

	rr := doExit(t, srv, player, 42)

	require.Equal(t, http.StatusOK, rr.Code)
	require.Len(t, suits.calls, 1)
	assert.Nil(t, suits.calls[0].docked, "suit in space is not docked")
}

func TestUnit_Eva_ExitShip_FromSuit_Rejected(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 42, PlayerID: player, SectorID: 2, IsSpacesuit: true}}},
	}
	suits := &fakeEvaSuits{}
	srv := newEvaTest(pool, suits, newFakeEvaPlayers(), &fakeEvaBus{})

	rr := doExit(t, srv, player, 42)

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Empty(t, suits.calls)
}

func TestUnit_Eva_ExitShip_OtherPlayer_Forbidden(t *testing.T) {
	t.Parallel()
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 42, PlayerID: 999, SectorID: 2}}},
	}
	suits := &fakeEvaSuits{}
	srv := newEvaTest(pool, suits, newFakeEvaPlayers(), &fakeEvaBus{})

	rr := doExit(t, srv, domain.PlayerID(100), 42)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Empty(t, suits.calls)
}

func TestUnit_Eva_ExitShip_Unknown_NotFound(t *testing.T) {
	t.Parallel()
	srv := newEvaTest(&fakeEvaPool{}, &fakeEvaSuits{}, newFakeEvaPlayers(), &fakeEvaBus{})
	rr := doExit(t, srv, domain.PlayerID(100), 12345)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// --- board ------------------------------------------------------------------

func doBoard(t *testing.T, srv *evaServer, player domain.PlayerID, targetID int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(boardShipRequest{TargetShipID: targetID})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/board-ship", bytes.NewReader(body))
	req = req.WithContext(auth.ContextWithPlayerID(req.Context(), player))
	rr := httptest.NewRecorder()
	srv.handleBoardShip(rr, req)
	return rr
}

// boardScene wires a player-100 spacesuit (id 50) docked at station 5 in sector
// 3, plus the given target ship in the same dock. active_ship_id = the suit.
func boardScene(target domain.Ship) (*fakeEvaPool, *fakeEvaPlayers, *fakeEvaBus) {
	dock := &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	suit := domain.Ship{ID: 50, PlayerID: 100, SectorID: 3, IsSpacesuit: true, Docked: dock}
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{50: 3, target.ID: target.SectorID},
		ships:      map[domain.SectorID][]domain.Ship{3: {suit, target}},
	}
	players := newFakeEvaPlayers()
	players.active[100] = 50
	return pool, players, &fakeEvaBus{}
}

func dockRef5() *domain.EntityRef { return &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5} }

func TestUnit_Eva_Board_OwnShip_TakesControl(t *testing.T) {
	t.Parallel()
	pool, players, bus := boardScene(domain.Ship{ID: 60, PlayerID: 100, SectorID: 3, Docked: dockRef5()})
	srv := newEvaTest(pool, &fakeEvaSuits{}, players, bus)

	rr := doBoard(t, srv, 100, 60)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, domain.ShipID(60), players.active[100], "boarded own ship becomes active")
	assert.Equal(t, domain.ShipID(0), players.passenger[100], "not a passenger")
	assert.Contains(t, bus.topics, sector.PlayerHandoffTopic(100))
	// the spacesuit was removed
	removed := false
	for _, c := range pool.sent {
		if rc, ok := c.(sector.RemoveShipCommand); ok && rc.ShipID == 50 {
			removed = true
		}
	}
	assert.True(t, removed, "spacesuit removed on boarding own ship")
}

func TestUnit_Eva_Board_NPCShip_RidesAsPassenger(t *testing.T) {
	t.Parallel()
	pool, players, bus := boardScene(domain.Ship{ID: 70, PlayerID: testNPC, SectorID: 3, Docked: dockRef5()})
	srv := newEvaTest(pool, &fakeEvaSuits{}, players, bus)

	rr := doBoard(t, srv, 100, 70)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, domain.ShipID(0), players.active[100], "passenger has no own ship")
	assert.Equal(t, domain.ShipID(70), players.passenger[100], "rides the NPC host")
	addedPassenger := false
	for _, c := range pool.sent {
		if ap, ok := c.(sector.AddPassengerCommand); ok && ap.HostID == 70 && ap.PlayerID == 100 {
			addedPassenger = true
		}
	}
	assert.True(t, addedPassenger, "registered as passenger of the host")
}

func TestUnit_Eva_Board_OtherOpenShip_RidesAsPassenger(t *testing.T) {
	t.Parallel()
	pool, players, bus := boardScene(domain.Ship{ID: 80, PlayerID: 200, SectorID: 3, Docked: dockRef5(), IsOpen: true})
	srv := newEvaTest(pool, &fakeEvaSuits{}, players, bus)

	rr := doBoard(t, srv, 100, 80)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, domain.ShipID(80), players.passenger[100])
}

func TestUnit_Eva_Board_OtherClosedShip_Forbidden(t *testing.T) {
	t.Parallel()
	pool, players, bus := boardScene(domain.Ship{ID: 80, PlayerID: 200, SectorID: 3, Docked: dockRef5(), IsOpen: false})
	srv := newEvaTest(pool, &fakeEvaSuits{}, players, bus)

	rr := doBoard(t, srv, 100, 80)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, domain.ShipID(0), players.passenger[100])
}

func TestUnit_Eva_Board_NotInSuit_Rejected(t *testing.T) {
	t.Parallel()
	// active ship is a real ship, not a spacesuit.
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{50: 3, 60: 3},
		ships: map[domain.SectorID][]domain.Ship{3: {
			{ID: 50, PlayerID: 100, SectorID: 3}, // not a spacesuit
			{ID: 60, PlayerID: testNPC, SectorID: 3},
		}},
	}
	players := newFakeEvaPlayers()
	players.active[100] = 50
	srv := newEvaTest(pool, &fakeEvaSuits{}, players, &fakeEvaBus{})

	rr := doBoard(t, srv, 100, 60)
	assert.Equal(t, http.StatusConflict, rr.Code)
}

// --- disembark --------------------------------------------------------------

func doDisembark(t *testing.T, srv *evaServer, player domain.PlayerID) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/disembark", nil)
	req = req.WithContext(auth.ContextWithPlayerID(req.Context(), player))
	rr := httptest.NewRecorder()
	srv.handleDisembark(rr, req)
	return rr
}

func TestUnit_Eva_Disembark_SpawnsSuitAtHost(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	dock := &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	host := domain.Ship{ID: 70, PlayerID: testNPC, SectorID: 3, Pos: domain.Vec2{X: 4, Y: 5}, Docked: dock, PassengerPlayers: []domain.PlayerID{player}}
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{70: 3},
		ships:      map[domain.SectorID][]domain.Ship{3: {host}},
	}
	players := newFakeEvaPlayers()
	players.passenger[player] = 70
	suits := &fakeEvaSuits{nextID: 88}
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	rr := doDisembark(t, srv, player)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Len(t, suits.calls, 1)
	assert.Equal(t, domain.SectorID(3), suits.calls[0].sector)
	assert.Equal(t, domain.Vec2{X: 4, Y: 5}, suits.calls[0].pos)
	require.NotNil(t, suits.calls[0].docked)
	assert.Equal(t, *dock, *suits.calls[0].docked, "suit lands in the host's hangar")
	assert.Equal(t, domain.ShipID(88), players.active[player], "suit becomes active")
	assert.Equal(t, domain.ShipID(0), players.passenger[player], "passenger link cleared")
	removed := false
	for _, c := range pool.sent {
		if rp, ok := c.(sector.RemovePassengerCommand); ok && rp.HostID == 70 && rp.PlayerID == player {
			removed = true
		}
	}
	assert.True(t, removed, "removed from host passenger mirror")
}

func TestUnit_Eva_Disembark_NotPassenger_Rejected(t *testing.T) {
	t.Parallel()
	srv := newEvaTest(&fakeEvaPool{}, &fakeEvaSuits{}, newFakeEvaPlayers(), &fakeEvaBus{})
	rr := doDisembark(t, srv, domain.PlayerID(100))
	assert.Equal(t, http.StatusConflict, rr.Code)
}

func TestUnit_Eva_Board_TooFar_Rejected(t *testing.T) {
	t.Parallel()
	// suit docked at station 5; target docked at a different station 9.
	suit := domain.Ship{ID: 50, PlayerID: 100, SectorID: 3, IsSpacesuit: true, Docked: dockRef5()}
	target := domain.Ship{ID: 60, PlayerID: testNPC, SectorID: 3, Docked: &domain.EntityRef{Kind: domain.EntityKindStation, ID: 9}}
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{50: 3, 60: 3},
		ships:      map[domain.SectorID][]domain.Ship{3: {suit, target}},
	}
	players := newFakeEvaPlayers()
	players.active[100] = 50
	srv := newEvaTest(pool, &fakeEvaSuits{}, players, &fakeEvaBus{})

	rr := doBoard(t, srv, 100, 60)
	assert.Equal(t, http.StatusConflict, rr.Code)
}
