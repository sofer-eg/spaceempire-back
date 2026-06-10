package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
)

type stubCargoService struct {
	inv      domain.Inventory
	invErr   error
	invCall  struct{ viewer domain.PlayerID }
	moveErr  error
	moveCall struct {
		actor    domain.PlayerID
		from, to domain.EntityRef
		gtype    domain.GoodsTypeID
		qty      int64
	}
}

func (s *stubCargoService) Inventory(_ context.Context, owner domain.EntityRef, viewer domain.PlayerID) (domain.Inventory, error) {
	s.invCall.viewer = viewer
	if s.invErr != nil {
		return domain.Inventory{}, s.invErr
	}
	out := s.inv
	out.Owner = owner
	return out, nil
}

func (s *stubCargoService) Move(_ context.Context, actor domain.PlayerID, from, to domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error {
	s.moveCall.actor = actor
	s.moveCall.from = from
	s.moveCall.to = to
	s.moveCall.gtype = gtype
	s.moveCall.qty = qty
	return s.moveErr
}

func newCargoServer(t *testing.T, svc api.CargoService) *api.Server {
	t.Helper()
	return api.NewServer(workerRouter{}, api.Config{
		SectorID: 1,
		Cargo:    svc,
	}, nil)
}

func TestUnit_CargoInventory_Ship_ReturnsItems(t *testing.T) {
	t.Parallel()
	svc := &stubCargoService{inv: domain.Inventory{
		Capacity: 100,
		Used:     5,
		Items:    []domain.CargoItem{{GoodsType: 1, Quantity: 5}},
	}}
	srv := newCargoServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/ship/42/cargo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.CargoInventoryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, int(domain.EntityKindShip), resp.OwnerKind)
	assert.EqualValues(t, 42, resp.OwnerID)
	assert.InDelta(t, 100.0, resp.Capacity, 1e-9)
	assert.InDelta(t, 5.0, resp.Used, 1e-9)
	require.Len(t, resp.Items, 1)
	assert.Equal(t, int32(1), resp.Items[0].TypeID)
	assert.EqualValues(t, 5, resp.Items[0].Quantity)
}

func TestUnit_CargoInventory_Station_RoutesByPath(t *testing.T) {
	t.Parallel()
	svc := &stubCargoService{inv: domain.Inventory{Capacity: 10000}}
	srv := newCargoServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/station/7/cargo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.CargoInventoryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, int(domain.EntityKindStation), resp.OwnerKind)
	assert.EqualValues(t, 7, resp.OwnerID)
}

func TestUnit_CargoInventory_OwnerNotFound_Returns404(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{invErr: cargo.ErrOwnerNotFound})

	req := httptest.NewRequest(http.MethodGet, "/api/ship/99/cargo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnit_CargoInventory_InvalidID_Returns400(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{})

	req := httptest.NewRequest(http.MethodGet, "/api/ship/notanumber/cargo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_CargoInventory_NoService_Returns503(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/ship/1/cargo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUnit_CargoMove_Success(t *testing.T) {
	t.Parallel()
	svc := &stubCargoService{}
	srv := newCargoServer(t, svc)

	body, _ := json.Marshal(dto.MoveCargoRequest{
		From:     dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 1},
		To:       dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		TypeID:   1,
		Quantity: 30,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	assert.Equal(t, domain.EntityKindStation, svc.moveCall.from.Kind)
	assert.EqualValues(t, 1, svc.moveCall.from.ID)
	assert.Equal(t, domain.EntityKindShip, svc.moveCall.to.Kind)
	assert.EqualValues(t, 2, svc.moveCall.to.ID)
	assert.EqualValues(t, 1, svc.moveCall.gtype)
	assert.EqualValues(t, 30, svc.moveCall.qty)
}

func TestUnit_CargoMove_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{})

	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_CargoMove_ZeroQuantity_Returns400(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{})

	body, _ := json.Marshal(dto.MoveCargoRequest{
		From:   dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 1},
		To:     dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		TypeID: 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_CargoMove_UnsupportedKind_Returns400(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{})

	body, _ := json.Marshal(dto.MoveCargoRequest{
		From:     dto.EntityRef{Kind: int(domain.EntityKindPirbase), ID: 1},
		To:       dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		TypeID:   1,
		Quantity: 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnit_CargoMove_NoSpace_Returns422(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{moveErr: cargo.ErrNoSpace})

	body, _ := json.Marshal(dto.MoveCargoRequest{
		From:     dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 1},
		To:       dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		TypeID:   3,
		Quantity: 100,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestUnit_CargoMove_Insufficient_Returns409(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{moveErr: cargo.ErrInsufficientQuantity})

	body, _ := json.Marshal(dto.MoveCargoRequest{
		From:     dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 1},
		To:       dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		TypeID:   1,
		Quantity: 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestUnit_CargoMove_Forbidden_Returns403(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{moveErr: cargo.ErrForbidden})

	body, _ := json.Marshal(dto.MoveCargoRequest{
		From:     dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 1},
		To:       dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		TypeID:   1,
		Quantity: 5,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, "body=%s", rec.Body.String())
}

func TestUnit_CargoMove_UnexpectedError_Returns500(t *testing.T) {
	t.Parallel()
	srv := newCargoServer(t, &stubCargoService{moveErr: errors.New("boom")})

	body, _ := json.Marshal(dto.MoveCargoRequest{
		From:     dto.EntityRef{Kind: int(domain.EntityKindStation), ID: 1},
		To:       dto.EntityRef{Kind: int(domain.EntityKindShip), ID: 2},
		TypeID:   1,
		Quantity: 1,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/cargo/move", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}
