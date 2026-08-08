package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

type spawnSuitCall struct {
	player domain.PlayerID
	sector domain.SectorID
	pos    domain.Vec2
}

type fakeSuitSpawner struct {
	suits    []spawnSuitCall
	respawns []domain.PlayerID
}

// SpawnSpacesuit hands out a distinct id per call (500, 501, …) so callers that
// repoint active_ship_id at the new suit can be asserted on.
func (f *fakeSuitSpawner) SpawnSpacesuit(_ context.Context, p domain.PlayerID, s domain.SectorID, pos domain.Vec2, _ *domain.EntityRef) (domain.ShipID, error) {
	f.suits = append(f.suits, spawnSuitCall{p, s, pos})
	return domain.ShipID(499 + len(f.suits)), nil
}

func (f *fakeSuitSpawner) SpawnFor(_ context.Context, p domain.PlayerID) error {
	f.respawns = append(f.respawns, p)
	return nil
}

type fakeBus struct{ topics []string }

func (f *fakeBus) Publish(_ context.Context, topic string, _ []byte) error {
	f.topics = append(f.topics, topic)
	return nil
}

func newRespawner(sp *fakeSuitSpawner, b *fakeBus) spacesuitRespawner {
	return newRespawnerWith(sp, b, newFakeEjector())
}

func newRespawnerWith(sp *fakeSuitSpawner, b *fakeBus, ej *fakeEjector) spacesuitRespawner {
	return spacesuitRespawner{
		spawner: sp, bus: b, players: ej, npc: 99, home: domain.SectorID(1), logger: slog.New(slog.DiscardHandler),
	}
}

func shipKill(player domain.PlayerID, suit bool) sector.EntityKilledEvent {
	return sector.EntityKilledEvent{
		Victim:            domain.EntityRef{Kind: domain.EntityKindShip, ID: 42},
		SectorID:          domain.SectorID(5),
		Pos:               domain.Vec2{X: 10, Y: 20},
		VictimPlayer:      player,
		VictimIsSpacesuit: suit,
	}
}

func TestUnit_Respawn_NormalShipDeath_SpawnsSpacesuitAtDeathSpot(t *testing.T) {
	t.Parallel()
	sp := &fakeSuitSpawner{}
	b := &fakeBus{}
	ej := newFakeEjector()
	ej.active[100] = 42 // flying the hull that just died
	newRespawnerWith(sp, b, ej).OnKill(context.Background(), shipKill(100, false))

	require.Equal(t, []spawnSuitCall{{player: 100, sector: 5, pos: domain.Vec2{X: 10, Y: 20}}}, sp.suits)
	assert.Empty(t, sp.respawns, "a normal death does not respawn at home")
	assert.Empty(t, b.topics, "no handoff for the in-place suit spawn")
	// TASK-194: the dead hull's row is deleted and the FK nulls active_ship_id,
	// so the killed pilot must be repointed at the suit. Without this the
	// «which ship do you fly» resolution falls back to the lowest-id rule and
	// picks a hull the player parked somewhere else entirely.
	assert.Equal(t, domain.ShipID(500), ej.active[100], "killed pilot now flies the spacesuit")
}

func TestUnit_Respawn_SpacesuitDeath_RespawnsHomeWithHandoff(t *testing.T) {
	t.Parallel()
	sp := &fakeSuitSpawner{}
	b := &fakeBus{}
	newRespawner(sp, b).OnKill(context.Background(), shipKill(100, true))

	assert.Empty(t, sp.suits, "a suit death does not spawn another suit")
	require.Equal(t, []domain.PlayerID{100}, sp.respawns)
	require.Equal(t, []string{sector.PlayerHandoffTopic(100)}, b.topics, "WS moved to home via handoff")
}

func TestUnit_Respawn_IgnoresNPCAndNonShipAndZeroPlayer(t *testing.T) {
	t.Parallel()
	sp := &fakeSuitSpawner{}
	r := newRespawner(sp, &fakeBus{})

	r.OnKill(context.Background(), shipKill(99, false))      // npc owner
	r.OnKill(context.Background(), shipKill(0, false))       // unattributed
	r.OnKill(context.Background(), sector.EntityKilledEvent{ // a station, not a ship
		Victim: domain.EntityRef{Kind: domain.EntityKindStation, ID: 7}, VictimPlayer: 100,
	})

	assert.Empty(t, sp.suits)
	assert.Empty(t, sp.respawns)
}

// the handoff payload carries the player's id and the home target so the WS
// re-subscribes correctly.
func TestUnit_Respawn_HandoffPayloadShape(t *testing.T) {
	t.Parallel()
	sp := &fakeSuitSpawner{}
	captured := &capturingBus{}
	r := spacesuitRespawner{spawner: sp, bus: captured, npc: 99, home: domain.SectorID(1), logger: slog.New(slog.DiscardHandler)}
	r.OnKill(context.Background(), shipKill(100, true))

	var ev sector.PlayerHandoffEvent
	require.NoError(t, json.Unmarshal(captured.payload, &ev))
	assert.Equal(t, domain.PlayerID(100), ev.PlayerID)
	assert.Equal(t, domain.SectorID(1), ev.TargetSector)
	assert.Equal(t, domain.SectorID(5), ev.SourceSector)
}

type capturingBus struct{ payload []byte }

func (c *capturingBus) Publish(_ context.Context, _ string, payload []byte) error {
	c.payload = payload
	return nil
}

type fakeEjector struct {
	active    map[domain.PlayerID]domain.ShipID
	passenger map[domain.PlayerID]domain.ShipID
}

func newFakeEjector() *fakeEjector {
	return &fakeEjector{active: map[domain.PlayerID]domain.ShipID{}, passenger: map[domain.PlayerID]domain.ShipID{}}
}

func (f *fakeEjector) SetActiveShip(_ context.Context, p domain.PlayerID, id domain.ShipID) error {
	f.active[p] = id
	return nil
}

func (f *fakeEjector) SetPassengerHost(_ context.Context, p domain.PlayerID, h domain.ShipID) error {
	f.passenger[p] = h
	return nil
}

func (f *fakeEjector) ActiveShip(_ context.Context, p domain.PlayerID) (domain.ShipID, bool, error) {
	id, ok := f.active[p]
	return id, ok, nil
}

func TestUnit_Respawn_HostDeath_EjectsPassengersIntoSuits(t *testing.T) {
	t.Parallel()
	sp := &fakeSuitSpawner{}
	b := &fakeBus{}
	ej := newFakeEjector()
	// Pre-seed a stale passenger link to confirm it is cleared.
	ej.passenger[8] = 42
	r := spacesuitRespawner{
		spawner: sp, bus: b, players: ej, npc: 99, home: domain.SectorID(1), logger: slog.New(slog.DiscardHandler),
	}

	// NPC host (owner 99) destroyed with one player rider (8).
	ev := shipKill(99, false)
	ev.VictimPassengers = []domain.PlayerID{8}
	r.OnKill(context.Background(), ev)

	require.Equal(t, []spawnSuitCall{{player: 8, sector: 5, pos: domain.Vec2{X: 10, Y: 20}}}, sp.suits,
		"passenger ejected into a suit at the death spot; NPC victim itself spawns none")
	assert.Empty(t, sp.respawns)
	assert.Equal(t, domain.ShipID(0), ej.passenger[8], "passenger link cleared")
	assert.Contains(t, b.topics, sector.PlayerHandoffTopic(8), "rider WS moved to the death sector")
}

func shipCaptured(oldOwner domain.PlayerID, passengers ...domain.PlayerID) sector.ShipCapturedEvent {
	return sector.ShipCapturedEvent{
		ShipID: 42, SectorID: domain.SectorID(5), Pos: domain.Vec2{X: 10, Y: 20},
		OldOwner: oldOwner, Passengers: passengers,
	}
}

// TASK-100.3.9.2 / AC-6: a capture ejects the piloting old owner AND every
// passenger into spacesuits at the ship's spot, repointing their active ship.
func TestUnit_Capture_EjectsPilotAndPassengers(t *testing.T) {
	t.Parallel()
	sp := &fakeSuitSpawner{}
	b := &fakeBus{}
	ej := newFakeEjector()
	ej.active[7] = 42 // old owner is actively flying the captured ship (id 42)
	r := spacesuitRespawner{spawner: sp, bus: b, players: ej, npc: 99, home: domain.SectorID(1), logger: slog.New(slog.DiscardHandler)}

	r.OnShipCaptured(context.Background(), shipCaptured(7, 100, 101))

	players := map[domain.PlayerID]bool{}
	for _, c := range sp.suits {
		players[c.player] = true
	}
	assert.True(t, players[7], "old pilot ejected")
	assert.True(t, players[100] && players[101], "passengers ejected")
	assert.Len(t, sp.suits, 3)
	assert.Empty(t, sp.respawns, "capture never respawns a ship at home")
	// passengers are ejected first (suits 500, 501), the old pilot last (502)
	assert.Equal(t, domain.ShipID(502), ej.active[7], "ejected pilot repointed to the new suit")
}

// The NPC/unowned old pilot is not a player — no eject for them, but riders
// still get ejected.
func TestUnit_Capture_SkipsNPCAndZeroPilot_StillEjectsPassengers(t *testing.T) {
	t.Parallel()
	for _, owner := range []domain.PlayerID{99, 0} {
		sp := &fakeSuitSpawner{}
		ej := newFakeEjector()
		r := spacesuitRespawner{spawner: sp, bus: &fakeBus{}, players: ej, npc: 99, home: domain.SectorID(1), logger: slog.New(slog.DiscardHandler)}

		r.OnShipCaptured(context.Background(), shipCaptured(owner, 100))

		require.Len(t, sp.suits, 1, "only the passenger is ejected, not the NPC/zero pilot")
		assert.Equal(t, domain.PlayerID(100), sp.suits[0].player)
	}
}

// An old owner who already left this ship (active ship points elsewhere) is not
// yanked out of whatever they are flying now.
func TestUnit_Capture_SkipsAbandonedPilot(t *testing.T) {
	t.Parallel()
	sp := &fakeSuitSpawner{}
	ej := newFakeEjector()
	ej.active[7] = 77 // flying a different ship, not the captured 42
	r := spacesuitRespawner{spawner: sp, bus: &fakeBus{}, players: ej, npc: 99, home: domain.SectorID(1), logger: slog.New(slog.DiscardHandler)}

	r.OnShipCaptured(context.Background(), shipCaptured(7))

	assert.Empty(t, sp.suits, "abandoned pilot not ejected from their current ship")
	assert.Equal(t, domain.ShipID(77), ej.active[7], "active ship untouched")
}
