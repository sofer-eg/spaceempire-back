package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	playersrepo "spaceempire/back/internal/persistence/players"
	"spaceempire/back/internal/sector"
)

// fakeReputationAdder records the awarder's AddReputation calls.
type fakeReputationAdder struct {
	calls []repCall
}

type repCall struct {
	player domain.PlayerID
	delta  playersrepo.Reputation
}

func (f *fakeReputationAdder) AddReputation(_ context.Context, player domain.PlayerID, delta playersrepo.Reputation) (playersrepo.Reputation, error) {
	f.calls = append(f.calls, repCall{player, delta})
	return playersrepo.Reputation{}, nil
}

func TestUnit_ReputationAwarder_GrantsWarRateToRealKiller(t *testing.T) {
	t.Parallel()
	adder := &fakeReputationAdder{}
	a := reputationAwarder{players: adder, npc: 99}

	require.NoError(t, a.OnShipKilled(context.Background(), 100))
	require.Equal(t, []repCall{{player: 100, delta: playersrepo.Reputation{War: warRatePerKill}}}, adder.calls)
}

func TestUnit_ReputationAwarder_SkipsNPCAndZeroKiller(t *testing.T) {
	t.Parallel()
	adder := &fakeReputationAdder{}
	a := reputationAwarder{players: adder, npc: 99}

	require.NoError(t, a.OnShipKilled(context.Background(), 99)) // the NPC owner
	require.NoError(t, a.OnShipKilled(context.Background(), 0))  // unattributed
	assert.Empty(t, adder.calls, "NPC / zero killers earn no war_rate")
}

// guard: the awarder satisfies the sector port.
var _ sector.ReputationAwarder = reputationAwarder{}
