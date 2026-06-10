package production_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/production"
	traderepo "spaceempire/back/internal/persistence/trade"
)

// stubRepo is a memory-backed production.Repo and TxRunner combined.
// Production runs on the station market (station_goods): each good a station
// trades is one marketRow with a stock and a cap. The tick service touches
// only one station per Tick(); a single goroutine view is enough to mimic
// transactional reads.
type stubRepo struct {
	market     map[domain.EntityRef]map[domain.GoodsTypeID]*marketRow
	production map[domain.StationID]productionRow
}

type marketRow struct {
	stock    int64
	maxStock int64
	output   bool // true = sell row (recipe output); false = buy row (recipe input)
}

type productionRow struct {
	InProgress  bool
	NextCycleAt time.Time
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		market:     make(map[domain.EntityRef]map[domain.GoodsTypeID]*marketRow),
		production: make(map[domain.StationID]productionRow),
	}
}

// put seeds one station_goods row.
func (s *stubRepo) put(ref domain.EntityRef, good domain.GoodsTypeID, stock, maxStock int64, output bool) {
	if s.market[ref] == nil {
		s.market[ref] = make(map[domain.GoodsTypeID]*marketRow)
	}
	s.market[ref][good] = &marketRow{stock: stock, maxStock: maxStock, output: output}
}

// stock reads the current stock of a good (0 when the station does not trade it).
func (s *stubRepo) stock(ref domain.EntityRef, good domain.GoodsTypeID) int64 {
	if r, ok := s.market[ref][good]; ok {
		return r.stock
	}
	return 0
}

func (s *stubRepo) ListMarket(_ context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	rows := s.market[owner]
	out := make([]traderepo.MarketEntry, 0, len(rows))
	for g, r := range rows {
		e := traderepo.MarketEntry{Owner: owner, GoodsType: g, Stock: r.stock, MaxStock: r.maxStock}
		price := int64(1)
		if r.output {
			e.SellPrice = &price
		} else {
			e.BuyPrice = &price
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *stubRepo) AdjustStock(_ context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, delta int64) (int64, error) {
	r, ok := s.market[owner][gtype]
	if !ok {
		return 0, traderepo.ErrMarketEntryNotFound
	}
	n := r.stock + delta
	switch {
	case n < 0:
		return 0, traderepo.ErrInsufficientStock
	case n > r.maxStock:
		return 0, traderepo.ErrStockOverflow
	}
	r.stock = n
	return n, nil
}

func (s *stubRepo) UpdateProduction(_ context.Context, id domain.StationID, inProgress bool, nextCycleAt time.Time) error {
	s.production[id] = productionRow{InProgress: inProgress, NextCycleAt: nextCycleAt}
	return nil
}

func (s *stubRepo) Do(ctx context.Context, fn func(ctx context.Context, txRepo production.Repo) error) error {
	return fn(ctx, s)
}

const (
	stationType = 1
	iron        = domain.GoodsTypeID(2)
	silicon     = domain.GoodsTypeID(3)
	microchip   = domain.GoodsTypeID(7)
)

func buildBalance(t *testing.T) *balance.Balance {
	t.Helper()
	goods := []balance.Goods{
		{ID: iron, Name: "Iron", Space: 1},
		{ID: silicon, Name: "Silicon", Space: 1},
		{ID: microchip, Name: "Microchips", Space: 1},
	}
	recipes := []balance.Recipe{{
		StationType: stationType,
		CycleTime:   time.Second,
		Inputs: []balance.RecipeLine{
			{GoodsType: iron, Quantity: 5},
			{GoodsType: silicon, Quantity: 1},
		},
		Outputs: []balance.RecipeLine{
			{GoodsType: microchip, Quantity: 3, Max: 100},
		},
	}}
	b, err := balance.New(goods, recipes)
	require.NoError(t, err)
	return b
}

func newStation() domain.Station {
	return domain.Station{ID: 42, Type: stationType, Built: true}
}

// seedMicrochipMarket lays down the buy rows (iron, silicon) and the sell row
// (microchip, cap 100) for the microchip recipe.
func seedMicrochipMarket(repo *stubRepo, ref domain.EntityRef, ironStock, siliconStock, microchipStock int64) {
	repo.put(ref, iron, ironStock, 1000, false)
	repo.put(ref, silicon, siliconStock, 1000, false)
	repo.put(ref, microchip, microchipStock, 100, true)
}

func TestUnit_Tick_StartsCycleWhenInputsAvailable(t *testing.T) {
	repo := newStubRepo()
	station := newStation()
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(station.ID)}
	seedMicrochipMarket(repo, stationRef, 10, 2, 0)

	svc, err := production.New(buildBalance(t), repo)
	require.NoError(t, err)

	stations := []domain.Station{station}
	now := time.Unix(1_700_000_000, 0)
	cycles, err := svc.Tick(context.Background(), stations, now)
	require.NoError(t, err)
	require.Equal(t, 0, cycles, "starting a cycle is not yet a completed cycle")
	require.True(t, stations[0].InProgress)
	require.Equal(t, now.Add(time.Second), stations[0].NextCycleAt)

	// Stock is untouched at start — consumed/produced only at cycle end.
	require.EqualValues(t, 10, repo.stock(stationRef, iron))
	require.EqualValues(t, 2, repo.stock(stationRef, silicon))
	require.EqualValues(t, 0, repo.stock(stationRef, microchip))
}

func TestUnit_Tick_SkipsStartWhenInputMissing(t *testing.T) {
	repo := newStubRepo()
	station := newStation()
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(station.ID)}
	seedMicrochipMarket(repo, stationRef, 4, 2, 0) // iron 4 < 5 needed

	svc, err := production.New(buildBalance(t), repo)
	require.NoError(t, err)

	stations := []domain.Station{station}
	cycles, err := svc.Tick(context.Background(), stations, time.Unix(1, 0))
	require.NoError(t, err)
	require.Equal(t, 0, cycles)
	require.False(t, stations[0].InProgress)
	require.Zero(t, repo.production[station.ID])
}

func TestUnit_Tick_SkipsStartWhenOutputCapWouldOverflow(t *testing.T) {
	repo := newStubRepo()
	station := newStation()
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(station.ID)}
	seedMicrochipMarket(repo, stationRef, 10, 2, 99) // sell cap 100, +3 would overflow

	svc, err := production.New(buildBalance(t), repo)
	require.NoError(t, err)

	stations := []domain.Station{station}
	cycles, err := svc.Tick(context.Background(), stations, time.Unix(1, 0))
	require.NoError(t, err)
	require.Equal(t, 0, cycles)
	require.False(t, stations[0].InProgress)
}

func TestUnit_Tick_FinishesCycleAndMovesGoods(t *testing.T) {
	repo := newStubRepo()
	station := newStation()
	station.InProgress = true
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(station.ID)}
	seedMicrochipMarket(repo, stationRef, 10, 2, 0)

	now := time.Unix(1_700_000_000, 0)
	station.NextCycleAt = now.Add(-time.Millisecond) // due in the past

	svc, err := production.New(buildBalance(t), repo)
	require.NoError(t, err)

	stations := []domain.Station{station}
	cycles, err := svc.Tick(context.Background(), stations, now)
	require.NoError(t, err)
	require.Equal(t, 1, cycles)
	require.False(t, stations[0].InProgress)
	require.True(t, stations[0].NextCycleAt.IsZero())

	require.EqualValues(t, 5, repo.stock(stationRef, iron), "consumed 5 iron from the buy stock")
	require.EqualValues(t, 1, repo.stock(stationRef, silicon), "consumed 1 silicon")
	require.EqualValues(t, 3, repo.stock(stationRef, microchip), "produced 3 into the sell stock")
	require.False(t, repo.production[station.ID].InProgress)
}

func TestUnit_Tick_KeepsStuckCycleWhenInputDisappeared(t *testing.T) {
	repo := newStubRepo()
	station := newStation()
	station.InProgress = true
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(station.ID)}
	seedMicrochipMarket(repo, stationRef, 2, 2, 0) // iron 2 < 5

	now := time.Unix(1_700_000_000, 0)
	station.NextCycleAt = now.Add(-time.Second)

	svc, err := production.New(buildBalance(t), repo)
	require.NoError(t, err)

	stations := []domain.Station{station}
	cycles, err := svc.Tick(context.Background(), stations, now)
	require.NoError(t, err)
	require.Equal(t, 0, cycles)
	require.True(t, stations[0].InProgress, "cycle stays in-progress until inputs return")
	require.Equal(t, station.NextCycleAt, stations[0].NextCycleAt)
	require.EqualValues(t, 2, repo.stock(stationRef, iron), "no stock moved while stuck")
}

func TestUnit_Tick_SkipsStationsWithoutRecipe(t *testing.T) {
	repo := newStubRepo()
	station := domain.Station{ID: 99, Type: 7, Built: true} // no recipe

	svc, err := production.New(buildBalance(t), repo)
	require.NoError(t, err)

	stations := []domain.Station{station}
	cycles, err := svc.Tick(context.Background(), stations, time.Unix(1, 0))
	require.NoError(t, err)
	require.Equal(t, 0, cycles)
	require.False(t, stations[0].InProgress)
}

func TestUnit_Tick_SkipsUnbuiltStations(t *testing.T) {
	repo := newStubRepo()
	station := newStation()
	station.Built = false
	stationRef := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(station.ID)}
	seedMicrochipMarket(repo, stationRef, 10, 2, 0)

	svc, err := production.New(buildBalance(t), repo)
	require.NoError(t, err)

	stations := []domain.Station{station}
	cycles, err := svc.Tick(context.Background(), stations, time.Unix(1, 0))
	require.NoError(t, err)
	require.Equal(t, 0, cycles)
	require.False(t, stations[0].InProgress)
}

func TestUnit_New_RejectsNilDeps(t *testing.T) {
	_, err := production.New(nil, newStubRepo())
	require.ErrorIs(t, err, production.ErrNoBalance)
	_, err = production.New(buildBalance(t), nil)
	require.ErrorIs(t, err, production.ErrNoTxRunner)
}

// --- NPC power-plant free-input rule (phase 9.2) ---------------------------

const (
	powerType = 1
	energy    = domain.GoodsTypeID(1) // Батарейки — base energy good
	crystals  = domain.GoodsTypeID(4)
)

func buildPowerPlantBalance(t *testing.T) *balance.Balance {
	t.Helper()
	goods := []balance.Goods{
		{ID: energy, Name: "Energy", Space: 1},
		{ID: crystals, Name: "Crystals", Space: 1},
	}
	recipes := []balance.Recipe{{
		StationType: powerType,
		CycleTime:   time.Second,
		Inputs:      []balance.RecipeLine{{GoodsType: crystals, Quantity: 5}},
		Outputs:     []balance.RecipeLine{{GoodsType: energy, Quantity: 909, Max: 10000}},
	}}
	b, err := balance.New(goods, recipes)
	require.NoError(t, err)
	return b
}

// seedPowerPlantMarket lays down the crystal buy row and the energy sell row.
func seedPowerPlantMarket(repo *stubRepo, ref domain.EntityRef, crystalStock, energyStock int64) {
	repo.put(ref, crystals, crystalStock, 95, false)
	repo.put(ref, energy, energyStock, 10000, true)
}

// runCycle ticks start then finish (one full cycle) and returns the station.
func runCycle(t *testing.T, svc *production.Service, st domain.Station) domain.Station {
	t.Helper()
	stations := []domain.Station{st}
	now := time.Unix(1_700_000_000, 0)
	_, err := svc.Tick(context.Background(), stations, now)
	require.NoError(t, err)
	if stations[0].InProgress {
		_, err = svc.Tick(context.Background(), stations, now.Add(time.Second))
		require.NoError(t, err)
	}
	return stations[0]
}

func TestUnit_Tick_NPCPowerPlant_ProducesWithoutCrystals(t *testing.T) {
	repo := newStubRepo()
	st := domain.Station{ID: 42, Type: powerType, Built: true} // OwnerID nil = NPC
	ref := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(st.ID)}
	seedPowerPlantMarket(repo, ref, 0, 0) // no crystals in stock

	svc, err := production.New(buildPowerPlantBalance(t), repo)
	require.NoError(t, err)

	out := runCycle(t, svc, st)
	require.False(t, out.InProgress, "cycle completed")
	require.EqualValues(t, 909, repo.stock(ref, energy), "NPC plant made energy without crystals")
	require.EqualValues(t, 0, repo.stock(ref, crystals))
}

func TestUnit_Tick_NPCPowerPlant_ConsumesCrystalsWhenPresent(t *testing.T) {
	repo := newStubRepo()
	st := domain.Station{ID: 42, Type: powerType, Built: true}
	ref := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(st.ID)}
	seedPowerPlantMarket(repo, ref, 12, 0)

	svc, err := production.New(buildPowerPlantBalance(t), repo)
	require.NoError(t, err)

	runCycle(t, svc, st)
	require.EqualValues(t, 909, repo.stock(ref, energy))
	require.EqualValues(t, 7, repo.stock(ref, crystals), "5 crystals consumed when present")
}

func TestUnit_Tick_PlayerPowerPlant_IdleWithoutCrystals(t *testing.T) {
	repo := newStubRepo()
	pid := domain.PlayerID(7)
	st := domain.Station{ID: 42, Type: powerType, Built: true, OwnerID: &pid} // player-owned
	ref := domain.EntityRef{Kind: domain.EntityKindStation, ID: int64(st.ID)}
	seedPowerPlantMarket(repo, ref, 0, 0)

	svc, err := production.New(buildPowerPlantBalance(t), repo)
	require.NoError(t, err)

	out := runCycle(t, svc, st)
	require.False(t, out.InProgress, "player plant never started without crystals")
	require.EqualValues(t, 0, repo.stock(ref, energy), "no free production for players")
}
