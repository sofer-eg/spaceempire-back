package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/auction"
	auctionrepo "spaceempire/back/internal/persistence/auction"
)

// AuctionService is the slice of *auction.Service the HTTP layer needs.
// Declared per ISP so handler tests can stub it.
type AuctionService interface {
	ListActive(ctx context.Context) ([]auctionrepo.Lot, error)
	MyLots(ctx context.Context, player domain.PlayerID) ([]auctionrepo.Lot, error)
	Create(ctx context.Context, p auction.CreateParams) (auctionrepo.Lot, error)
	Bid(ctx context.Context, bidder domain.PlayerID, shipID domain.ShipID, lotID int64, amount int64) (auction.BidResult, error)
}

// handleAuctionMine lists the caller's lots (as seller or current high bidder).
func (s *Server) handleAuctionMine(w http.ResponseWriter, r *http.Request) {
	if s.auction == nil {
		writeError(w, http.StatusServiceUnavailable, "auction not available")
		return
	}
	playerID, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	lots, err := s.auction.MyLots(r.Context(), playerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]dto.AuctionLot, 0, len(lots))
	for _, l := range lots {
		out = append(out, dto.AuctionLotFromRepo(l))
	}
	writeJSON(w, http.StatusOK, dto.AuctionListResponse{Lots: out})
}

func (s *Server) handleAuctionList(w http.ResponseWriter, r *http.Request) {
	if s.auction == nil {
		writeError(w, http.StatusServiceUnavailable, "auction not available")
		return
	}
	lots, err := s.auction.ListActive(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]dto.AuctionLot, 0, len(lots))
	for _, l := range lots {
		out = append(out, dto.AuctionLotFromRepo(l))
	}
	writeJSON(w, http.StatusOK, dto.AuctionListResponse{Lots: out})
}

func (s *Server) handleAuctionCreate(w http.ResponseWriter, r *http.Request) {
	if s.auction == nil {
		writeError(w, http.StatusServiceUnavailable, "auction not available")
		return
	}
	var req dto.AuctionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Quantity <= 0 || req.StartPrice <= 0 || req.DurationSeconds <= 0 || req.GoodsTypeID <= 0 || req.Source.ID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	if !isAuctionSourceKind(req.Source.Kind) {
		writeError(w, http.StatusBadRequest, "source kind cannot hold cargo")
		return
	}
	playerID, _ := auth.PlayerIDFromContext(r.Context())

	lot, err := s.auction.Create(r.Context(), auction.CreateParams{
		Seller:     playerID,
		Source:     domain.EntityRef{Kind: domain.EntityKind(req.Source.Kind), ID: req.Source.ID},
		GoodsType:  domain.GoodsTypeID(req.GoodsTypeID),
		Quantity:   req.Quantity,
		StartPrice: req.StartPrice,
		Duration:   time.Duration(req.DurationSeconds) * time.Second,
	})
	if err != nil {
		writeAuctionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.AuctionLotFromRepo(lot))
}

func (s *Server) handleAuctionBid(w http.ResponseWriter, r *http.Request) {
	if s.auction == nil {
		writeError(w, http.StatusServiceUnavailable, "auction not available")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req dto.AuctionBidRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}
	if req.ShipID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid shipID")
		return
	}
	playerID, _ := auth.PlayerIDFromContext(r.Context())

	res, err := s.auction.Bid(r.Context(), playerID, domain.ShipID(req.ShipID), id, req.Amount)
	if err != nil {
		writeAuctionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.AuctionBidResponse{
		NewPrice:  res.NewPrice,
		NewLeader: res.NewLeader,
	})
}

func isAuctionSourceKind(kind int) bool {
	switch domain.EntityKind(kind) {
	case domain.EntityKindShip, domain.EntityKindStation, domain.EntityKindTradeStation:
		return true
	}
	return false
}

func writeAuctionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auction.ErrLotNotFound), errors.Is(err, auctionrepo.ErrLotNotFound):
		writeError(w, http.StatusNotFound, "lot not found")
	case errors.Is(err, auction.ErrLotNotActive), errors.Is(err, auctionrepo.ErrLotNotActive):
		writeError(w, http.StatusGone, "lot is no longer active")
	case errors.Is(err, auction.ErrBidTooLow):
		writeError(w, http.StatusBadRequest, "bid below current price")
	case errors.Is(err, auction.ErrInsufficientCash):
		writeError(w, http.StatusPaymentRequired, "insufficient cash")
	case errors.Is(err, auction.ErrSellerBid):
		writeError(w, http.StatusBadRequest, "seller cannot bid on own lot")
	case errors.Is(err, auction.ErrInvalidQuantity):
		writeError(w, http.StatusBadRequest, "quantity must be positive")
	case errors.Is(err, auction.ErrInvalidStartPrice):
		writeError(w, http.StatusBadRequest, "start price must be positive")
	case errors.Is(err, auction.ErrInvalidDuration):
		writeError(w, http.StatusBadRequest, "duration out of range")
	case errors.Is(err, auction.ErrInsufficientCargo):
		writeError(w, http.StatusBadRequest, "source does not carry enough goods")
	case errors.Is(err, auction.ErrNotDocked):
		writeError(w, http.StatusConflict, "ship must be docked at a station")
	case errors.Is(err, auction.ErrForbidden):
		writeError(w, http.StatusForbidden, "ship not owned by player")
	case errors.Is(err, auction.ErrShipNotFound):
		writeError(w, http.StatusNotFound, "ship not found")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
