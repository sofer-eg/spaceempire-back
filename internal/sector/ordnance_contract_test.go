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
// return exactly one id per drone, and this returns `ids` regardless of how many
// drones it was handed. It stands in for a future implementation that batches the
// INSERTs (`INSERT … RETURNING id`) or retries inside the transaction and gets the
// count wrong — the only way to reach the guard, since both real fakes and the
// app-side adapter honour the contract.
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

// TestUnit_LaunchDrones_IDCountContract is a white-box test of the guard in
// launchDrones (review round 2). LaunchDroneCommand.apply pairs ids[i] with
// ds[i], so a miscounting Ordnance would either index past the salvo — panicking
// the tick goroutine, which has no recover() and would take every sector this
// worker owns with it — or silently drop a drone that was already charged and
// INSERTed. The guard turns both into a refused launch, so neither can reach
// apply.
func TestUnit_LaunchDrones_IDCountContract(t *testing.T) {
	t.Parallel()

	ship := &domain.Ship{ID: 1, PlayerID: 7, SectorID: 1}
	salvo := make([]domain.Drone, 2)

	cases := map[string][]domain.DroneID{
		"too many ids": {1, 2, 3},
		"too few ids":  {1},
		"no ids":       nil,
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := &Worker{
				logger:   slog.New(slog.DiscardHandler),
				cfg:      Config{RepoTimeout: time.Second},
				ordnance: miscountingOrdnance{ids: ids},
			}
			got, err := w.launchDrones(ship, 51, salvo)
			require.ErrorIs(t, err, errOrdnanceIDCount)
			assert.Nil(t, got, "a broken contract yields no ids for apply to pair")
		})
	}

	// The matching count is accepted — the guard rejects only a mismatch.
	t.Run("exact count accepted", func(t *testing.T) {
		t.Parallel()
		w := &Worker{
			logger:   slog.New(slog.DiscardHandler),
			cfg:      Config{RepoTimeout: time.Second},
			ordnance: miscountingOrdnance{ids: []domain.DroneID{1, 2}},
		}
		got, err := w.launchDrones(ship, 51, salvo)
		require.NoError(t, err)
		assert.Equal(t, []domain.DroneID{1, 2}, got)
	})
}
