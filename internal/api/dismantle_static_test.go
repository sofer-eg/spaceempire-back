package api_test

import (
	"bytes"
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

func postDismantle(t *testing.T, srv *api.Server, shipID int64, kind int, id int64) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(dto.DismantleStaticRequest{
		ShipID: shipID,
		Target: dto.EntityRef{Kind: kind, ID: id},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cmd/dismantle-static", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestUnit_DismantleStatic_OK: the happy path folds the deployed generator back
// into the hold. The credit happens inside the worker's transaction, so the
// handler only reports it — and it must pay back the generator's goods (27), not
// the satellite's.
func TestUnit_DismantleStatic_OK(t *testing.T) {
	t.Parallel()
	srv, w, inst := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 1)
	runWorker(t, w)

	require.Equal(t, http.StatusOK, postInstallJammer(t, srv, 1).Code)
	stock, _, jammers, _ := inst.snapshot()
	require.EqualValues(t, 0, stock, "the install paid for it")
	require.Equal(t, 1, jammers)

	rec := postDismantle(t, srv, 1, int(domain.EntityKindJammer), 1)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp dto.DismantleStaticResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.OK)

	stock, _, jammers, _ = inst.snapshot()
	assert.EqualValues(t, 1, stock, "the unit is back in the hold")
	assert.Equal(t, 0, jammers, "the object is gone")

	// The literal id, not api.JammerGoodsType: comparing the constant with itself
	// would pass even if the handler credited the satellite's goods 26.
	goods := inst.installedGoods()
	require.Len(t, goods, 2, "one debit for the install, one credit for the dismantle")
	assert.Equal(t, domain.GoodsTypeID(27), goods[1], "the generator's own goods credited back")
}

// A satellite is dismantled through the same endpoint, and is paid back in
// satellite goods (26).
func TestUnit_DismantleStatic_Satellite(t *testing.T) {
	t.Parallel()
	srv, inst := newSatelliteTestServer(t, []domain.Ship{missileTestShip()}, 1)

	require.Equal(t, http.StatusOK, postInstallSatellite(t, srv, 1).Code)

	rec := postDismantle(t, srv, 1, int(domain.EntityKindSatellite), 1)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	stock, _, _, satellites := inst.snapshot()
	assert.EqualValues(t, 1, stock)
	assert.Equal(t, 0, satellites)
	goods := inst.installedGoods()
	require.Len(t, goods, 2)
	assert.Equal(t, domain.GoodsTypeID(26), goods[1])
}

// A kind that is not player-deployed equipment is refused by the handler itself,
// before any command is sent — the sector package never sees it.
func TestUnit_DismantleStatic_NonDeployableKind(t *testing.T) {
	t.Parallel()
	srv, w, inst := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 1)
	runWorker(t, w)

	rec := postDismantle(t, srv, 1, int(domain.EntityKindStation), 3)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Empty(t, inst.installedGoods(), "no goods touched")
}

// An object that is not in the sector is a 404, and nothing is credited: the
// worker's ErrDeployedNotFound must not fall through to a 500.
func TestUnit_DismantleStatic_UnknownObject(t *testing.T) {
	t.Parallel()
	srv, w, inst := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 1)
	runWorker(t, w)

	rec := postDismantle(t, srv, 1, int(domain.EntityKindJammer), 4242)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	stock, _, _, _ := inst.snapshot()
	assert.EqualValues(t, 1, stock, "hold untouched")
}

// A hold with no room answers 422, not 500: the object stays deployed and the
// player is told why (the same shape a full-hold container pickup gets).
func TestUnit_DismantleStatic_NoRoomIs422(t *testing.T) {
	t.Parallel()
	srv, w, inst := newJammerTestServer(t, []domain.Ship{missileTestShip()}, 1)
	runWorker(t, w)
	require.Equal(t, http.StatusOK, postInstallJammer(t, srv, 1).Code)

	inst.noRoom = true
	rec := postDismantle(t, srv, 1, int(domain.EntityKindJammer), 1)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	_, _, jammers, _ := inst.snapshot()
	assert.Equal(t, 1, jammers, "still deployed")
}

// Without an installer wired the worker refuses to remove an object it cannot pay
// for, and that surfaces as 503 (a wiring fault, not a player error).
func TestUnit_DismantleStatic_NoInstallerWired(t *testing.T) {
	t.Parallel()
	owner := domain.PlayerID(0)
	w := sector.NewWorker(0,
		sector.Config{TickInterval: 10 * time.Millisecond, InboxCapacity: 64},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{domain.SectorID(1): {missileTestShip()}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			domain.SectorID(1): {Jammers: []domain.Jammer{{
				ID: 1, OwnerID: &owner, SectorID: domain.SectorID(1), Built: true, HP: 7500,
			}}},
		}))
	srv := api.NewServer(workerRouter{w}, api.Config{
		SnapshotInterval: 10 * time.Millisecond,
		AckTimeout:       time.Second,
		SectorID:         1,
	}, nil)
	runWorker(t, w)

	rec := postDismantle(t, srv, 1, int(domain.EntityKindJammer), 1)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Len(t, w.Snapshot(domain.SectorID(1)).Statics.Jammers, 1, "nothing removed for free")
}
