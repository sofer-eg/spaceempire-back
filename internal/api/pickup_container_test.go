package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api"
	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/persistence/containers"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// noSpaceRepo is a sector.ContainerRepo whose Pickup always reports a full
// hold, used to exercise the handler's 409 mapping.
type noSpaceRepo struct{}

func (noSpaceRepo) ShipCargo(context.Context, domain.ShipID) ([]domain.CargoItem, error) {
	return nil, nil
}
func (noSpaceRepo) RecordKill(context.Context, domain.ShipID, domain.SectorID, []domain.ContainerDrop) ([]domain.Container, error) {
	return nil, nil
}
func (noSpaceRepo) SpawnContainer(context.Context, domain.SectorID, domain.ContainerDrop) (domain.Container, error) {
	return domain.Container{}, nil
}
func (noSpaceRepo) Pickup(context.Context, domain.ContainerID, domain.ShipID) error {
	return containers.ErrNoSpace
}
func (noSpaceRepo) Delete(context.Context, domain.ContainerID) error { return nil }

func newPickupServer(t *testing.T, repo sector.ContainerRepo, containerPos domain.Vec2) (*api.Server, *sector.Worker) {
	t.Helper()
	ship := domain.Ship{ID: 1, PlayerID: 0, SectorID: 1, Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, MaxHP: 100}
	container := domain.Container{ID: 7, SectorID: 1, Pos: containerPos, ExpiresAt: time.Now().Add(time.Hour)}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64, PickupRange: 30},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{1: {ship}},
		sector.WithContainers(repo, map[domain.SectorID][]domain.Container{1: {container}}),
	)
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
	}, nil)
	return srv, w
}

func postPickup(t *testing.T, srv *api.Server, shipID, containerID int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.PickupContainerRequest{ShipID: shipID, ContainerID: containerID})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/pickup-container", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnit_PickupContainer_OK(t *testing.T) {
	t.Parallel()
	srv, w := newPickupServer(t, nil, domain.Vec2{X: 10, Y: 0}) // in range
	runWorker(t, w)

	rec := postPickup(t, srv, 1, 7)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.PickupContainerResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
}

func TestUnit_PickupContainer_OutOfRange_400(t *testing.T) {
	t.Parallel()
	srv, w := newPickupServer(t, nil, domain.Vec2{X: 1000, Y: 0}) // far
	runWorker(t, w)

	require.Equal(t, http.StatusBadRequest, postPickup(t, srv, 1, 7).Code)
}

func TestUnit_PickupContainer_NotFound_404(t *testing.T) {
	t.Parallel()
	srv, w := newPickupServer(t, nil, domain.Vec2{X: 10, Y: 0})
	runWorker(t, w)

	require.Equal(t, http.StatusNotFound, postPickup(t, srv, 1, 999).Code)
}

func TestUnit_PickupContainer_NoSpace_409(t *testing.T) {
	t.Parallel()
	srv, w := newPickupServer(t, noSpaceRepo{}, domain.Vec2{X: 10, Y: 0})
	runWorker(t, w)

	require.Equal(t, http.StatusConflict, postPickup(t, srv, 1, 7).Code)
}
