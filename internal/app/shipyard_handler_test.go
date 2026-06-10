package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/sector"
)

type spawnAtCall struct {
	player domain.PlayerID
	sector domain.SectorID
	pos    domain.Vec2
}

type fakeReplacer struct{ calls []spawnAtCall }

func (f *fakeReplacer) SpawnStarterAt(_ context.Context, p domain.PlayerID, s domain.SectorID, pos domain.Vec2) error {
	f.calls = append(f.calls, spawnAtCall{p, s, pos})
	return nil
}

type fakeYard struct {
	shipID domain.ShipID
	sector domain.SectorID
	ok     bool
	ships  []domain.Ship
	sent   []domain.ShipID
}

func (f *fakeYard) LookupPrimaryShipByPlayer(_ domain.PlayerID) (domain.ShipID, domain.SectorID, bool) {
	return f.shipID, f.sector, f.ok
}

func (f *fakeYard) Snapshot(_ domain.SectorID) sector.Snapshot {
	return sector.Snapshot{Ships: f.ships}
}

func (f *fakeYard) ShipsByPlayer(_ domain.PlayerID) []domain.Ship {
	return f.ships
}

func (f *fakeYard) Send(_ domain.SectorID, cmd sector.Command) error {
	if c, ok := cmd.(sector.RemoveShipCommand); ok {
		f.sent = append(f.sent, c.ShipID)
		if c.Reply != nil {
			c.Reply <- sector.CmdResult{}
		}
	}
	return nil
}

// yardWithSuit wires a player whose ship (id 7, sector 2) is a spacesuit docked
// at the given object.
func yardWithSuit(docked *domain.EntityRef) *fakeYard {
	return &fakeYard{
		shipID: 7, sector: 2, ok: true,
		ships: []domain.Ship{{
			ID: 7, PlayerID: 100, SectorID: 2, Pos: domain.Vec2{X: 100, Y: 0},
			IsSpacesuit: true, Docked: docked,
		}},
	}
}

func doGetShip(t *testing.T, yard *fakeYard, repl *fakeReplacer, shipyardID string) *httptest.ResponseRecorder {
	t.Helper()
	srv := newShipyardServer(yard, repl, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodPost, "/api/shipyard/"+shipyardID+"/get-ship", nil)
	req = req.WithContext(auth.ContextWithPlayerID(req.Context(), domain.PlayerID(100)))
	req.SetPathValue("id", shipyardID)
	rr := httptest.NewRecorder()
	srv.handleGetShip(rr, req)
	return rr
}

func shipyardRef(id int64) *domain.EntityRef {
	return &domain.EntityRef{Kind: domain.EntityKindShipyard, ID: id}
}

func TestUnit_Shipyard_GetShip_ReplacesSuitAtShipyard(t *testing.T) {
	t.Parallel()
	yard := yardWithSuit(shipyardRef(5))
	repl := &fakeReplacer{}

	rr := doGetShip(t, yard, repl, "5")

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, []spawnAtCall{{player: 100, sector: 2, pos: domain.Vec2{X: 100, Y: 0}}}, repl.calls,
		"new starter ship spawned at the suit's spot")
	require.Equal(t, []domain.ShipID{7}, yard.sent, "suit removed")
}

func TestUnit_Shipyard_GetShip_RejectsNonSuit(t *testing.T) {
	t.Parallel()
	yard := yardWithSuit(shipyardRef(5))
	yard.ships[0].IsSpacesuit = false
	repl := &fakeReplacer{}

	rr := doGetShip(t, yard, repl, "5")

	require.Equal(t, http.StatusConflict, rr.Code)
	assert.Empty(t, repl.calls)
	assert.Empty(t, yard.sent)
}

func TestUnit_Shipyard_GetShip_RejectsNotDockedAtThisShipyard(t *testing.T) {
	t.Parallel()
	repl := &fakeReplacer{}

	// docked at a station, not a shipyard
	rr := doGetShip(t, yardWithSuit(&domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}), repl, "5")
	assert.Equal(t, http.StatusConflict, rr.Code)

	// docked at a different shipyard
	rr = doGetShip(t, yardWithSuit(shipyardRef(9)), repl, "5")
	assert.Equal(t, http.StatusConflict, rr.Code)

	// not docked at all
	rr = doGetShip(t, yardWithSuit(nil), repl, "5")
	assert.Equal(t, http.StatusConflict, rr.Code)

	assert.Empty(t, repl.calls)
}

func TestUnit_Shipyard_GetShip_RejectsNoShip(t *testing.T) {
	t.Parallel()
	repl := &fakeReplacer{}
	rr := doGetShip(t, &fakeYard{ok: false}, repl, "5")
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Empty(t, repl.calls)
}
