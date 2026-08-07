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
//
// It names MoveByPlayer, never the ungated Move: the HTTP path must not be able
// to reach the engine's unauthorized transfer even by mistake (TASK-189).
type CargoService interface {
	Inventory(ctx context.Context, owner domain.EntityRef, viewer domain.PlayerID) (domain.Inventory, error)
	MoveByPlayer(ctx context.Context, actor domain.PlayerID, from, to domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

func (s *Server) handleCargoInventory(kind domain.EntityKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cargo == nil {
			writeError(w, http.StatusServiceUnavailable, "работа с грузом недоступна")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "некорректный идентификатор")
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
			s.writeCargoError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, dto.CargoInventoryFromDomain(inv))
	}
}

func (s *Server) handleCargoMove(w http.ResponseWriter, r *http.Request) {
	if s.cargo == nil {
		writeError(w, http.StatusServiceUnavailable, "работа с грузом недоступна")
		return
	}
	var req dto.MoveCargoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "некорректный запрос")
		return
	}
	if req.Quantity <= 0 {
		writeError(w, http.StatusBadRequest, "количество должно быть положительным")
		return
	}
	if req.TypeID <= 0 {
		writeError(w, http.StatusBadRequest, "некорректный тип груза")
		return
	}
	if !isCargoOwnerKind(req.From.Kind) || !isCargoOwnerKind(req.To.Kind) {
		writeError(w, http.StatusBadRequest, "этот объект не может хранить груз")
		return
	}
	from := domain.EntityRef{Kind: domain.EntityKind(req.From.Kind), ID: req.From.ID}
	to := domain.EntityRef{Kind: domain.EntityKind(req.To.Kind), ID: req.To.ID}

	// actor authorizes the move: the ship end must be actor's and docked to the
	// station end (TASK-189), deposits into a station tag the actor's goods, and
	// withdrawals draw only the actor's own (+ unowned) stacks.
	actor, _ := auth.PlayerIDFromContext(r.Context())
	if err := s.cargo.MoveByPlayer(r.Context(), actor, from, to, domain.GoodsTypeID(req.TypeID), req.Quantity); err != nil {
		s.writeCargoError(w, r, err)
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

func (s *Server) writeCargoError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, cargo.ErrOwnerNotFound):
		writeError(w, http.StatusNotFound, "объект не найден")
	case errors.Is(err, cargo.ErrShipNotFound):
		writeError(w, http.StatusNotFound, "корабль не найден")
	case errors.Is(err, cargo.ErrGoodsTypeNotFound):
		writeError(w, http.StatusNotFound, "тип груза не найден")
	case errors.Is(err, cargo.ErrUnsupportedOwnerKind):
		writeError(w, http.StatusBadRequest, "этот объект не может хранить груз")
	case errors.Is(err, cargo.ErrShipForbidden):
		writeError(w, http.StatusForbidden, "чужой корабль")
	case errors.Is(err, cargo.ErrForbidden):
		writeError(w, http.StatusForbidden, "груз принадлежит другому игроку")
	case errors.Is(err, cargo.ErrNotDocked):
		writeError(w, http.StatusBadRequest, "корабль не пристыкован")
	case errors.Is(err, cargo.ErrWrongStation):
		writeError(w, http.StatusBadRequest, "корабль пристыкован к другому объекту")
	case errors.Is(err, cargo.ErrInvalidTransfer):
		writeError(w, http.StatusBadRequest, "груз можно перекладывать только между кораблём и станцией")
	case errors.Is(err, cargo.ErrSameOwner):
		writeError(w, http.StatusBadRequest, "источник и приёмник совпадают")
	case errors.Is(err, cargo.ErrNonPositiveQuantity):
		writeError(w, http.StatusBadRequest, "количество должно быть положительным")
	case errors.Is(err, cargo.ErrInsufficientQuantity):
		writeError(w, http.StatusConflict, "в источнике недостаточно груза")
	case errors.Is(err, cargo.ErrNoSpace):
		writeError(w, http.StatusUnprocessableEntity, "не хватает места в трюме приёмника")
	default:
		s.writeInternalError(w, r, err)
	}
}
