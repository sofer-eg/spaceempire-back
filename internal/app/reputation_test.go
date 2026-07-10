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

// TestUnit_ReputationAwarder_OnShipCaptured_GrantsWarAndDropsRaceStanding: a real
// player capturing a main-race (1-5) ship earns war_rate AND loses standing with
// that race (TASK-100.3.9.4, mirrors OnRaceShipKilled).
func TestUnit_ReputationAwarder_OnShipCaptured_GrantsWarAndDropsRaceStanding(t *testing.T) {
	t.Parallel()
	adder := &fakeReputationAdder{}
	standing := newFakeStanding(-10)
	a := reputationAwarder{players: adder, standing: standing, npc: 99}

	require.NoError(t, a.OnShipCaptured(context.Background(), 100, 1))
	require.Equal(t, []repCall{{player: 100, delta: playersrepo.Reputation{War: warRatePerKill}}}, adder.calls)
	require.Len(t, standing.adjusts, 1)
	assert.Equal(t, adjust{player: 100, race: 1, delta: -captureRacePenalty}, standing.adjusts[0])
}

// TestUnit_ReputationAwarder_OnShipCaptured_NoRacePenaltyForNeutral: capturing a
// neutral (race 0, a player ship) or a pirate/xenon/kha'ak (6-8) ship still grants
// war_rate but drops no race standing (those races carry no per-player standing).
func TestUnit_ReputationAwarder_OnShipCaptured_NoRacePenaltyForNeutral(t *testing.T) {
	t.Parallel()
	adder := &fakeReputationAdder{}
	standing := newFakeStanding(-10)
	a := reputationAwarder{players: adder, standing: standing, npc: 99}

	require.NoError(t, a.OnShipCaptured(context.Background(), 100, 0)) // neutral player ship
	require.NoError(t, a.OnShipCaptured(context.Background(), 100, 8)) // Kha'ak
	require.Len(t, adder.calls, 2, "each capture still grants war_rate")
	assert.Empty(t, standing.adjusts, "no per-player standing for neutral / hostile races")
}

// TestUnit_ReputationAwarder_OnShipCaptured_SkipsNPCAndZero: the NPC owner and the
// zero player earn nothing.
func TestUnit_ReputationAwarder_OnShipCaptured_SkipsNPCAndZero(t *testing.T) {
	t.Parallel()
	adder := &fakeReputationAdder{}
	standing := newFakeStanding(-10)
	a := reputationAwarder{players: adder, standing: standing, npc: 99}

	require.NoError(t, a.OnShipCaptured(context.Background(), 99, 1)) // the NPC owner
	require.NoError(t, a.OnShipCaptured(context.Background(), 0, 1))  // unattributed
	assert.Empty(t, adder.calls)
	assert.Empty(t, standing.adjusts)
}

// guard: the awarder satisfies the sector port.
var _ sector.ReputationAwarder = reputationAwarder{}
