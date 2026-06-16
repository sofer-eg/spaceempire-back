package api_test

import (
	"bytes"
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
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/production"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/sector"
	"spaceempire/back/internal/trade"
)

// stubTradeService implements api.TradeService. Each method returns the
// pre-set Err, falling back to the pre-set BuyResult / SellResult / market
// on success. Captures the last call for assertion convenience.
type stubTradeService struct {
	marketEntries []traderepo.MarketEntry
	marketErr     error
	buyResult     trade.BuyResult
	buyErr        error
	sellResult    trade.SellResult
	sellErr       error

	lastMarketOwner domain.EntityRef
	lastDockedShip  domain.ShipID
	lastBuyPlayer   domain.PlayerID
	lastBuyShip     domain.ShipID
	lastBuyStation  domain.EntityRef
	lastBuyType     domain.GoodsTypeID
	lastBuyQty      int64
	lastSellPlayer  domain.PlayerID
	lastSellShip    domain.ShipID
	lastSellStation domain.EntityRef
	lastSellType    domain.GoodsTypeID
	lastSellQty     int64
}

func (s *stubTradeService) MarketDocked(_ context.Context, _ domain.PlayerID, shipID domain.ShipID, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	s.lastMarketOwner = owner
	s.lastDockedShip = shipID
	if s.marketErr != nil {
		return nil, s.marketErr
	}
	return s.marketEntries, nil
}

func (s *stubTradeService) Market(_ context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error) {
	s.lastMarketOwner = owner
	if s.marketErr != nil {
		return nil, s.marketErr
	}
	return s.marketEntries, nil
}

func (s *stubTradeService) Buy(_ context.Context, playerID domain.PlayerID, shipID domain.ShipID, station domain.EntityRef, gtype domain.GoodsTypeID, qty int64) (trade.BuyResult, error) {
	s.lastBuyPlayer, s.lastBuyShip, s.lastBuyStation, s.lastBuyType, s.lastBuyQty = playerID, shipID, station, gtype, qty
	if s.buyErr != nil {
		return trade.BuyResult{}, s.buyErr
	}
	return s.buyResult, nil
}

func (s *stubTradeService) Sell(_ context.Context, playerID domain.PlayerID, shipID domain.ShipID, station domain.EntityRef, gtype domain.GoodsTypeID, qty int64) (trade.SellResult, error) {
	s.lastSellPlayer, s.lastSellShip, s.lastSellStation, s.lastSellType, s.lastSellQty = playerID, shipID, station, gtype, qty
	if s.sellErr != nil {
		return trade.SellResult{}, s.sellErr
	}
	return s.sellResult, nil
}

// marketRouter is a minimal api.SectorRouter for the dock-gated market tests:
// it resolves any player to a fixed primary ship so resolveActiveShip succeeds
// and the handler reaches MarketDocked. It only needs the two lookup methods;
// the rest are no-ops.
type marketRouter struct {
	primaryShip domain.ShipID
}

func (marketRouter) Send(_ domain.SectorID, _ sector.Command) error { return nil }
func (marketRouter) Snapshot(_ domain.SectorID) sector.Snapshot     { return sector.Snapshot{} }
func (marketRouter) Subscribe(_ context.Context, _ domain.SectorID, _ domain.PlayerID) (*sector.Subscription, func(), error) {
	return &sector.Subscription{}, func() {}, nil
}
func (marketRouter) LookupShipSector(_ domain.ShipID) (domain.SectorID, bool) { return 1, true }
func (r marketRouter) LookupPrimaryShipByPlayer(_ domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	if r.primaryShip == 0 {
		return 0, 0, false
	}
	return r.primaryShip, 1, true
}

// newTradeServer wires a minimal api.Server around the stub. A marketRouter
// resolves a primary ship (id 7) so the dock-gated market handler reaches the
// service; the buy/sell handlers ignore it. Auth middleware is left off — the
// market tests inject the player id directly via authedMarketRequest.
func newTradeServer(t *testing.T, stub *stubTradeService) *api.Server {
	t.Helper()
	return api.NewServer(marketRouter{primaryShip: 7}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		Trade:            stub,
	}, nil)
}

// authedMarketRequest builds a GET request to path with a player id stamped on
// the context, the way RequireAuth would in production. The market handler
// reads it to resolve the active ship.
func authedMarketRequest(path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	return req.WithContext(auth.ContextWithPlayerID(req.Context(), domain.PlayerID(42)))
}

func TestUnit_Market_ReturnsEntries(t *testing.T) {
	t.Parallel()
	buy, sell := int64(40), int64(80)
	stub := &stubTradeService{
		marketEntries: []traderepo.MarketEntry{
			{
				Owner:     domain.EntityRef{Kind: domain.EntityKindStation, ID: 11},
				GoodsType: 1,
				BuyPrice:  &buy,
				SellPrice: &sell,
				Stock:     100,
				MaxStock:  500,
			},
		},
	}
	srv := newTradeServer(t, stub)

	req := authedMarketRequest("/api/station/11/market")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.MarketResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, int(domain.EntityKindStation), resp.OwnerKind)
	assert.EqualValues(t, 11, resp.OwnerID)
	require.Len(t, resp.Items, 1)
	assert.EqualValues(t, 1, resp.Items[0].TypeID)
	require.NotNil(t, resp.Items[0].BuyPrice)
	require.NotNil(t, resp.Items[0].SellPrice)
	assert.EqualValues(t, 40, *resp.Items[0].BuyPrice)
	assert.EqualValues(t, 80, *resp.Items[0].SellPrice)
	// The handler must have resolved the docked ship before reading the market.
	assert.EqualValues(t, 7, stub.lastDockedShip)
}

func TestUnit_Market_NotDocked_PropagatesError(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{marketErr: trade.ErrNotDocked}
	srv := newTradeServer(t, stub)

	req := authedMarketRequest("/api/station/11/market")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_Market_NoActiveShip_Returns400(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{}
	// A router that resolves no primary ship → resolveActiveShip fails.
	srv := api.NewServer(marketRouter{primaryShip: 0}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		Trade:            stub,
	}, nil)

	req := authedMarketRequest("/api/station/11/market")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_Market_InvalidID(t *testing.T) {
	t.Parallel()
	srv := newTradeServer(t, &stubTradeService{})
	req := authedMarketRequest("/api/station/abc/market")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_Market_TradeUnavailable_Returns503(t *testing.T) {
	t.Parallel()
	srv := api.NewServer(nil, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/station/1/market", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// stubProductionReader implements api.StationProductionReader. called records
// whether the handler consulted it (used to assert non-station markets skip it).
type stubProductionReader struct {
	info   production.CycleInfo
	err    error
	called bool
}

func (s *stubProductionReader) StationCycle(_ context.Context, _ domain.StationID) (production.CycleInfo, error) {
	s.called = true
	if s.err != nil {
		return production.CycleInfo{}, s.err
	}
	return s.info, nil
}

func TestUnit_Market_Station_IncludesProduction(t *testing.T) {
	t.Parallel()
	sell := int64(80)
	tradeStub := &stubTradeService{
		marketEntries: []traderepo.MarketEntry{
			{Owner: domain.EntityRef{Kind: domain.EntityKindStation, ID: 11}, GoodsType: 7, SellPrice: &sell, Stock: 10, MaxStock: 100},
		},
	}
	prod := &stubProductionReader{info: production.CycleInfo{
		Produces:    true,
		InProgress:  true,
		NextCycleAt: time.Now().Add(30 * time.Second),
		CycleTime:   60 * time.Second,
	}}
	srv := api.NewServer(marketRouter{primaryShip: 7}, api.Config{
		SnapshotInterval:  10 * time.Millisecond,
		AckTimeout:        time.Second,
		SectorID:          1,
		Trade:             tradeStub,
		StationProduction: prod,
	}, nil)

	req := authedMarketRequest("/api/station/11/market")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.MarketResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotNil(t, resp.Production)
	assert.True(t, resp.Production.InProgress)
	assert.InDelta(t, 60.0, resp.Production.CycleSeconds, 0.001)
	assert.Greater(t, resp.Production.SecondsRemaining, 0.0)
	assert.LessOrEqual(t, resp.Production.SecondsRemaining, 30.0)
}

func TestUnit_Market_TradeStation_OmitsProduction(t *testing.T) {
	t.Parallel()
	buy := int64(40)
	tradeStub := &stubTradeService{
		marketEntries: []traderepo.MarketEntry{
			{Owner: domain.EntityRef{Kind: domain.EntityKindTradeStation, ID: 3}, GoodsType: 7, BuyPrice: &buy, Stock: 10, MaxStock: 100},
		},
	}
	prod := &stubProductionReader{info: production.CycleInfo{Produces: true}}
	srv := api.NewServer(marketRouter{primaryShip: 7}, api.Config{
		SnapshotInterval:  10 * time.Millisecond,
		AckTimeout:        time.Second,
		SectorID:          1,
		Trade:             tradeStub,
		StationProduction: prod,
	}, nil)

	req := authedMarketRequest("/api/trade-station/3/market")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.MarketResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Nil(t, resp.Production)
	assert.False(t, prod.called, "production reader must not be consulted for trade-station market")
}

func TestUnit_TradeBuy_Success(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{
		buyResult: trade.BuyResult{NewCash: 9000, NewStock: 80, UnitPrice: 50, TotalAmount: 1000},
	}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeBuyRequest{
		ShipID:   7,
		Station:  dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 11},
		TypeID:   1,
		Quantity: 20,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/buy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.TradeBuyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.EqualValues(t, 9000, resp.NewCash)
	assert.EqualValues(t, 80, resp.NewStock)
	assert.EqualValues(t, 20, resp.Moved)
	assert.EqualValues(t, 50, resp.UnitPrice)
	assert.EqualValues(t, 1000, resp.TotalAmount)

	assert.EqualValues(t, 7, stub.lastBuyShip)
	assert.Equal(t, domain.EntityKindStation, stub.lastBuyStation.Kind)
	assert.EqualValues(t, 1, stub.lastBuyType)
	assert.EqualValues(t, 20, stub.lastBuyQty)
}

func TestUnit_TradeBuy_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()
	srv := newTradeServer(t, &stubTradeService{})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/buy", bytes.NewReader([]byte("not-json")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_TradeBuy_MissingFields_Returns400(t *testing.T) {
	t.Parallel()
	srv := newTradeServer(t, &stubTradeService{})
	body, _ := json.Marshal(dto.TradeBuyRequest{ShipID: 1, Station: dto.EntityRef{Kind: 2, ID: 1}, TypeID: 1, Quantity: 0})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/buy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_TradeBuy_InsufficientCash_Returns402(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{buyErr: trade.ErrInsufficientCash}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeBuyRequest{
		ShipID: 1, Station: dto.EntityRef{Kind: 2, ID: 1}, TypeID: 1, Quantity: 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/buy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusPaymentRequired, rec.Code)
}

func TestUnit_TradeBuy_NotDocked_Returns400(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{buyErr: trade.ErrNotDocked}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeBuyRequest{
		ShipID: 1, Station: dto.EntityRef{Kind: 2, ID: 1}, TypeID: 1, Quantity: 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/buy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_TradeBuy_NoCargoSpace_Returns400(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{buyErr: trade.ErrNoCargoSpace}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeBuyRequest{
		ShipID: 1, Station: dto.EntityRef{Kind: 2, ID: 1}, TypeID: 1, Quantity: 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/buy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_TradeBuy_StationNotOffer_Returns404(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{buyErr: trade.ErrMarketEntryNotFound}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeBuyRequest{
		ShipID: 1, Station: dto.EntityRef{Kind: 2, ID: 1}, TypeID: 1, Quantity: 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/buy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnit_TradeSell_Success(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{
		sellResult: trade.SellResult{NewCash: 11000, NewStock: 110, UnitPrice: 30, TotalAmount: 600},
	}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeSellRequest{
		ShipID:   7,
		Station:  dto.EntityRef{Kind: int(domain.EntityKindTradeStation), ID: 3},
		TypeID:   2,
		Quantity: 20,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/sell", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.TradeSellResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.EqualValues(t, 11000, resp.NewCash)
	assert.EqualValues(t, 110, resp.NewStock)
	assert.EqualValues(t, 20, resp.Moved)
	assert.EqualValues(t, 600, resp.TotalAmount)

	assert.EqualValues(t, 7, stub.lastSellShip)
	assert.Equal(t, domain.EntityKindTradeStation, stub.lastSellStation.Kind)
}

func TestUnit_TradeSell_InsufficientCargo_Returns400(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{sellErr: trade.ErrInsufficientCargo}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeSellRequest{
		ShipID: 1, Station: dto.EntityRef{Kind: 2, ID: 1}, TypeID: 1, Quantity: 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/sell", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_TradeSell_StationOverflow_Returns400(t *testing.T) {
	t.Parallel()
	stub := &stubTradeService{sellErr: trade.ErrStockOverflow}
	srv := newTradeServer(t, stub)
	body, _ := json.Marshal(dto.TradeSellRequest{
		ShipID: 1, Station: dto.EntityRef{Kind: 2, ID: 1}, TypeID: 1, Quantity: 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/trade/sell", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
