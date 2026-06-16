package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/sector"
)

// scanRouter serves a single sector whose snapshot holds the player's ship
// (with its equipment) and the static stations to scan. It is enough for the
// scanner's resolveActiveShip → lookupShip → Snapshot(statics) path.
type scanRouter struct {
	sectorID domain.SectorID
	snap     sector.Snapshot
	primary  domain.ShipID
}

func (scanRouter) Send(_ domain.SectorID, _ sector.Command) error { return nil }
func (r scanRouter) Snapshot(_ domain.SectorID) sector.Snapshot   { return r.snap }
func (scanRouter) Subscribe(_ context.Context, _ domain.SectorID, _ domain.PlayerID) (*sector.Subscription, func(), error) {
	return &sector.Subscription{}, func() {}, nil
}
func (r scanRouter) LookupShipSector(_ domain.ShipID) (domain.SectorID, bool) {
	return r.sectorID, true
}
func (r scanRouter) LookupPrimaryShipByPlayer(_ domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	if r.primary == 0 {
		return 0, 0, false
	}
	return r.primary, r.sectorID, true
}

// scanGoods is a GoodsCatalog stub: AllGoods is unused by the scanner; Get
// supplies the [avg, max] band used for the price tier.
type scanGoodsCatalog struct {
	bands map[domain.GoodsTypeID]balance.Goods
}

func (c scanGoodsCatalog) AllGoods() []balance.Goods { return nil }
func (c scanGoodsCatalog) Get(id domain.GoodsTypeID) (balance.Goods, bool) {
	g, ok := c.bands[id]
	return g, ok
}

// newScanServer wires a server whose active ship (id 1, owned by player 42)
// carries the given equipment and sits in a sector with one tradeable station
// offering one good (sell=80) at stock 30/100. The good's band is [16, 96].
func newScanServer(t *testing.T, equipment []domain.InstalledEquipment) *api.Server {
	t.Helper()
	const gtype = domain.GoodsTypeID(1)
	sell := int64(80)
	stub := &stubTradeService{
		marketEntries: []traderepo.MarketEntry{
			{
				Owner:     domain.EntityRef{Kind: domain.EntityKindStation, ID: 11},
				GoodsType: gtype,
				SellPrice: &sell,
				Stock:     30,
				MaxStock:  100,
			},
		},
	}
	snap := sector.Snapshot{
		SectorID: 1,
		Ships: []domain.Ship{
			{ID: 1, PlayerID: 42, SectorID: 1, Equipment: equipment},
		},
		Statics: domain.SectorStatics{
			Stations: []domain.Station{
				{ID: 11, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 200}, Built: true},
			},
		},
	}
	router := scanRouter{sectorID: 1, snap: snap, primary: 1}
	goods := scanGoodsCatalog{bands: map[domain.GoodsTypeID]balance.Goods{
		gtype: {ID: gtype, Name: "Батарейки", AvgPrice: 16, MaxPrice: 96},
	}}
	return api.NewServer(router, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		Trade:            stub,
		Goods:            goods,
	}, nil)
}

func doScan(t *testing.T, srv *api.Server) (*httptest.ResponseRecorder, dto.ScanResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/market-scan", nil)
	req = req.WithContext(auth.ContextWithPlayerID(req.Context(), domain.PlayerID(42)))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var resp dto.ScanResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}
	return rec, resp
}

func TestUnit_MarketScan_Level0_Forbidden(t *testing.T) {
	t.Parallel()
	srv := newScanServer(t, nil) // no trade_up fitted
	rec, _ := doScan(t, srv)
	assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_MarketScan_Level1_TierOnly_PricesAndStockMaskedToZero(t *testing.T) {
	t.Parallel()
	srv := newScanServer(t, []domain.InstalledEquipment{{Type: "trade_up", Level: 1}})
	rec, resp := doScan(t, srv)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, 1, resp.Level)
	require.Len(t, resp.Stations, 1)
	st := resp.Stations[0]
	assert.Equal(t, "Станция", st.Name)
	assert.Equal(t, int(domain.EntityKindStation), st.Owner.Kind)
	assert.EqualValues(t, 11, st.Owner.ID)
	assert.InDelta(t, 100, st.Pos.X, 0.001)
	require.Len(t, st.Goods, 1)
	g := st.Goods[0]
	assert.EqualValues(t, 1, g.TypeID)
	// price 80 in band [16,96]: cuts at 16+80/3≈42 and 16+160/3≈69 → high.
	assert.Equal(t, "high", g.PriceLevel)
	assert.Zero(t, g.BuyPrice, "real prices masked at level 1")
	assert.Zero(t, g.SellPrice, "real prices masked at level 1")
	assert.Zero(t, g.Stock, "stock masked at level 1")
}

func TestUnit_MarketScan_Level2_RevealsPrices_StockStillMasked(t *testing.T) {
	t.Parallel()
	srv := newScanServer(t, []domain.InstalledEquipment{{Type: "trade_up", Level: 2}})
	rec, resp := doScan(t, srv)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, 2, resp.Level)
	require.Len(t, resp.Stations, 1)
	g := resp.Stations[0].Goods[0]
	assert.Equal(t, "high", g.PriceLevel)
	assert.EqualValues(t, 80, g.SellPrice, "level 2 reveals the real sell price")
	assert.Zero(t, g.BuyPrice, "the station does not buy this good")
	assert.Zero(t, g.Stock, "stock still masked at level 2")
}

func TestUnit_MarketScan_Level3_RevealsStock(t *testing.T) {
	t.Parallel()
	srv := newScanServer(t, []domain.InstalledEquipment{{Type: "trade_up", Level: 3}})
	rec, resp := doScan(t, srv)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, 3, resp.Level)
	require.Len(t, resp.Stations, 1)
	g := resp.Stations[0].Goods[0]
	assert.EqualValues(t, 80, g.SellPrice)
	assert.EqualValues(t, 30, g.Stock, "level 3 reveals the on-hand stock")
}

func TestUnit_MarketScan_NoActiveShip_Returns400(t *testing.T) {
	t.Parallel()
	srv := newScanServer(t, []domain.InstalledEquipment{{Type: "trade_up", Level: 1}})
	req := httptest.NewRequest(http.MethodGet, "/api/market-scan", nil) // no player id
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}
