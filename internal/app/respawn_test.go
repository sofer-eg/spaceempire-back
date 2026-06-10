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

func (f *fakeSuitSpawner) SpawnSpacesuit(_ context.Context, p domain.PlayerID, s domain.SectorID, pos domain.Vec2, _ *domain.EntityRef) (domain.ShipID, error) {
	f.suits = append(f.suits, spawnSuitCall{p, s, pos})
	return 0, nil
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
	return spacesuitRespawner{spawner: sp, bus: b, npc: 99, home: domain.SectorID(1), logger: slog.New(slog.DiscardHandler)}
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
	newRespawner(sp, b).OnKill(context.Background(), shipKill(100, false))

	require.Equal(t, []spawnSuitCall{{player: 100, sector: 5, pos: domain.Vec2{X: 10, Y: 20}}}, sp.suits)
	assert.Empty(t, sp.respawns, "a normal death does not respawn at home")
	assert.Empty(t, b.topics, "no handoff for the in-place suit spawn")
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
