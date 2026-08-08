package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
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

	mu   sync.Mutex // guards sent: the concurrency test drives two requests at once
	sent []sector.Command
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
	f.mu.Lock()
	f.sent = append(f.sent, cmd)
	f.mu.Unlock()
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
	// gate, when set, runs inside the spawn — the concurrency test uses it to
	// hold every caller until they have all spawned, reproducing the real
	// window (the spawner waits for a worker ack) instead of hoping for a
	// scheduling coincidence.
	gate func()

	mu    sync.Mutex
	calls []suitCall
	// spawned mirrors the DB rows the spawner inserted — they exist before the
	// worker republishes its snapshot, so fakeEvaShips can answer for them.
	spawned map[domain.ShipID]bool
}

func (f *fakeEvaSuits) SpawnSpacesuit(_ context.Context, p domain.PlayerID, s domain.SectorID, pos domain.Vec2, docked *domain.EntityRef) (domain.ShipID, error) {
	f.mu.Lock()
	// Each spawn is a fresh row, exactly like the INSERT ... RETURNING id it
	// stands for.
	id := f.nextID + domain.ShipID(len(f.calls))
	f.calls = append(f.calls, suitCall{p, s, pos, docked})
	if f.spawned == nil {
		f.spawned = map[domain.ShipID]bool{}
	}
	f.spawned[id] = true
	f.mu.Unlock()
	if f.gate != nil {
		f.gate()
	}
	return id, nil
}

func (f *fakeEvaSuits) spawnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeEvaSuits) isSpawned(id domain.ShipID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.spawned[id]
}

// fakeEvaPlayers stands in for the players table. The mutex is the row lock,
// not a way to serialize the handler: every method takes it for the duration of
// one statement only, so two concurrent requests interleave freely everywhere
// except inside a single compare-and-set — which is exactly what Postgres does.
type fakeEvaPlayers struct {
	mu        sync.Mutex
	active    map[domain.PlayerID]domain.ShipID
	passenger map[domain.PlayerID]domain.ShipID
	// injected failures for the compensation paths (TASK-194)
	setActiveErr error
	setHostErr   error
}

func newFakeEvaPlayers() *fakeEvaPlayers {
	return &fakeEvaPlayers{active: map[domain.PlayerID]domain.ShipID{}, passenger: map[domain.PlayerID]domain.ShipID{}}
}

func (f *fakeEvaPlayers) ActiveShip(_ context.Context, p domain.PlayerID) (domain.ShipID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.active[p]
	return id, ok && id != 0, nil
}

func (f *fakeEvaPlayers) SetActiveShip(_ context.Context, p domain.PlayerID, id domain.ShipID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setActiveErr != nil {
		return f.setActiveErr
	}
	f.active[p] = id
	return nil
}

func (f *fakeEvaPlayers) CompareAndSetActiveShip(_ context.Context, p domain.PlayerID, expected, next domain.ShipID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setActiveErr != nil {
		return false, f.setActiveErr
	}
	if f.active[p] != expected {
		return false, nil
	}
	f.active[p] = next
	return true, nil
}

func (f *fakeEvaPlayers) PassengerHost(_ context.Context, p domain.PlayerID) (domain.ShipID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.passenger[p]
	return id, ok && id != 0, nil
}

func (f *fakeEvaPlayers) SetPassengerHost(_ context.Context, p domain.PlayerID, host domain.ShipID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setHostErr != nil {
		return f.setHostErr
	}
	f.passenger[p] = host
	return nil
}

// activeShip reads the pointer for assertions.
func (f *fakeEvaPlayers) activeShip(p domain.PlayerID) domain.ShipID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[p]
}

// fakeEvaShips answers is_spacesuit from the DB's point of view: the ships the
// pool knows about, plus dbOnly rows that exist in the DB but have not reached
// a worker snapshot yet (a suit spawned within the current tick).
type fakeEvaShips struct {
	pool   *fakeEvaPool
	suits  *fakeEvaSuits
	dbOnly map[domain.ShipID]bool
	err    error
}

func (f *fakeEvaShips) IsSpacesuit(_ context.Context, id domain.ShipID) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if suit, ok := f.dbOnly[id]; ok {
		return suit, nil
	}
	if f.suits != nil && f.suits.isSpawned(id) {
		return true, nil
	}
	if f.pool != nil {
		for _, ships := range f.pool.ships {
			for _, sh := range ships {
				if sh.ID == id {
					return sh.IsSpacesuit, nil
				}
			}
		}
	}
	return false, errors.New("ship not found")
}

type fakeEvaBus struct{ topics []string }

func (f *fakeEvaBus) Publish(_ context.Context, topic string, _ []byte) error {
	f.topics = append(f.topics, topic)
	return nil
}

// --- harness ---------------------------------------------------------------

const testNPC = domain.PlayerID(1)

func newEvaTest(pool *fakeEvaPool, suits *fakeEvaSuits, players *fakeEvaPlayers, bus *fakeEvaBus) *evaServer {
	return newEvaServer(pool, suits, players, &fakeEvaShips{pool: pool, suits: suits}, bus, testNPC, EVAConfig{}, slog.New(slog.DiscardHandler))
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

// TASK-194: being already outside a hull is not an error — the exit the player
// asked for has happened, so answer 200 with the suit they are already in and
// do not mint a second one.
func TestUnit_Eva_ExitShip_FromSuit_Idempotent(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 42, PlayerID: player, SectorID: 2, IsSpacesuit: true}}},
	}
	suits := &fakeEvaSuits{}
	srv := newEvaTest(pool, suits, newFakeEvaPlayers(), &fakeEvaBus{})

	rr := doExit(t, srv, player, 42)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, int64(42), decodeExit(t, rr).ShipID, "the suit already worn is returned")
	assert.Empty(t, suits.calls, "no second spacesuit spawned")
}

// TASK-194: the lost-ack retry. The SPA still shows the abandoned ship and
// re-posts its id; the player is already in suit 99, so the answer is 200 with
// 99 and nothing is created.
func TestUnit_Eva_ExitShip_RepeatAfterLostAck_SameSuit_NoSecondRow(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2, 99: 2},
		ships: map[domain.SectorID][]domain.Ship{2: {
			{ID: 42, PlayerID: player, SectorID: 2},                    // the ship they stepped out of
			{ID: 99, PlayerID: player, SectorID: 2, IsSpacesuit: true}, // the suit from the first call
		}},
	}
	suits := &fakeEvaSuits{nextID: 100}
	players := newFakeEvaPlayers()
	players.active[player] = 99
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	for i := range 3 {
		rr := doExit(t, srv, player, 42)
		require.Equalf(t, http.StatusOK, rr.Code, "retry %d: %s", i, rr.Body.String())
		assert.Equal(t, int64(99), decodeExit(t, rr).ShipID, "every retry answers the same suit")
	}
	assert.Empty(t, suits.calls, "no spacesuit row created by the retries")
	assert.Equal(t, domain.ShipID(99), players.active[player])
}

// TASK-194: the same retry inside the tick the first exit landed in. The suit
// row exists (active_ship_id points at it) but the worker has not republished
// its snapshot yet, so the suit is invisible in RAM — the case that made the
// live run mint three suits from three back-to-back clicks.
func TestUnit_Eva_ExitShip_RepeatBeforeSnapshotRepublish_SameSuit(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 42, PlayerID: player, SectorID: 2}}},
	}
	suits := &fakeEvaSuits{nextID: 100}
	players := newFakeEvaPlayers()
	players.active[player] = 99 // spawned this tick, not in any snapshot yet
	srv := newEvaServer(pool, suits, players,
		&fakeEvaShips{pool: pool, suits: suits, dbOnly: map[domain.ShipID]bool{99: true}},
		&fakeEvaBus{}, testNPC, EVAConfig{}, slog.New(slog.DiscardHandler))

	rr := doExit(t, srv, player, 42)

	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, int64(99), decodeExit(t, rr).ShipID)
	assert.Empty(t, suits.calls, "no second suit while the snapshot is stale")
}

// TASK-194: two exit-ship requests genuinely running at the same time (the
// double-click). Both get past the "not in a suit" read and both spawn — the
// gate that has to hold is the conditional write on active_ship_id: exactly one
// claims the pointer, the loser deletes its own suit and reports the winner's.
// The spawner gate holds both callers until they have both spawned, so the
// overlap is forced rather than hoped for.
func TestUnit_Eva_ExitShip_ConcurrentRequests_OneSuitSurvives(t *testing.T) {
	t.Parallel()
	const (
		player   = domain.PlayerID(100)
		attempts = 2
	)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 42, PlayerID: player, SectorID: 2}}},
	}
	var spawnedAll sync.WaitGroup
	spawnedAll.Add(attempts)
	suits := &fakeEvaSuits{nextID: 90, gate: func() {
		spawnedAll.Done()
		spawnedAll.Wait()
	}}
	players := newFakeEvaPlayers()
	players.active[player] = 42
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	codes := make([]int, attempts)
	bodies := make([]int64, attempts)
	var running sync.WaitGroup
	running.Add(attempts)
	for i := range attempts {
		go func() {
			defer running.Done()
			rr := doExit(t, srv, player, 42)
			codes[i] = rr.Code
			var resp exitShipResponse
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			bodies[i] = resp.ShipID
		}()
	}
	running.Wait()

	winner := players.activeShip(player)
	assert.Equal(t, attempts, suits.spawnCount(), "both attempts raced past the read, as they do live")
	for i := range attempts {
		assert.Equalf(t, http.StatusOK, codes[i], "attempt %d", i)
		assert.Equalf(t, int64(winner), bodies[i], "attempt %d reports the suit that actually stuck", i)
	}
	// Every suit except the winner must have been removed again.
	removed := 0
	for _, c := range pool.sent {
		if rc, ok := c.(sector.RemoveShipCommand); ok {
			assert.NotEqual(t, winner, rc.ShipID, "the winning suit is never removed")
			removed++
		}
	}
	assert.Equal(t, attempts-1, removed, "exactly the losing suits are rolled back")
	assert.True(t, winner == 90 || winner == 91, "the winner is one of the spawned suits, got %d", winner)
}

// TASK-194 / AC-7: exiting a ship the player owns but does not fly is the
// teleport hole — the suit would spawn at that ship's position, in another
// sector, and become active.
func TestUnit_Eva_ExitShip_NotTheFlownShip_Rejected(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 1, 43: 40},
		ships: map[domain.SectorID][]domain.Ship{
			1:  {{ID: 42, PlayerID: player, SectorID: 1}},
			40: {{ID: 43, PlayerID: player, SectorID: 40, Pos: domain.Vec2{X: 111, Y: 222}}},
		},
	}
	suits := &fakeEvaSuits{nextID: 77}
	players := newFakeEvaPlayers()
	players.active[player] = 42
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	rr := doExit(t, srv, player, 43)

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Empty(t, suits.calls, "no suit spawned in the remote sector")
	assert.Equal(t, domain.ShipID(42), players.active[player], "still flying the same ship")
}

// TASK-194: active_ship_id is NULL for accounts that never switched ships; the
// min-id fallback keeps their exit legal. Ship 42 is the min-id one here, 43 is
// the parked second hull.
func TestUnit_Eva_ExitShip_NoActiveShipID_MinIDFallback(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 1, 43: 40},
		ships: map[domain.SectorID][]domain.Ship{
			1:  {{ID: 42, PlayerID: player, SectorID: 1}},
			40: {{ID: 43, PlayerID: player, SectorID: 40}},
		},
	}
	suits := &fakeEvaSuits{nextID: 77}
	srv := newEvaTest(pool, suits, newFakeEvaPlayers(), &fakeEvaBus{}) // no active_ship_id at all

	require.Equal(t, http.StatusOK, doExit(t, srv, player, 42).Code)
	require.Len(t, suits.calls, 1)
	assert.Equal(t, domain.SectorID(1), suits.calls[0].sector)

	// the parked hull is refused for the same account (fresh server: the player
	// is back in ship 42, still without an explicit active_ship_id)
	parkedSuits := &fakeEvaSuits{nextID: 78}
	parked := newEvaTest(pool, parkedSuits, newFakeEvaPlayers(), &fakeEvaBus{})
	assert.Equal(t, http.StatusConflict, doExit(t, parked, player, 43).Code)
	assert.Empty(t, parkedSuits.calls)
}

// TASK-194: a passenger flies nothing of their own — exit-ship must not resolve
// them onto a hull they left parked elsewhere. Their way out is disembark.
func TestUnit_Eva_ExitShip_Passenger_Rejected(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 1, 70: 3},
		ships: map[domain.SectorID][]domain.Ship{
			1: {{ID: 42, PlayerID: player, SectorID: 1}},
			3: {{ID: 70, PlayerID: testNPC, SectorID: 3}},
		},
	}
	suits := &fakeEvaSuits{nextID: 77}
	players := newFakeEvaPlayers()
	players.passenger[player] = 70
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	rr := doExit(t, srv, player, 42)

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Empty(t, suits.calls)
}

// TASK-194: a failed active_ship_id write must not leave the suit behind — the
// retry would otherwise mint another one.
func TestUnit_Eva_ExitShip_SetActiveFails_SuitRemoved(t *testing.T) {
	t.Parallel()
	const player = domain.PlayerID(100)
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{42: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 42, PlayerID: player, SectorID: 2}}},
	}
	suits := &fakeEvaSuits{nextID: 99}
	players := newFakeEvaPlayers()
	players.active[player] = 42
	players.setActiveErr = errors.New("db down")
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	rr := doExit(t, srv, player, 42)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.True(t, shipRemoved(pool, 99), "the spawned suit is rolled back")
}

func decodeExit(t *testing.T, rr *httptest.ResponseRecorder) exitShipResponse {
	t.Helper()
	var resp exitShipResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	return resp
}

// shipRemoved reports whether a RemoveShipCommand for shipID reached the pool.
func shipRemoved(pool *fakeEvaPool, shipID domain.ShipID) bool {
	for _, c := range pool.sent {
		if rc, ok := c.(sector.RemoveShipCommand); ok && rc.ShipID == shipID {
			return true
		}
	}
	return false
}

// The caller flies their own ship 41 and names someone else's hull. (Since
// TASK-194 the controlled ship is resolved first, so a caller with no ship at
// all is answered «нет активного корабля» before ownership is even looked at —
// there is nothing for them to exit either way.)
func TestUnit_Eva_ExitShip_OtherPlayer_Forbidden(t *testing.T) {
	t.Parallel()
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{41: 2, 42: 2},
		ships: map[domain.SectorID][]domain.Ship{2: {
			{ID: 41, PlayerID: 100, SectorID: 2},
			{ID: 42, PlayerID: 999, SectorID: 2},
		}},
	}
	suits := &fakeEvaSuits{}
	srv := newEvaTest(pool, suits, newFakeEvaPlayers(), &fakeEvaBus{})

	rr := doExit(t, srv, domain.PlayerID(100), 42)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Empty(t, suits.calls)
}

func TestUnit_Eva_ExitShip_Unknown_NotFound(t *testing.T) {
	t.Parallel()
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{41: 2},
		ships:      map[domain.SectorID][]domain.Ship{2: {{ID: 41, PlayerID: 100, SectorID: 2}}},
	}
	srv := newEvaTest(pool, &fakeEvaSuits{}, newFakeEvaPlayers(), &fakeEvaBus{})
	rr := doExit(t, srv, domain.PlayerID(100), 12345)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TASK-194: a caller with no ship in the world at all is refused before the
// ownership lookup — the resolution has nothing to hand back.
func TestUnit_Eva_ExitShip_NoShipAtAll_Conflict(t *testing.T) {
	t.Parallel()
	suits := &fakeEvaSuits{}
	srv := newEvaTest(&fakeEvaPool{}, suits, newFakeEvaPlayers(), &fakeEvaBus{})
	rr := doExit(t, srv, domain.PlayerID(100), 12345)
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Empty(t, suits.calls)
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

// disembarkScene wires player 100 riding NPC host 70 in sector 3.
func disembarkScene() (*fakeEvaPool, *fakeEvaPlayers, *fakeEvaSuits) {
	host := domain.Ship{ID: 70, PlayerID: testNPC, SectorID: 3, Pos: domain.Vec2{X: 4, Y: 5}, PassengerPlayers: []domain.PlayerID{100}}
	pool := &fakeEvaPool{
		shipSector: map[domain.ShipID]domain.SectorID{70: 3},
		ships:      map[domain.SectorID][]domain.Ship{3: {host}},
	}
	players := newFakeEvaPlayers()
	players.passenger[100] = 70
	return pool, players, &fakeEvaSuits{nextID: 88}
}

// TASK-194: two concurrent disembarks of the same rider — same conditional
// write, same outcome: one suit survives, both callers are told which.
func TestUnit_Eva_Disembark_ConcurrentRequests_OneSuitSurvives(t *testing.T) {
	t.Parallel()
	const attempts = 2
	pool, players, suits := disembarkScene()
	var spawnedAll sync.WaitGroup
	spawnedAll.Add(attempts)
	suits.gate = func() {
		spawnedAll.Done()
		spawnedAll.Wait()
	}
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	codes := make([]int, attempts)
	bodies := make([]int64, attempts)
	var running sync.WaitGroup
	running.Add(attempts)
	for i := range attempts {
		go func() {
			defer running.Done()
			rr := doDisembark(t, srv, 100)
			codes[i] = rr.Code
			var resp disembarkResponse
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			bodies[i] = resp.ShipID
		}()
	}
	running.Wait()

	winner := players.activeShip(100)
	assert.Equal(t, attempts, suits.spawnCount())
	for i := range attempts {
		assert.Equalf(t, http.StatusOK, codes[i], "attempt %d", i)
		assert.Equalf(t, int64(winner), bodies[i], "attempt %d reports the surviving suit", i)
	}
	removed := 0
	for _, c := range pool.sent {
		if rc, ok := c.(sector.RemoveShipCommand); ok {
			assert.NotEqual(t, winner, rc.ShipID)
			removed++
		}
	}
	assert.Equal(t, attempts-1, removed, "exactly the losing suits are rolled back")
}

// TASK-194 / AC-2: the old code logged the failed SetPassengerHost and still
// answered 200, leaving the player a passenger with a stray suit in the world.
func TestUnit_Eva_Disembark_ClearHostFails_NotOK(t *testing.T) {
	t.Parallel()
	pool, players, suits := disembarkScene()
	players.setHostErr = errors.New("db down")
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	rr := doDisembark(t, srv, 100)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, domain.ShipID(70), players.passenger[100], "still riding the host")
	assert.True(t, shipRemoved(pool, 88), "the spawned suit is rolled back")
	// The pointer is put back by hand, not left to the FK's ON DELETE SET NULL:
	// the remove is fire-and-forget and the worker only logs a failed row delete.
	assert.Equal(t, domain.ShipID(0), players.activeShip(100), "active_ship_id restored to the passenger's NULL")
}

// TASK-194: same for the active_ship_id write, which runs before the passenger
// link is cleared, so the rollback restores the exact pre-call state.
func TestUnit_Eva_Disembark_SetActiveFails_NotOK(t *testing.T) {
	t.Parallel()
	pool, players, suits := disembarkScene()
	players.setActiveErr = errors.New("db down")
	srv := newEvaTest(pool, suits, players, &fakeEvaBus{})

	rr := doDisembark(t, srv, 100)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, domain.ShipID(70), players.passenger[100], "still riding the host")
	assert.True(t, shipRemoved(pool, 88), "the spawned suit is rolled back")
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
