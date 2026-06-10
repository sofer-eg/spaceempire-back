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
	"spaceempire/back/internal/economy/production"
	traderepo "spaceempire/back/internal/persistence/trade"
	"spaceempire/back/internal/trade"
)

// TradeService is the slice of *trade.Service the HTTP layer needs.
// Declared per ISP so handler tests can stub it.
type TradeService interface {
	Market(ctx context.Context, owner domain.EntityRef) ([]traderepo.MarketEntry, error)
	Buy(ctx context.Context, playerID domain.PlayerID, shipID domain.ShipID, station domain.EntityRef, gtype domain.GoodsTypeID, qty int64) (trade.BuyResult, error)
	Sell(ctx context.Context, playerID domain.PlayerID, shipID domain.ShipID, station domain.EntityRef, gtype domain.GoodsTypeID, qty int64) (trade.SellResult, error)
}

// StationProductionReader adorns a factory's market with its production
// cycle. Optional (nil omits the block); only consulted for Station markets.
type StationProductionReader interface {
	StationCycle(ctx context.Context, id domain.StationID) (production.CycleInfo, error)
}

func (s *Server) handleMarket(kind domain.EntityKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.trade == nil {
			writeError(w, http.StatusServiceUnavailable, "trade not available")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		owner := domain.EntityRef{Kind: kind, ID: id}
		entries, err := s.trade.Market(r.Context(), owner)
		if err != nil {
			writeTradeError(w, err)
			return
		}
		resp := dto.MarketResponseFromRepo(owner, entries)
		if kind == domain.EntityKindStation && s.stationProduction != nil {
			// The production block is a non-critical adornment: on a lookup
			// error the market still renders, just without the cycle timer.
			if info, perr := s.stationProduction.StationCycle(r.Context(), domain.StationID(id)); perr != nil {
				s.logger.Warn("market production lookup", "station", id, "err", perr)
			} else {
				resp.Production = dto.ProductionInfoFromCycle(info, time.Now())
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *Server) handleTradeBuy(w http.ResponseWriter, r *http.Request) {
	if s.trade == nil {
		writeError(w, http.StatusServiceUnavailable, "trade not available")
		return
	}
	var req dto.TradeBuyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.Station.ID <= 0 || req.TypeID <= 0 || req.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	playerID, _ := auth.PlayerIDFromContext(r.Context())
	station := domain.EntityRef{Kind: domain.EntityKind(req.Station.Kind), ID: req.Station.ID}

	res, err := s.trade.Buy(
		r.Context(), playerID,
		domain.ShipID(req.ShipID), station,
		domain.GoodsTypeID(req.TypeID), req.Quantity,
	)
	if err != nil {
		writeTradeError(w, err)
		return
	}
	s.publishTradeCompleted(r.Context(), playerID, "buy", domain.GoodsTypeID(req.TypeID), req.Quantity)
	writeJSON(w, http.StatusOK, dto.TradeBuyResponse{
		NewCash:     res.NewCash,
		NewStock:    res.NewStock,
		Moved:       req.Quantity,
		UnitPrice:   res.UnitPrice,
		TotalAmount: res.TotalAmount,
	})
}

func (s *Server) handleTradeSell(w http.ResponseWriter, r *http.Request) {
	if s.trade == nil {
		writeError(w, http.StatusServiceUnavailable, "trade not available")
		return
	}
	var req dto.TradeSellRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ShipID <= 0 || req.Station.ID <= 0 || req.TypeID <= 0 || req.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "invalid request fields")
		return
	}
	playerID, _ := auth.PlayerIDFromContext(r.Context())
	station := domain.EntityRef{Kind: domain.EntityKind(req.Station.Kind), ID: req.Station.ID}

	res, err := s.trade.Sell(
		r.Context(), playerID,
		domain.ShipID(req.ShipID), station,
		domain.GoodsTypeID(req.TypeID), req.Quantity,
	)
	if err != nil {
		writeTradeError(w, err)
		return
	}
	s.publishTradeCompleted(r.Context(), playerID, "sell", domain.GoodsTypeID(req.TypeID), req.Quantity)
	writeJSON(w, http.StatusOK, dto.TradeSellResponse{
		NewCash:     res.NewCash,
		NewStock:    res.NewStock,
		Moved:       req.Quantity,
		UnitPrice:   res.UnitPrice,
		TotalAmount: res.TotalAmount,
	})
}

func writeTradeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, trade.ErrShipNotFound):
		writeError(w, http.StatusNotFound, "ship not found")
	case errors.Is(err, trade.ErrForbidden):
		writeError(w, http.StatusForbidden, "ship belongs to another player")
	case errors.Is(err, trade.ErrNotDocked):
		writeError(w, http.StatusBadRequest, "ship is not docked")
	case errors.Is(err, trade.ErrWrongStation):
		writeError(w, http.StatusBadRequest, "ship docked at a different station")
	case errors.Is(err, trade.ErrInvalidStationKind):
		writeError(w, http.StatusBadRequest, "station kind not tradeable")
	case errors.Is(err, trade.ErrMarketEntryNotFound):
		writeError(w, http.StatusNotFound, "station does not offer this good")
	case errors.Is(err, trade.ErrStationDoesNotSell):
		writeError(w, http.StatusBadRequest, "station does not sell this good")
	case errors.Is(err, trade.ErrStationDoesNotBuy):
		writeError(w, http.StatusBadRequest, "station does not buy this good")
	case errors.Is(err, trade.ErrNonPositiveQuantity):
		writeError(w, http.StatusBadRequest, "quantity must be positive")
	case errors.Is(err, trade.ErrInsufficientStock):
		writeError(w, http.StatusBadRequest, "station does not have enough stock")
	case errors.Is(err, trade.ErrStockOverflow):
		writeError(w, http.StatusBadRequest, "station cannot accept this much")
	case errors.Is(err, trade.ErrInsufficientCash):
		writeError(w, http.StatusPaymentRequired, "insufficient cash")
	case errors.Is(err, trade.ErrInsufficientCargo):
		writeError(w, http.StatusBadRequest, "ship does not carry enough of this good")
	case errors.Is(err, trade.ErrNoCargoSpace):
		writeError(w, http.StatusBadRequest, "ship cargobay cannot fit this purchase")
	case errors.Is(err, trade.ErrGoodsTypeNotFound):
		writeError(w, http.StatusNotFound, "goods type not found")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
