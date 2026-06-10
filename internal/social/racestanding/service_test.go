package racestanding_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/social/racestanding"
)

// fakeRepo is an in-memory standing store for Service tests.
type fakeRepo struct {
	rows map[key]int
}

type key struct {
	player domain.PlayerID
	race   domain.RaceID
}

func newFakeRepo(rows ...racestanding.Row) *fakeRepo {
	m := map[key]int{}
	for _, r := range rows {
		m[key{r.Player, r.Race}] = r.Standing
	}
	return &fakeRepo{rows: m}
}

func (f *fakeRepo) LoadAll(_ context.Context) ([]racestanding.Row, error) {
	out := make([]racestanding.Row, 0, len(f.rows))
	for k, v := range f.rows {
		out = append(out, racestanding.Row{Player: k.player, Race: k.race, Standing: v})
	}
	return out, nil
}

func (f *fakeRepo) Upsert(_ context.Context, player domain.PlayerID, race domain.RaceID, standing int) error {
	f.rows[key{player, race}] = standing
	return nil
}

func (f *fakeRepo) DecayAll(_ context.Context, step int) error {
	for k, v := range f.rows {
		switch {
		case v > 0:
			if step > v {
				step = v
			}
			f.rows[k] = v - step
		case v < 0:
			s := step
			if s > -v {
				s = -v
			}
			f.rows[k] = v + s
		}
	}
	return nil
}

func primed(t *testing.T, repo racestanding.Repo, cfg racestanding.Config) *racestanding.Service {
	t.Helper()
	svc := racestanding.New(repo, cfg)
	require.NoError(t, svc.Precount(context.Background()))
	return svc
}

const (
	player = domain.PlayerID(7)
	argon  = domain.RaceID(1)
	boron  = domain.RaceID(2)
)

func TestUnit_RaceStanding_NeutralByDefault(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(), racestanding.Config{})
	assert.Equal(t, 0, svc.Get(player, argon))
	assert.False(t, svc.IsWanted(player, argon))
}

func TestUnit_RaceStanding_AdjustAccumulatesAndPersists(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	svc := primed(t, repo, racestanding.Config{})

	got, err := svc.Adjust(context.Background(), player, argon, -5)
	require.NoError(t, err)
	assert.Equal(t, -5, got)

	got, err = svc.Adjust(context.Background(), player, argon, -5)
	require.NoError(t, err)
	assert.Equal(t, -10, got)

	assert.Equal(t, -10, svc.Get(player, argon))
	assert.Equal(t, -10, repo.rows[key{player, argon}], "standing persisted")
}

func TestUnit_RaceStanding_WantedAtThreshold(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(), racestanding.Config{WantedThreshold: -10})

	_, err := svc.Adjust(context.Background(), player, argon, -9)
	require.NoError(t, err)
	assert.False(t, svc.IsWanted(player, argon), "above threshold not wanted yet")

	_, err = svc.Adjust(context.Background(), player, argon, -1)
	require.NoError(t, err)
	assert.True(t, svc.IsWanted(player, argon), "at threshold is wanted")

	// A different race is unaffected.
	assert.False(t, svc.IsWanted(player, boron))
}

func TestUnit_RaceStanding_DecayTowardNeutral(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(
		racestanding.Row{Player: player, Race: argon, Standing: -3},
		racestanding.Row{Player: player, Race: boron, Standing: 2},
	)
	svc := primed(t, repo, racestanding.Config{DecayStep: 1})

	require.NoError(t, svc.Decay(context.Background()))
	assert.Equal(t, -2, svc.Get(player, argon), "negative decays up toward 0")
	assert.Equal(t, 1, svc.Get(player, boron), "positive decays down toward 0")

	// Decaying past 0 settles exactly at neutral, not overshooting.
	for i := 0; i < 5; i++ {
		require.NoError(t, svc.Decay(context.Background()))
	}
	assert.Equal(t, 0, svc.Get(player, argon))
	assert.Equal(t, 0, svc.Get(player, boron))
}

func TestUnit_RaceStanding_SnapshotForPlayer(t *testing.T) {
	t.Parallel()
	svc := primed(t, newFakeRepo(
		racestanding.Row{Player: player, Race: argon, Standing: -4},
		racestanding.Row{Player: player, Race: boron, Standing: 3},
		racestanding.Row{Player: 99, Race: argon, Standing: -1},
	), racestanding.Config{})

	snap := svc.SnapshotForPlayer(player)
	assert.Equal(t, map[domain.RaceID]int{argon: -4, boron: 3}, snap)
}
