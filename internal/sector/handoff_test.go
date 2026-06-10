package sector_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/world"
)

// newRunningHandoffWorker boots a Worker with a real in-memory bus and
// starts Run in a background goroutine so the intake subscription is live.
// Cancel the returned context to stop it.
func newRunningHandoffWorker(t *testing.T) (*sector.Worker, bus.Bus, context.Context, context.CancelFunc) {
	t.Helper()

	b := bus.NewInMemory(16)
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: 50 * time.Millisecond, GateRange: 50},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{2: {}},
		sector.WithHandoff(handoffTopology(), b),
	)
	ctx, cancel := context.WithCancel(context.Background())
	w.EnsureSubscriptions(ctx)
	go func() { _ = w.Run(ctx) }()
	return w, b, ctx, cancel
}

// fakeBus is a minimal sector.Bus used to inspect Publish payloads without
// pulling in the real in-memory implementation (which spawns goroutines).
type fakeBus struct {
	mu         sync.Mutex
	published  []publishedMessage
	publishErr error
}

type publishedMessage struct {
	topic   string
	payload []byte
}

func (b *fakeBus) Publish(_ context.Context, topic string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.publishErr != nil {
		return b.publishErr
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	b.published = append(b.published, publishedMessage{topic: topic, payload: cp})
	return nil
}

func (b *fakeBus) Subscribe(_ context.Context, _ string, _ func([]byte)) error {
	return nil
}

func (b *fakeBus) snapshot() []publishedMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]publishedMessage, len(b.published))
	copy(out, b.published)
	return out
}

func handoffTopology() *world.Topology {
	sectors := []domain.Sector{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}
	gates := []domain.Gate{{
		ID:      10,
		SectorA: 1, PosA: domain.Vec2{X: 100, Y: 0},
		SectorB: 2, PosB: domain.Vec2{X: -100, Y: 0},
	}}
	return world.New(sectors, gates)
}

func newJumpWorker(t *testing.T, repo sector.ShipRepo, b bus.Bus, initial []domain.Ship) *sector.Worker {
	t.Helper()
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50},
		clock.NewRealClock(),
		repo,
		nil,
		map[domain.SectorID][]domain.Ship{1: initial},
		sector.WithHandoff(handoffTopology(), b),
	)
}

func jumpReply(t *testing.T, w *sector.Worker, cmd sector.JumpCommand) sector.CmdResult {
	t.Helper()
	reply := make(chan sector.CmdResult, 1)
	cmd.Reply = reply
	require.NoError(t, w.Send(1, cmd))
	w.Tick(context.Background())
	select {
	case r := <-reply:
		return r
	case <-time.After(time.Second):
		t.Fatal("no reply from JumpCommand")
		return sector.CmdResult{}
	}
}

func TestUnit_JumpCommand_Success(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	bus := &fakeBus{}
	pid := domain.PlayerID(7)
	w := newJumpWorker(t, repo, bus, []domain.Ship{{
		ID: 1, PlayerID: pid, SectorID: 1,
		Pos: domain.Vec2{X: 100, Y: 0}, MaxSpeed: 10,
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: pid, ShipID: 1, GateID: 10})

	require.NoError(t, res.Err)
	assert.Empty(t, w.Snapshot(1).Ships, "ship must leave source sector")

	require.Len(t, repo.saves, 1, "Save must be invoked exactly once")
	saved := repo.saves[0]
	assert.Equal(t, domain.SectorID(2), saved.SectorID)
	assert.Equal(t, domain.Vec2{X: -100, Y: 0}, saved.Pos)
	assert.Equal(t, domain.Vec2{}, saved.Vel)
	assert.Nil(t, saved.Target, "Target must be cleared on handoff")

	msgs := bus.snapshot()
	require.Len(t, msgs, 2, "expect intake + player-handoff events")
	assert.Equal(t, "sector.2.intake", msgs[0].topic)
	var ev sector.JumpEvent
	require.NoError(t, json.Unmarshal(msgs[0].payload, &ev))
	assert.Equal(t, domain.SectorID(1), ev.SourceSector)
	assert.Equal(t, domain.SectorID(2), ev.TargetSector)
	assert.Equal(t, domain.ShipID(1), ev.Ship.ID)
	assert.Equal(t, domain.Vec2{X: -100, Y: 0}, ev.ExitPos)

	assert.Equal(t, "player.7.handoff", msgs[1].topic)
	var handoff sector.PlayerHandoffEvent
	require.NoError(t, json.Unmarshal(msgs[1].payload, &handoff))
	assert.Equal(t, pid, handoff.PlayerID)
	assert.Equal(t, domain.ShipID(1), handoff.ShipID)
	assert.Equal(t, domain.SectorID(1), handoff.SourceSector)
	assert.Equal(t, domain.SectorID(2), handoff.TargetSector)

	stats := w.Stats().Sectors[0]
	assert.Equal(t, uint64(1), stats.HandoffsOut[2], "HandoffsOut must be incremented")
}

func TestUnit_JumpCommand_PassengerFollowsHost(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	passenger := domain.PlayerID(8)
	w := newJumpWorker(t, repo, b, []domain.Ship{{
		ID: 1, PlayerID: pid, SectorID: 1,
		Pos: domain.Vec2{X: 100, Y: 0}, MaxSpeed: 10,
		PassengerPlayers: []domain.PlayerID{passenger},
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: pid, ShipID: 1, GateID: 10})
	require.NoError(t, res.Err)

	msgs := b.snapshot()
	topics := make(map[string]sector.PlayerHandoffEvent)
	var intake sector.JumpEvent
	for _, m := range msgs {
		if m.topic == "sector.2.intake" {
			require.NoError(t, json.Unmarshal(m.payload, &intake))
			continue
		}
		var ev sector.PlayerHandoffEvent
		require.NoError(t, json.Unmarshal(m.payload, &ev))
		topics[m.topic] = ev
	}

	require.Contains(t, topics, sector.PlayerHandoffTopic(pid), "owner handoff published")
	require.Contains(t, topics, sector.PlayerHandoffTopic(passenger), "passenger handoff published")
	assert.Equal(t, domain.SectorID(2), topics[sector.PlayerHandoffTopic(passenger)].TargetSector)
	assert.Equal(t, domain.ShipID(1), topics[sector.PlayerHandoffTopic(passenger)].ShipID, "passenger follows the host ship")
	assert.Equal(t, []domain.PlayerID{passenger}, intake.Ship.PassengerPlayers, "host carries passengers across the jump")
}

func TestUnit_JumpCommand_ShipNotFound(t *testing.T) {
	t.Parallel()

	w := newJumpWorker(t, &fakeShipRepo{}, &fakeBus{}, nil)

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 1, ShipID: 999, GateID: 10})
	assert.ErrorIs(t, res.Err, sector.ErrShipNotFound)
}

func TestUnit_JumpCommand_Forbidden(t *testing.T) {
	t.Parallel()

	w := newJumpWorker(t, &fakeShipRepo{}, &fakeBus{}, []domain.Ship{{
		ID: 1, PlayerID: 7, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0},
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 999, ShipID: 1, GateID: 10})
	assert.ErrorIs(t, res.Err, sector.ErrForbidden)
}

func TestUnit_JumpCommand_InvalidGate(t *testing.T) {
	t.Parallel()

	w := newJumpWorker(t, &fakeShipRepo{}, &fakeBus{}, []domain.Ship{{
		ID: 1, PlayerID: 7, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0},
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 7, ShipID: 1, GateID: 9999})
	assert.ErrorIs(t, res.Err, sector.ErrInvalidGate)
}

func TestUnit_JumpCommand_OutOfRange(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	bus := &fakeBus{}
	w := newJumpWorker(t, repo, bus, []domain.Ship{{
		ID: 1, PlayerID: 7, SectorID: 1, Pos: domain.Vec2{X: 0, Y: 0},
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 7, ShipID: 1, GateID: 10})
	assert.ErrorIs(t, res.Err, sector.ErrGateOutOfRange)
	assert.Empty(t, repo.saves, "Save must not happen on validation failure")
	assert.Empty(t, bus.snapshot(), "Publish must not happen on validation failure")

	// And the ship is still in sector A.
	assert.Len(t, w.Snapshot(1).Ships, 1)
}

func TestUnit_JumpCommand_HandoffUnavailable(t *testing.T) {
	t.Parallel()

	// Worker without WithHandoff option.
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {{ID: 1, PlayerID: 7, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0}}}},
	)

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 7, ShipID: 1, GateID: 10})
	assert.ErrorIs(t, res.Err, sector.ErrHandoffUnavailable)
}

func TestUnit_JumpCommand_DBSaveFails(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{saveErr: errors.New("db down")}
	bus := &fakeBus{}
	w := newJumpWorker(t, repo, bus, []domain.Ship{{
		ID: 1, PlayerID: 7, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0},
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 7, ShipID: 1, GateID: 10})

	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "save ship")
	assert.Empty(t, bus.snapshot(), "Publish must not run when Save failed")
	assert.Len(t, w.Snapshot(1).Ships, 1, "ship stays in source sector on Save failure")
}

func TestUnit_JumpCommand_BusPublishFails(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	bus := &fakeBus{publishErr: errors.New("bus down")}
	w := newJumpWorker(t, repo, bus, []domain.Ship{{
		ID: 1, PlayerID: 7, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 0},
	}})

	res := jumpReply(t, w, sector.JumpCommand{PlayerID: 7, ShipID: 1, GateID: 10})

	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "publish jump event")
	// DB was already updated to B before bus failure — that's the deliberate
	// trade-off recorded in the spec (worker B's intake will restore RAM on
	// restart via LoadAll); the ship remains in sector A's RAM here.
	assert.Len(t, w.Snapshot(1).Ships, 1)
}

func TestUnit_JumpIntakeCommand_AddsShip(t *testing.T) {
	t.Parallel()

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second, GateRange: 50},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{2: {}},
		sector.WithHandoff(handoffTopology(), &fakeBus{}),
	)

	ship := domain.Ship{
		ID: 42, PlayerID: 7, SectorID: 2,
		Pos: domain.Vec2{X: -100, Y: 0}, HP: 100, Shield: 100,
	}
	require.NoError(t, w.Send(2, sector.JumpIntakeCommand{Event: sector.JumpEvent{
		Ship: ship, SourceSector: 1, TargetSector: 2, ExitPos: ship.Pos,
	}}))
	w.Tick(context.Background())

	snap := w.Snapshot(2)
	require.Len(t, snap.Ships, 1)
	assert.Equal(t, domain.ShipID(42), snap.Ships[0].ID)
	assert.Equal(t, domain.SectorID(2), snap.Ships[0].SectorID)

	stats := w.Stats().Sectors[0]
	assert.Equal(t, uint64(1), stats.HandoffsIn[1])
}

func TestUnit_Handoff_RunWiresIntakeSubscription(t *testing.T) {
	t.Parallel()

	// Use the real in-memory bus so we exercise the Subscribe path that the
	// worker takes during Run.
	w, busInstance, ctx, cancel := newRunningHandoffWorker(t)
	defer cancel()

	ship := domain.Ship{ID: 7, PlayerID: 1, SectorID: 2, Pos: domain.Vec2{X: -100, Y: 0}}
	payload, err := json.Marshal(sector.JumpEvent{
		Ship: ship, SourceSector: 1, TargetSector: 2, ExitPos: ship.Pos,
	})
	require.NoError(t, err)
	require.NoError(t, busInstance.Publish(ctx, sector.IntakeTopic(2), payload))

	require.Eventually(t, func() bool {
		return len(w.Snapshot(2).Ships) == 1
	}, 5*time.Second, 20*time.Millisecond, "ship must appear in B after intake")
}
