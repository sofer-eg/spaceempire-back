package sector

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/world"
)

const ownSector = domain.SectorID(1)

// recordingShipRepo records Save calls so the test can assert changeShipOwner
// persists the new owner + race through the (dynamic) Save path.
type recordingShipRepo struct{ saves []domain.Ship }

func (r *recordingShipRepo) Save(_ context.Context, s domain.Ship) error {
	r.saves = append(r.saves, s)
	return nil
}
func (r *recordingShipRepo) SaveEquipment(context.Context, domain.Ship) error { return nil }
func (r *recordingShipRepo) BatchUpdate(context.Context, []domain.Ship) error { return nil }
func (r *recordingShipRepo) Delete(context.Context, domain.ShipID) error      { return nil }

func ownTopology() *world.Topology {
	return world.New([]domain.Sector{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}, nil)
}

func TestUnit_ChangeShipOwner_ReownsResetsPersistsAndEjects(t *testing.T) {
	t.Parallel()

	repo := &recordingShipRepo{}
	b := bus.NewInMemory(16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan ShipCapturedEvent, 1)
	require.NoError(t, b.Subscribe(ctx, ShipCapturedTopic, func(payload []byte) {
		var ev ShipCapturedEvent
		if json.Unmarshal(payload, &ev) == nil {
			events <- ev
		}
	}))

	target := domain.Vec2{X: 5, Y: 6}
	ship := domain.Ship{
		ID: 3, PlayerID: 7, Race: 5, SectorID: ownSector,
		Pos: domain.Vec2{X: 10, Y: 20}, Vel: domain.Vec2{X: 2, Y: 2}, LastStep: domain.Vec2{X: 2, Y: 2},
		Target:           &target,
		AttackTarget:     &domain.EntityRef{Kind: domain.EntityKindShip, ID: 99},
		PassengerPlayers: []domain.PlayerID{100, 101},
	}
	w := NewWorker(0, Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), repo, nil,
		map[domain.SectorID][]domain.Ship{ownSector: {ship}},
		WithHandoff(ownTopology(), b),
	)
	s := w.sectors[ownSector]
	sh := s.ships[3]

	require.NoError(t, w.changeShipOwner(ctx, s, sh, 9))

	// FR-B1: re-owned + neutralised + combat/motion reset.
	assert.Equal(t, domain.PlayerID(9), sh.PlayerID)
	assert.Equal(t, domain.RaceID(0), sh.Race)
	assert.Nil(t, sh.AttackTarget)
	assert.Nil(t, sh.Target)
	assert.Equal(t, domain.Vec2{}, sh.Vel)
	assert.Equal(t, domain.Vec2{}, sh.LastStep)
	assert.Nil(t, sh.PassengerPlayers, "passengers cleared (ejected)")

	// Persisted immediately (player_id + race).
	// The row is written BEFORE the RAM ship is re-owned (TASK-148): player_id and
	// race are Save-only columns, so a failed write has nothing to recover it.
	require.Len(t, repo.saves, 1, "changeShipOwner persists the transfer through saveShip")
	assert.Equal(t, domain.PlayerID(9), repo.saves[0].PlayerID)
	assert.Equal(t, domain.RaceID(0), repo.saves[0].Race)

	// FR-B2: eject event carries the old crew for the app to spacesuit.
	select {
	case ev := <-events:
		assert.Equal(t, domain.ShipID(3), ev.ShipID)
		assert.Equal(t, domain.PlayerID(7), ev.OldOwner)
		assert.Equal(t, []domain.PlayerID{100, 101}, ev.Passengers)
		assert.Equal(t, domain.Vec2{X: 10, Y: 20}, ev.Pos)
	case <-time.After(time.Second):
		t.Fatal("no ShipCapturedEvent published")
	}
}
