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
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/auction"
	auctionrepo "spaceempire/back/internal/persistence/auction"
)

// stubAuctionService records the latest call so each test can assert the
// handler decoded its inputs correctly. Each method's error path falls
// back to a preset error, mirroring stubTradeService in trade_test.go.
type stubAuctionService struct {
	listLots []auctionrepo.Lot
	listErr  error

	createOut auctionrepo.Lot
	createErr error
	createIn  auction.CreateParams

	bidOut  auction.BidResult
	bidErr  error
	bidLot  int64
	bidAmt  int64
	bidder  domain.PlayerID
	bidShip domain.ShipID
}

func (s *stubAuctionService) ListActive(_ context.Context) ([]auctionrepo.Lot, error) {
	return s.listLots, s.listErr
}

func (s *stubAuctionService) MyLots(_ context.Context, _ domain.PlayerID) ([]auctionrepo.Lot, error) {
	return s.listLots, s.listErr
}

func (s *stubAuctionService) Create(_ context.Context, p auction.CreateParams) (auctionrepo.Lot, error) {
	s.createIn = p
	if s.createErr != nil {
		return auctionrepo.Lot{}, s.createErr
	}
	return s.createOut, nil
}

func (s *stubAuctionService) Bid(_ context.Context, bidder domain.PlayerID, shipID domain.ShipID, lotID int64, amount int64) (auction.BidResult, error) {
	s.bidder, s.bidShip, s.bidLot, s.bidAmt = bidder, shipID, lotID, amount
	if s.bidErr != nil {
		return auction.BidResult{}, s.bidErr
	}
	return s.bidOut, nil
}

func newAuctionServer(t *testing.T, stub *stubAuctionService) *api.Server {
	t.Helper()
	return api.NewServer(nil, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
		Auction:          stub,
	}, nil)
}

func TestUnit_AuctionList_ReturnsLots(t *testing.T) {
	t.Parallel()
	stub := &stubAuctionService{
		listLots: []auctionrepo.Lot{
			{
				ID:           1,
				SellerID:     7,
				GoodsType:    2,
				Quantity:     10,
				Source:       domain.EntityRef{Kind: domain.EntityKindShip, ID: 100},
				StartPrice:   100,
				CurrentPrice: 100,
				EndsAt:       time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
				Status:       auctionrepo.StatusActive,
			},
		},
	}
	srv := newAuctionServer(t, stub)
	req := httptest.NewRequest(http.MethodGet, "/api/auction", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AuctionListResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Lots, 1)
	assert.Equal(t, int64(1), resp.Lots[0].ID)
	assert.Equal(t, int32(2), resp.Lots[0].GoodsTypeID)
}

func TestUnit_AuctionCreate_PassesParams(t *testing.T) {
	t.Parallel()
	stub := &stubAuctionService{
		createOut: auctionrepo.Lot{ID: 42},
	}
	srv := newAuctionServer(t, stub)

	body := dto.AuctionCreateRequest{
		Source:          dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 100},
		GoodsTypeID:     2,
		Quantity:        10,
		StartPrice:      100,
		DurationSeconds: 3600,
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auction", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, int64(10), stub.createIn.Quantity)
	assert.Equal(t, int64(100), stub.createIn.StartPrice)
	assert.Equal(t, time.Hour, stub.createIn.Duration)
	assert.Equal(t, domain.EntityKindShip, stub.createIn.Source.Kind)
}

func TestUnit_AuctionCreate_RejectsBadKind(t *testing.T) {
	t.Parallel()
	stub := &stubAuctionService{}
	srv := newAuctionServer(t, stub)
	body := dto.AuctionCreateRequest{
		Source:          dto.EntityRef{Kind: int(domain.EntityKindPirbase), ID: 1},
		GoodsTypeID:     2,
		Quantity:        1,
		StartPrice:      1,
		DurationSeconds: 60,
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/auction", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_AuctionBid_HappyPath(t *testing.T) {
	t.Parallel()
	stub := &stubAuctionService{bidOut: auction.BidResult{NewPrice: 200, NewLeader: true}}
	srv := newAuctionServer(t, stub)
	body, _ := json.Marshal(dto.AuctionBidRequest{ShipID: 100, Amount: 200})
	req := httptest.NewRequest(http.MethodPost, "/api/auction/42/bid", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AuctionBidResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, int64(200), resp.NewPrice)
	assert.True(t, resp.NewLeader)
	assert.Equal(t, int64(42), stub.bidLot)
	assert.Equal(t, domain.ShipID(100), stub.bidShip)
}

func TestUnit_AuctionBid_RejectsMissingShip(t *testing.T) {
	t.Parallel()
	stub := &stubAuctionService{}
	srv := newAuctionServer(t, stub)
	body, _ := json.Marshal(dto.AuctionBidRequest{Amount: 200}) // no shipID
	req := httptest.NewRequest(http.MethodPost, "/api/auction/42/bid", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_AuctionBid_ErrorMapping(t *testing.T) {
	t.Parallel()
	cases := map[error]int{
		auction.ErrBidTooLow:        http.StatusBadRequest,
		auction.ErrInsufficientCash: http.StatusPaymentRequired,
		auction.ErrLotNotActive:     http.StatusGone,
		auction.ErrLotNotFound:      http.StatusNotFound,
		auction.ErrSellerBid:        http.StatusBadRequest,
		auction.ErrNotDocked:        http.StatusConflict,
		auction.ErrForbidden:        http.StatusForbidden,
		auction.ErrShipNotFound:     http.StatusNotFound,
		auctionrepo.ErrLotNotFound:  http.StatusNotFound,
		auctionrepo.ErrLotNotActive: http.StatusGone,
	}
	for err, code := range cases {
		t.Run(err.Error(), func(t *testing.T) {
			stub := &stubAuctionService{bidErr: err}
			srv := newAuctionServer(t, stub)
			body, _ := json.Marshal(dto.AuctionBidRequest{ShipID: 100, Amount: 200})
			req := httptest.NewRequest(http.MethodPost, "/api/auction/42/bid", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			assert.Equal(t, code, rec.Code, "for error %v", err)
		})
	}
}
