package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/reference/contraband"
	"spaceempire/back/internal/sector"
)

// --- fakes -----------------------------------------------------------------

// fakeWanted is an in-memory wanted oracle keyed by (player, race).
type fakeWanted map[[2]int64]bool

func (f fakeWanted) IsWanted(player domain.PlayerID, race domain.RaceID) bool {
	return f[[2]int64{int64(player), int64(race)}]
}

// fakeCargo is a one-ship in-memory hold for scanner tests.
type fakeCargo struct {
	items    []domain.CargoItem
	consumed []consumed
}

type consumed struct {
	gtype domain.GoodsTypeID
	qty   int64
}

func (f *fakeCargo) Inventory(_ context.Context, _ domain.EntityRef, _ domain.PlayerID) (domain.Inventory, error) {
	return domain.Inventory{Items: f.items}, nil
}

func (f *fakeCargo) Consume(_ context.Context, _ domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	f.consumed = append(f.consumed, consumed{gtype, qty})
	return nil
}

// fakeStanding is an in-memory standing store for scanner tests.
type fakeStanding struct {
	cur       map[[2]int64]int
	threshold int
	adjusts   []adjust
}

type adjust struct {
	player domain.PlayerID
	race   domain.RaceID
	delta  int
}

func newFakeStanding(threshold int) *fakeStanding {
	return &fakeStanding{cur: map[[2]int64]int{}, threshold: threshold}
}

func (f *fakeStanding) Adjust(_ context.Context, player domain.PlayerID, race domain.RaceID, delta int) (int, error) {
	f.adjusts = append(f.adjusts, adjust{player, race, delta})
	k := [2]int64{int64(player), int64(race)}
	f.cur[k] += delta
	return f.cur[k], nil
}

func (f *fakeStanding) IsWanted(player domain.PlayerID, race domain.RaceID) bool {
	return f.cur[[2]int64{int64(player), int64(race)}] <= f.threshold
}

// --- scanner ---------------------------------------------------------------

func TestUnit_PoliceScanner_ConfiscatesContrabandAndDropsStanding(t *testing.T) {
	t.Parallel()
	cargo := &fakeCargo{items: []domain.CargoItem{
		{GoodsType: 1, Quantity: 50},                // ordinary goods — kept
		{GoodsType: contraband.Slaves, Quantity: 7}, // contraband — confiscated
	}}
	standing := newFakeStanding(-10)
	scanner := policeScanner{cargo: cargo, standing: standing, npc: 99, cfg: PoliceScanConfig{}.withDefaults()}

	res, err := scanner.Scan(context.Background(), 1, 5, 100)
	require.NoError(t, err)

	assert.True(t, res.Confiscated)
	assert.Equal(t, contraband.Slaves, res.GoodsType)
	assert.EqualValues(t, 7, res.Quantity)
	assert.Equal(t, []consumed{{contraband.Slaves, 7}}, cargo.consumed, "only contraband is confiscated")
	require.Len(t, standing.adjusts, 1)
	assert.Equal(t, adjust{player: 100, race: 1, delta: -5}, standing.adjusts[0])
}

func TestUnit_PoliceScanner_CleanHoldNoActionNoStandingChange(t *testing.T) {
	t.Parallel()
	cargo := &fakeCargo{items: []domain.CargoItem{{GoodsType: 1, Quantity: 50}}}
	standing := newFakeStanding(-10)
	scanner := policeScanner{cargo: cargo, standing: standing, npc: 99, cfg: PoliceScanConfig{}.withDefaults()}

	res, err := scanner.Scan(context.Background(), 1, 5, 100)
	require.NoError(t, err)

	assert.False(t, res.Confiscated)
	assert.Empty(t, cargo.consumed)
	assert.Empty(t, standing.adjusts, "a clean hold never touches standing")
}

func TestUnit_PoliceScanner_SkipsNPCAndZeroPlayer(t *testing.T) {
	t.Parallel()
	cargo := &fakeCargo{items: []domain.CargoItem{{GoodsType: contraband.Slaves, Quantity: 7}}}
	standing := newFakeStanding(-10)
	scanner := policeScanner{cargo: cargo, standing: standing, npc: 99, cfg: PoliceScanConfig{}.withDefaults()}

	for _, player := range []domain.PlayerID{0, 99} {
		res, err := scanner.Scan(context.Background(), 1, 5, player)
		require.NoError(t, err)
		assert.False(t, res.Confiscated)
	}
	assert.Empty(t, cargo.consumed, "NPC / zero-player ships are not inspected")
}

func TestUnit_PoliceScanner_OnRaceShipKilledDropsStanding(t *testing.T) {
	t.Parallel()
	standing := newFakeStanding(-10)
	scanner := policeScanner{standing: standing, npc: 99, cfg: PoliceScanConfig{}.withDefaults()}

	require.NoError(t, scanner.OnRaceShipKilled(context.Background(), 100, 2))
	require.Len(t, standing.adjusts, 1)
	assert.Equal(t, adjust{player: 100, race: 2, delta: -10}, standing.adjusts[0])

	// NPC / zero killers are ignored.
	require.NoError(t, scanner.OnRaceShipKilled(context.Background(), 99, 2))
	require.NoError(t, scanner.OnRaceShipKilled(context.Background(), 0, 2))
	require.Len(t, standing.adjusts, 1, "no standing change for NPC/zero killer")
}

// --- wanted overlay targeter -----------------------------------------------

func TestUnit_WantedOverlay_MainRaceAttacksWantedPlayer(t *testing.T) {
	t.Parallel()
	wanted := fakeWanted{{100, 1}: true}
	tg := wantedOverlayTargeter{base: raceMatrixTargeter{}, standing: wanted, npc: 99}

	argon := domain.Ship{Race: 1}
	wantedPlayer := domain.Ship{Race: 0, PlayerID: 100}
	cleanPlayer := domain.Ship{Race: 0, PlayerID: 101}

	assert.True(t, tg.IsHostile(argon, wantedPlayer), "Argon navy attacks a wanted player")
	assert.False(t, tg.IsHostile(argon, cleanPlayer), "a clean player is left alone")
}

func TestUnit_WantedOverlay_DelegatesToBaseMatrix(t *testing.T) {
	t.Parallel()
	tg := wantedOverlayTargeter{base: raceMatrixTargeter{}, standing: fakeWanted{}, npc: 99}

	// Base matrix hostility still holds without any standing.
	assert.True(t, tg.IsHostile(domain.Ship{Race: 6}, domain.Ship{Race: 0, PlayerID: 100}), "pirate raids player")
	assert.True(t, tg.IsHostile(domain.Ship{Race: 1}, domain.Ship{Race: 7}), "Argon attacks Xenon")
	assert.False(t, tg.IsHostile(domain.Ship{Race: 1}, domain.Ship{Race: 2}), "allies stay friendly")
}

func TestUnit_WantedOverlay_IgnoresNPCShips(t *testing.T) {
	t.Parallel()
	// Even if some standing key matched the NPC owner, an NPC trader must not
	// become a navy target via the overlay.
	wanted := fakeWanted{{99, 1}: true}
	tg := wantedOverlayTargeter{base: raceMatrixTargeter{}, standing: wanted, npc: 99}

	npcTrader := domain.Ship{Race: 0, PlayerID: 99}
	assert.False(t, tg.IsHostile(domain.Ship{Race: 1}, npcTrader))
}

// guard: the scanner satisfies the sector port.
var _ sector.PoliceScanner = policeScanner{}
