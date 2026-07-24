package sector_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/ai"
	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// npcAIWorker builds a worker whose single ship (id 1) is an NPC: system-owned
// (PlayerID sysPlayer, non-zero) and driven by an AI controller. The controller
// presence is what marks it NPC to the pacer gate (C1) — mirrors how police.go
// / knock.go distinguish NPCs from real players.
func npcAIWorker(t *testing.T, b bus.Bus, sysPlayer domain.PlayerID, ship domain.Ship, opts ...sector.Option) *sector.Worker {
	t.Helper()
	ship.PlayerID = sysPlayer
	registry := ai.NewRegistry()
	registry.Register("circle", newCircleController)
	base := []sector.Option{
		sector.WithHandoff(handoffTopology(), b),
		sector.WithAI(registry, nil, map[domain.SectorID][]domain.AIState{testSector: {
			{ShipID: ship.ID, SectorID: testSector, ControllerKind: "circle", StateJSON: mustMarshalCircle(t, &circleController{})},
		}}),
	}
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50, DockRange: 3},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {ship}},
		append(base, opts...)...,
	)
}

// C1: an NPC ship (system player + AI controller) jumping through a gate must
// NOT publish player.jumped — otherwise the quest pacer churns counter rows and
// offers for the system player. The intake handoff still happens (the jump is
// real); only the pacer trigger is suppressed.
func TestUnit_JumpCommand_NPCShip_DoesNotPublishPlayerJumped(t *testing.T) {
	t.Parallel()
	b := &fakeBus{}
	const sysPlayer = domain.PlayerID(999)
	w := npcAIWorker(t, b, sysPlayer, domain.Ship{
		ID: 1, SectorID: testSector, Pos: domain.Vec2{X: 100, Y: 0}, MaxSpeed: 10,
	})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: sysPlayer, ShipID: 1, GateID: 10})
	require.NoError(t, res.Err)
	assert.Empty(t, w.Snapshot(testSector).Ships, "NPC still leaves the source sector")

	msgs := b.snapshot()
	var sawIntake, sawJumped bool
	for _, m := range msgs {
		switch m.topic {
		case "sector.2.intake":
			sawIntake = true
		case sector.PlayerJumpedTopic:
			sawJumped = true
		}
	}
	assert.True(t, sawIntake, "intake handoff must still fire for an NPC jump")
	assert.False(t, sawJumped, "NPC jump must not fire the pacer (player.jumped)")
}

// C1: an NPC ship docking to a station must NOT publish player.docked.
func TestUnit_DockCommand_NPCShip_DoesNotPublishPlayerDocked(t *testing.T) {
	t.Parallel()
	b := &fakeBus{}
	const sysPlayer = domain.PlayerID(999)
	stationPos := domain.Vec2{X: 100, Y: 50}
	w := npcAIWorker(t, b, sysPlayer, domain.Ship{
		ID: 1, SectorID: testSector, Pos: stationPos,
	}, sector.WithStatics(map[domain.SectorID]domain.SectorStatics{testSector: {
		Stations: []domain.Station{{ID: 7, SectorID: testSector, Pos: stationPos, Built: true}},
	}}))

	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: sysPlayer, ShipID: 1, Target: domain.EntityRef{Kind: domain.EntityKindStation, ID: 7}, Reply: reply}
	})
	require.NoError(t, err)

	for _, m := range b.snapshot() {
		assert.NotEqual(t, sector.PlayerDockedTopic, m.topic, "NPC dock must not fire the pacer (player.docked)")
	}
}

// Counterpart to the NPC case: a real player (no AI controller) docking to a
// station DOES publish player.docked with the ship's player and sector, so the
// gate suppresses only NPCs.
func TestUnit_DockCommand_RealPlayer_PublishesPlayerDocked(t *testing.T) {
	t.Parallel()
	b := &fakeBus{}
	const player = domain.PlayerID(7)
	stationPos := domain.Vec2{X: 100, Y: 50}
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, DockRange: 3},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {{ID: 1, PlayerID: player, SectorID: testSector, Pos: stationPos}}},
		sector.WithHandoff(handoffTopology(), b),
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{testSector: {
			Stations: []domain.Station{{ID: 7, SectorID: testSector, Pos: stationPos, Built: true}},
		}}),
	)

	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.DockCommand{PlayerID: player, ShipID: 1, Target: domain.EntityRef{Kind: domain.EntityKindStation, ID: 7}, Reply: reply}
	})
	require.NoError(t, err)

	var docked *sector.PlayerDockedEvent
	for _, m := range b.snapshot() {
		if m.topic == sector.PlayerDockedTopic {
			var ev sector.PlayerDockedEvent
			require.NoError(t, json.Unmarshal(m.payload, &ev))
			docked = &ev
		}
	}
	require.NotNil(t, docked, "real player dock must fire the pacer (player.docked)")
	assert.Equal(t, player, docked.PlayerID)
	assert.Equal(t, testSector, docked.SectorID)
}
