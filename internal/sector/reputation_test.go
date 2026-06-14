package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeReputationAwarder records the worker's OnShipKilled calls.
type fakeReputationAwarder struct {
	killers []domain.PlayerID
}

func (f *fakeReputationAwarder) OnShipKilled(_ context.Context, killer domain.PlayerID) error {
	f.killers = append(f.killers, killer)
	return nil
}

// TestUnit_Worker_Reputation_AwardsWarRateOnKill proves the worker credits the
// attributed killer (LastAttacker) when a ship is destroyed (phase 10.3.13).
func TestUnit_Worker_Reputation_AwardsWarRateOnKill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	awarder := &fakeReputationAwarder{}
	killer := laserShip(1, 100, domain.Vec2{X: 0, Y: 0})
	killer.Energy = 1000
	killer.MaxEnergy = 1000
	killer.LaserDamage = 1000
	killer.LaserRange = 1000
	killer.LaserEnergyCost = 0
	killer.AttackTarget = &domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	victim := laserShip(2, 200, domain.Vec2{X: 30, Y: 0})

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {killer, victim}},
		sector.WithReputation(awarder),
	)

	w.Tick(ctx) // killer one-shots the victim; the kill sweep awards war_rate

	require.Equal(t, []domain.PlayerID{100}, awarder.killers)
}

// TestUnit_Worker_Reputation_NoAwardWhenUnattributed proves an unattributed
// death (LastAttacker 0 — e.g. a ship that simply has no HP) does not call the
// awarder, so no war_rate is granted to player 0.
func TestUnit_Worker_Reputation_NoAwardWhenUnattributed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	awarder := &fakeReputationAwarder{}
	dead := laserShip(1, 200, domain.Vec2{X: 0, Y: 0})
	dead.HP = 0 // dies this tick with no LastAttacker set

	w := sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {dead}},
		sector.WithReputation(awarder),
	)

	w.Tick(ctx)

	assert.Empty(t, awarder.killers, "an unattributed death awards no war_rate")
}
