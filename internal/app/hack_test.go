package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/trade"
)

// fakeHackMarket is a stub trade.Service.Rob for robber tests.
type fakeHackMarket struct {
	out        trade.RobOutcome
	err        error
	calls      int
	gotStation domain.EntityRef
	gotShip    domain.EntityRef
	gotDeposit bool
}

func (f *fakeHackMarket) Rob(_ context.Context, station, ship domain.EntityRef, _, _, _ float64, deposit bool) (trade.RobOutcome, error) {
	f.calls++
	f.gotStation = station
	f.gotShip = ship
	f.gotDeposit = deposit
	return f.out, f.err
}

func hackCfg() HackConfig {
	return HackConfig{RobFraction: 0.15, DamageFraction: 0.05, GoodsMinFraction: 0.3, ReputationPenalty: 50}
}

// A successful raid maps the market outcome and drops the hacker's standing with
// the station's race in proportion to the fraction taken:
// (150+50)/1000 * 50 = 10.
func TestUnit_StationRobber_AppliesProportionalStandingPenalty(t *testing.T) {
	t.Parallel()
	market := &fakeHackMarket{out: trade.RobOutcome{
		GoodsType: 2, Robbed: 150, Damaged: 50, Delivered: true, MaxStock: 1000,
	}}
	standing := newFakeStanding(-10)
	r := stationRobber{market: market, standing: standing, cfg: hackCfg(), npc: 99}

	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}
	station := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 3}
	res, err := r.Rob(context.Background(), station, 1, 100, ship, true)
	require.NoError(t, err)

	assert.Equal(t, sector.RobResult{GoodsType: 2, Robbed: 150, Damaged: 50, Delivered: true}, res)
	assert.True(t, market.gotDeposit)
	assert.Equal(t, station, market.gotStation)
	require.Len(t, standing.adjusts, 1)
	assert.Equal(t, adjust{player: 100, race: 1, delta: -10}, standing.adjusts[0])
}

// AC-4: on a realistically-filled production station (max_stock 10000, ~47%
// full → robbed 705 + damaged 235), the standing penalty does NOT round to zero:
// (705+235)/10000 * 50 = round(4.7) = 5.
func TestUnit_StationRobber_ProductionStation_PenaltyNonZero(t *testing.T) {
	t.Parallel()
	market := &fakeHackMarket{out: trade.RobOutcome{
		GoodsType: 1, Robbed: 705, Damaged: 235, Delivered: false, MaxStock: 10000,
	}}
	standing := newFakeStanding(-10)
	r := stationRobber{market: market, standing: standing, cfg: hackCfg(), npc: 99}

	// Production station (owner_kind 2), Argon race 1.
	station := domain.EntityRef{Kind: domain.EntityKindStation, ID: 2}
	_, err := r.Rob(context.Background(), station, 1, 100,
		domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}, false)
	require.NoError(t, err)
	require.Len(t, standing.adjusts, 1, "a real haul drops standing")
	assert.Equal(t, adjust{player: 100, race: 1, delta: -5}, standing.adjusts[0])
}

// trade.ErrTooLittleGoods is translated to the sector sentinel (which the worker
// turns into a clean 422), and no standing is touched.
func TestUnit_StationRobber_TooLittleGoods_MapsSentinel(t *testing.T) {
	t.Parallel()
	market := &fakeHackMarket{err: trade.ErrTooLittleGoods}
	standing := newFakeStanding(-10)
	r := stationRobber{market: market, standing: standing, cfg: hackCfg(), npc: 99}

	_, err := r.Rob(context.Background(), domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 3},
		1, 100, domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}, true)
	assert.ErrorIs(t, err, sector.ErrHackTooLittleGoods)
	assert.Empty(t, standing.adjusts)
}

// A penalty that rounds to zero (tiny fraction, e.g. the live universal-market
// seed's 1e6 max_stock) applies no standing change.
func TestUnit_StationRobber_ZeroPenalty_NoStandingChange(t *testing.T) {
	t.Parallel()
	market := &fakeHackMarket{out: trade.RobOutcome{
		GoodsType: 2, Robbed: 75, Damaged: 25, MaxStock: 1_000_000,
	}}
	standing := newFakeStanding(-10)
	r := stationRobber{market: market, standing: standing, cfg: hackCfg(), npc: 99}

	_, err := r.Rob(context.Background(), domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 3},
		1, 100, domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}, false)
	require.NoError(t, err)
	assert.Empty(t, standing.adjusts, "sub-unit fraction rounds to no penalty")
}

// NPC / non-main-race hackers carry no standing.
func TestUnit_StationRobber_SkipsNPCAndNonMainRace(t *testing.T) {
	t.Parallel()
	out := trade.RobOutcome{GoodsType: 2, Robbed: 150, Damaged: 50, MaxStock: 1000}
	station := domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 3}
	ship := domain.EntityRef{Kind: domain.EntityKindShip, ID: 7}

	// NPC hacker (== npc) → skipped even for a main race.
	market := &fakeHackMarket{out: out}
	standing := newFakeStanding(-10)
	r := stationRobber{market: market, standing: standing, cfg: hackCfg(), npc: 99}
	_, err := r.Rob(context.Background(), station, 1, 99, ship, false)
	require.NoError(t, err)
	assert.Empty(t, standing.adjusts, "NPC hacker carries no standing")

	// Non-main race (7 = Xenon) → no per-player standing.
	market2 := &fakeHackMarket{out: out}
	standing2 := newFakeStanding(-10)
	r2 := stationRobber{market: market2, standing: standing2, cfg: hackCfg(), npc: 99}
	_, err = r2.Rob(context.Background(), station, 7, 100, ship, false)
	require.NoError(t, err)
	assert.Empty(t, standing2.adjusts, "non-main race carries no per-player standing")
}

// guard: the app robber satisfies the sector port.
var _ sector.StationRobber = stationRobber{}
