package sector

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
)

// miscountingOrdnance breaks the Ordnance contract on purpose: LaunchDrones must
// never return MORE ids than the drones it was handed, and this returns `ids`
// regardless. It stands in for a future implementation that batches the INSERTs
// (`INSERT … RETURNING id`) or retries inside the transaction and gets the count
// wrong — the only way to reach the guard, since both real fakes and the app-side
// adapter honour the contract.
type miscountingOrdnance struct{ ids []domain.DroneID }

func (o miscountingOrdnance) SpendMissile(context.Context, domain.EntityRef, domain.GoodsTypeID) error {
	return nil
}

func (o miscountingOrdnance) LaunchTorpedo(context.Context, domain.EntityRef, domain.GoodsTypeID, domain.Torpedo) (domain.TorpedoID, error) {
	return 1, nil
}

func (o miscountingOrdnance) LaunchDrones(context.Context, domain.EntityRef, domain.GoodsTypeID, []domain.Drone) ([]domain.DroneID, error) {
	return o.ids, nil
}

func (o miscountingOrdnance) RecallDrones(_ context.Context, _ domain.EntityRef, _ domain.GoodsTypeID, ids []domain.DroneID) (RecallOutcome, error) {
	return RecallOutcome{Removed: ids, Credited: len(ids)}, nil
}

// TestUnit_LaunchDrones_IDCountContract is a white-box test of the guard in
// launchDrones (review round 2; relaxed by TASK-176). LaunchDroneCommand.apply
// pairs ids[i] with ds[i], so an Ordnance returning MORE ids than drones would
// index past the salvo — panicking the tick goroutine, which has no recover() and
// would take every sector this worker owns with it. The guard turns that into a
// refused launch, so it cannot reach apply.
//
// FEWER ids is now a legal answer, not a broken contract: since TASK-176 the
// ordnance sizes the salvo by what the hold carries and returns one id per drone it
// actually launched. apply spawns exactly those.
func TestUnit_LaunchDrones_IDCountContract(t *testing.T) {
	t.Parallel()

	ship := &domain.Ship{ID: 1, PlayerID: 7, SectorID: 1}
	salvo := make([]domain.Drone, 2)

	newWorker := func(ids []domain.DroneID) *Worker {
		return &Worker{
			logger:   slog.New(slog.DiscardHandler),
			cfg:      Config{RepoTimeout: time.Second},
			ordnance: miscountingOrdnance{ids: ids},
		}
	}

	// Both ways to break the contract: more ids than drones, and an empty set with
	// no error. The salvo is never empty (apply only calls this with
	// toSpawn = allowed >= 1) and an ordnance that launched nothing owes the caller
	// cargo.ErrInsufficientQuantity, so a silent nil would otherwise reach the player
	// as 200 "spawned: 0" — a successful launch of nothing.
	refused := map[string][]domain.DroneID{
		"too many ids": {1, 2, 3},
		"nothing flew": nil,
	}
	for name, ids := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := newWorker(ids).launchDrones(ship, 51, salvo)
			require.ErrorIs(t, err, errOrdnanceIDCount)
			assert.Nil(t, got, "a broken contract yields no ids for apply to pair")
		})
	}

	// Fewer than requested is the short-magazine answer TASK-176 made legal: the
	// units the hold could pay for flew, and only those come back as ids.
	accepted := map[string][]domain.DroneID{
		"exact count": {1, 2},
		"short salvo": {1},
	}
	for name, ids := range accepted {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := newWorker(ids).launchDrones(ship, 51, salvo)
			require.NoError(t, err)
			assert.Equal(t, ids, got)
		})
	}
}
