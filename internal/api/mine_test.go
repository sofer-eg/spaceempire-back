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
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// mineFakeContainerRepo satisfies sector.ContainerRepo for the mine handler
// tests; the handler never touches containers, so every method is a no-op.
type mineFakeContainerRepo struct{}

func (mineFakeContainerRepo) ShipCargo(context.Context, domain.ShipID) ([]domain.CargoItem, error) {
	return nil, nil
}
func (mineFakeContainerRepo) RecordKill(context.Context, domain.ShipID, domain.SectorID, []domain.ContainerDrop) ([]domain.Container, error) {
	return nil, nil
}
func (mineFakeContainerRepo) SpawnContainer(context.Context, domain.SectorID, domain.ContainerDrop) (domain.Container, error) {
	return domain.Container{}, nil
}
func (mineFakeContainerRepo) Pickup(context.Context, domain.ContainerID, domain.ShipID) error {
	return nil
}
func (mineFakeContainerRepo) Delete(context.Context, domain.ContainerID) error { return nil }

// newMineServer wires a single ship next to one asteroid. withDrill installs an
// up_drill module so the ship may start mining; without it the worker rejects
// the command with ErrEquipmentRequired (handler -> 422).
func newMineServer(t *testing.T, withDrill bool) *api.Server {
	t.Helper()
	ship := domain.Ship{
		ID: 1, PlayerID: 0, SectorID: 1, Pos: domain.Vec2{X: 0, Y: 0},
		HP: 100, MaxHP: 100, Energy: 1000, MaxEnergy: 1000,
	}
	if withDrill {
		ship.Equipment = []domain.InstalledEquipment{{Type: "up_drill", Level: 2}}
	}
	asteroid := domain.Asteroid{ID: 5, SectorID: 1, Pos: domain.Vec2{X: 5, Y: 0}, Mass: 100, OreType: 2}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64, MineRange: 12, MineRate: 5},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{1: {ship}},
		sector.WithContainers(mineFakeContainerRepo{}, nil),
		sector.WithAsteroids(nil, map[domain.SectorID][]domain.Asteroid{1: {asteroid}}),
	)
	runWorker(t, w)
	return api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
	}, nil)
}

func postMine(t *testing.T, srv *api.Server, shipID, asteroidID int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.MineRequest{ShipID: shipID, AsteroidID: asteroidID})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/mine", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnit_Mine_OK(t *testing.T) {
	t.Parallel()
	srv := newMineServer(t, true)

	rec := postMine(t, srv, 1, 5)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.MineResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
}

func TestUnit_Mine_NoDrill_422(t *testing.T) {
	t.Parallel()
	srv := newMineServer(t, false) // ship has no up_drill

	rec := postMine(t, srv, 1, 5)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestUnit_Mine_Stop_OK(t *testing.T) {
	t.Parallel()
	srv := newMineServer(t, true)

	rec := postMine(t, srv, 1, 0) // asteroidID 0 == stop

	require.Equal(t, http.StatusOK, rec.Code)
}
