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
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// fakeHauler is a sector.TraderLogistics that moves goods between owners in an
// in-memory store, so the transport handler test can assert cargo actually moved
// without a database.
type fakeHauler struct{ store map[domain.EntityRef]int64 }

func (f *fakeHauler) Haul(_ context.Context, from, to domain.EntityRef, _ domain.GoodsTypeID, maxUnits int64) error {
	q := f.store[from]
	if q > maxUnits {
		q = maxUnits
	}
	if q <= 0 {
		return nil
	}
	f.store[from] -= q
	f.store[to] += q
	return nil
}

// newTransportServer wires two player-0 ships in one sector: a receiver (id 1,
// optionally fitted with up_transporter) at the origin and a source (id 2) five
// units away, plus the fake cargo hauler.
func newTransportServer(t *testing.T, withModule bool, hauler sector.TraderLogistics) *api.Server {
	t.Helper()
	dest := domain.Ship{
		ID: 1, PlayerID: 0, SectorID: 1, Pos: domain.Vec2{X: 0, Y: 0},
		HP: 100, MaxHP: 100, Energy: 1000, MaxEnergy: 1000,
	}
	if withModule {
		dest.Equipment = []domain.InstalledEquipment{{Type: "up_transporter", Level: 1}}
	}
	src := domain.Ship{ID: 2, PlayerID: 0, SectorID: 1, Pos: domain.Vec2{X: 5, Y: 0}, HP: 100, MaxHP: 100}
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64, TransporterRange: 100, TransporterEnergyCost: 50},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{1: {dest, src}},
		sector.WithTraderLogistics(hauler),
	)
	runWorker(t, w)
	return api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond, AckTimeout: time.Second, SectorID: 1,
	}, nil)
}

func postTransport(t *testing.T, srv *api.Server, shipID, sourceShipID, goodsType, qty int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.TransportRequest{ShipID: shipID, SourceShipID: sourceShipID, GoodsType: goodsType, Quantity: qty})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/transport-cargo", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnit_Transport_OK(t *testing.T) {
	t.Parallel()
	srcRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	destRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: 1}
	hauler := &fakeHauler{store: map[domain.EntityRef]int64{srcRef: 100}}
	srv := newTransportServer(t, true, hauler)

	rec := postTransport(t, srv, 1, 2, 42, 30)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp dto.TransportResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.OK)
	assert.EqualValues(t, 70, hauler.store[srcRef])
	assert.EqualValues(t, 30, hauler.store[destRef])
}

func TestUnit_Transport_NoModule_422(t *testing.T) {
	t.Parallel()
	srcRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: 2}
	hauler := &fakeHauler{store: map[domain.EntityRef]int64{srcRef: 100}}
	srv := newTransportServer(t, false, hauler) // no up_transporter

	rec := postTransport(t, srv, 1, 2, 42, 30)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "body=%s", rec.Body.String())
	assert.EqualValues(t, 100, hauler.store[srcRef], "nothing moved")
}

func TestUnit_Transport_SameShip_400(t *testing.T) {
	t.Parallel()
	hauler := &fakeHauler{store: map[domain.EntityRef]int64{}}
	srv := newTransportServer(t, true, hauler)

	rec := postTransport(t, srv, 1, 1, 42, 30) // source == destination

	require.Equal(t, http.StatusBadRequest, rec.Code)
}
