package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
)

// isStationKind reports whether a cargo destination is a station-like object
// (a quest "deliver" target), phase 8.17.
func isStationKind(k domain.EntityKind) bool {
	switch k {
	case domain.EntityKindStation, domain.EntityKindTradeStation, domain.EntityKindPirbase:
		return true
	default:
		return false
	}
}

// CargoService is the slice of *cargo.Service the HTTP layer needs.
// Declared here per ISP — *cargo.Service implements it implicitly.
type CargoService interface {
	Inventory(ctx context.Context, owner domain.EntityRef, viewer domain.PlayerID) (domain.Inventory, error)
	Move(ctx context.Context, actor domain.PlayerID, from, to domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

func (s *Server) handleCargoInventory(kind domain.EntityKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cargo == nil {
			writeError(w, http.StatusServiceUnavailable, "cargo not available")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		owner := domain.EntityRef{Kind: kind, ID: id}
		// viewer scopes a station hold to the requester's own goods plus the
		// unowned pool (phase 10.22). Absent auth (legacy tests, ship holds)
		// viewer is 0, which still returns every unowned stack — exactly a
		// ship's whole hold.
		viewer, _ := auth.PlayerIDFromContext(r.Context())
		inv, err := s.cargo.Inventory(r.Context(), owner, viewer)
		if err != nil {
			writeCargoError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, dto.CargoInventoryFromDomain(inv))
	}
}

func (s *Server) handleCargoMove(w http.ResponseWriter, r *http.Request) {
	if s.cargo == nil {
		writeError(w, http.StatusServiceUnavailable, "cargo not available")
		return
	}
	var req dto.MoveCargoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "quantity must be positive")
		return
	}
	if req.TypeID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid typeID")
		return
	}
	if !isCargoOwnerKind(req.From.Kind) || !isCargoOwnerKind(req.To.Kind) {
		writeError(w, http.StatusBadRequest, "unsupported owner kind")
		return
	}
	from := domain.EntityRef{Kind: domain.EntityKind(req.From.Kind), ID: req.From.ID}
	to := domain.EntityRef{Kind: domain.EntityKind(req.To.Kind), ID: req.To.ID}

	// actor authorizes the move: deposits into a station tag the actor's
	// goods, withdrawals draw only the actor's own (+ unowned) stacks.
	actor, _ := auth.PlayerIDFromContext(r.Context())
	if err := s.cargo.Move(r.Context(), actor, from, to, domain.GoodsTypeID(req.TypeID), req.Quantity); err != nil {
		writeCargoError(w, err)
		return
	}
	// Unloading from a ship onto a station is a quest "deliver" signal (8.17).
	if from.Kind == domain.EntityKindShip && isStationKind(to.Kind) {
		s.publishCargoDelivered(r.Context(), actor, to, domain.GoodsTypeID(req.TypeID), req.Quantity)
	}
	writeJSON(w, http.StatusOK, dto.MoveCargoResponse{OK: true})
}

func isCargoOwnerKind(kind int) bool {
	switch domain.EntityKind(kind) {
	case domain.EntityKindShip, domain.EntityKindStation, domain.EntityKindTradeStation:
		return true
	}
	return false
}

func writeCargoError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cargo.ErrOwnerNotFound):
		writeError(w, http.StatusNotFound, "owner not found")
	case errors.Is(err, cargo.ErrGoodsTypeNotFound):
		writeError(w, http.StatusNotFound, "goods type not found")
	case errors.Is(err, cargo.ErrUnsupportedOwnerKind):
		writeError(w, http.StatusBadRequest, "unsupported owner kind")
	case errors.Is(err, cargo.ErrForbidden):
		writeError(w, http.StatusForbidden, "goods belong to another player")
	case errors.Is(err, cargo.ErrSameOwner):
		writeError(w, http.StatusBadRequest, "source and destination are the same")
	case errors.Is(err, cargo.ErrNonPositiveQuantity):
		writeError(w, http.StatusBadRequest, "quantity must be positive")
	case errors.Is(err, cargo.ErrInsufficientQuantity):
		writeError(w, http.StatusConflict, "insufficient quantity at source")
	case errors.Is(err, cargo.ErrNoSpace):
		writeError(w, http.StatusUnprocessableEntity, "destination has insufficient space")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
