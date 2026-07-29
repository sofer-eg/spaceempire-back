package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/cargo"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// dismantleCfg gives the worker a pickup reach of 50 — the range the dismantle
// gate uses — and the jam radius the generator projects.
func dismantleCfg() sector.Config {
	return sector.Config{
		TickInterval: time.Second,
		AOIRadius:    2000,
		PickupRange:  50,
		JammerRange:  2000,
	}
}

// dismantleShips are the three ships the gates need: the owner's ship parked
// where the generator gets deployed, another player's ship on the same spot (the
// object-ownership gate), and the owner's second ship far away (the range gate).
func dismantleShips() []domain.Ship {
	return []domain.Ship{
		installerTestShip(), // ID 1, player 7, (30,-40)
		{ID: 2, PlayerID: 8, SectorID: testSector, Pos: domain.Vec2{X: 30, Y: -40}},
		{ID: 3, PlayerID: 7, SectorID: testSector, Pos: domain.Vec2{X: 1000, Y: 1000}},
	}
}

// deployJammer installs one generator from ship 1 and returns its ref. The
// install is the only way an object reaches both the layout and the live combat
// set with an installer-assigned id, so the dismantle tests start from it instead
// of hand-building state.
func deployJammer(t *testing.T, w *sector.Worker) domain.EntityRef {
	t.Helper()
	reply := make(chan sector.InstallJammerResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallJammerCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 27, Reply: reply,
	}))
	w.Tick(context.Background())
	res := <-reply
	require.NoError(t, res.Err)
	return jammerRef(int64(res.JammerID))
}

// sendDismantle routes a DismantleStaticCommand from the given ship and returns
// the ack after one tick.
func sendDismantle(t *testing.T, w *sector.Worker, player domain.PlayerID, ship domain.ShipID,
	ref domain.EntityRef, gtype domain.GoodsTypeID,
) sector.CmdResult {
	t.Helper()
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.DismantleStaticCommand{
		PlayerID: player, ShipID: ship, Target: ref, GoodsType: gtype, Reply: reply,
	}))
	w.Tick(context.Background())
	select {
	case res := <-reply:
		return res
	default:
		t.Fatal("no dismantle ack")
		return sector.CmdResult{}
	}
}

// TestUnit_Dismantle_JammerReturnsGoodsAndClearsTheSector is the TASK-146 core: a
// deployed generator used to be irreversible — no take-down command, and
// fireLaserAtStatic refuses to shoot an object of your own — so a generator parked
// by your own station jammed its owner forever. Dismantling pays the unit back,
// deletes the row in the same transaction, and drops the object from the layout and
// the combat set, which is also what lifts the no-jump zone (jammerActive reads
// statics.Jammers).
func TestUnit_Dismantle_JammerReturnsGoodsAndClearsTheSector(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, dismantleCfg(), dismantleShips())

	ref := deployJammer(t, w)
	require.Len(t, w.Snapshot(testSector).Statics.Jammers, 1)
	require.Zero(t, inst.stock, "the install paid for the generator")

	res := sendDismantle(t, w, 7, 1, ref, 27)
	require.NoError(t, res.Err)

	assert.Equal(t, 1, inst.credits, "one unit credited back")
	assert.EqualValues(t, 1, inst.stock, "and it is in the hold")
	assert.Equal(t, []domain.EntityRef{ref}, inst.dismantled, "the row was deleted in the same transaction")
	assert.Empty(t, inst.jammers, "no generator left in the DB")

	snap := w.Snapshot(testSector)
	assert.Empty(t, snap.Statics.Jammers, "gone from the rendered layout")
	_, ok := findDestructible(snap, ref)
	assert.False(t, ok, "gone from the live combat set")
}

// The satellite goes through the same command and the same reverse transaction.
func TestUnit_Dismantle_SatelliteReturnsGoods(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, dismantleCfg(), dismantleShips())

	reply := make(chan sector.InstallSatelliteResult, 1)
	require.NoError(t, w.Send(testSector, sector.InstallSatelliteCommand{
		PlayerID: 7, ShipID: 1, GoodsType: 26, Reply: reply,
	}))
	w.Tick(context.Background())
	installed := <-reply
	require.NoError(t, installed.Err)
	ref := domain.EntityRef{Kind: domain.EntityKindSatellite, ID: int64(installed.SatelliteID)}

	res := sendDismantle(t, w, 7, 1, ref, 26)
	require.NoError(t, res.Err)
	assert.Equal(t, 1, inst.credits)
	assert.Empty(t, inst.satellites, "row deleted")
	assert.Empty(t, w.Snapshot(testSector).Statics.Satellites)
}

// A full hold refuses the whole dismantle: one object is indivisible, so the
// generator stays deployed and nothing is credited. Nothing is lost — the player
// makes room and tries again, which is what makes the refusal safe.
func TestUnit_Dismantle_NoRoomKeepsTheObject(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, dismantleCfg(), dismantleShips())
	ref := deployJammer(t, w)

	inst.noRoom = true
	res := sendDismantle(t, w, 7, 1, ref, 27)
	require.ErrorIs(t, res.Err, cargo.ErrNoSpace)
	assert.Zero(t, inst.credits)
	assert.Empty(t, inst.dismantled, "the transaction rolled back: the row is still there")
	assert.Len(t, w.Snapshot(testSector).Statics.Jammers, 1, "still deployed and still jamming")

	inst.noRoom = false
	require.NoError(t, sendDismantle(t, w, 7, 1, ref, 27).Err)
	assert.Empty(t, w.Snapshot(testSector).Statics.Jammers)
}

// Someone else's generator is not yours to fold up — it has to be shot. Player 8
// acts from their own ship (so the ship gate passes) on player 7's object.
func TestUnit_Dismantle_ForeignObjectForbidden(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, dismantleCfg(), dismantleShips())
	ref := deployJammer(t, w)

	res := sendDismantle(t, w, 8, 2, ref, 27)
	require.ErrorIs(t, res.Err, sector.ErrForbidden)
	assert.Len(t, w.Snapshot(testSector).Statics.Jammers, 1, "nothing removed")
	assert.Zero(t, inst.credits)
}

// Out of reach: the object is taken into a hold by hand, so the ship has to be
// next to it — the same PickupRange a container pickup needs. Ship 3 is the
// owner's, 1000+ units away.
func TestUnit_Dismantle_OutOfRangeRejected(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, dismantleCfg(), dismantleShips())
	ref := deployJammer(t, w)

	res := sendDismantle(t, w, 7, 3, ref, 27)
	require.ErrorIs(t, res.Err, sector.ErrDeployedOutOfRange)
	assert.Len(t, w.Snapshot(testSector).Statics.Jammers, 1)
	assert.Zero(t, inst.credits)
}

// Only player-deployed equipment folds back into cargo. A station is a world
// fixture: the command refuses it before any lookup.
func TestUnit_Dismantle_NonDeployableKindRejected(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, dismantleCfg(), dismantleShips())

	res := sendDismantle(t, w, 7, 1, domain.EntityRef{Kind: domain.EntityKindStation, ID: 3}, 27)
	require.ErrorIs(t, res.Err, sector.ErrNotDismantlable)
	assert.Zero(t, inst.credits)
}

// An id that is not in this sector (already destroyed, or a stale click) is a
// clean refusal, not a credit for nothing.
func TestUnit_Dismantle_UnknownObjectNotFound(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	w := installerWorker(t, inst, dismantleCfg(), dismantleShips())

	res := sendDismantle(t, w, 7, 1, jammerRef(4242), 27)
	require.ErrorIs(t, res.Err, sector.ErrDeployedNotFound)
	assert.Zero(t, inst.credits)
}

// A docked ship cannot dismantle: the object sits in open space, and the install
// it reverses is refused from a dock for the same reason. Ship 4 starts docked.
func TestUnit_Dismantle_DockedShipRejected(t *testing.T) {
	t.Parallel()
	inst := &fakeStaticInstaller{stock: 1}
	docked := domain.Ship{
		ID: 4, PlayerID: 7, SectorID: testSector, Pos: domain.Vec2{X: 30, Y: -40},
		Docked: &domain.EntityRef{Kind: domain.EntityKindStation, ID: 1},
	}
	w := installerWorker(t, inst, dismantleCfg(), append(dismantleShips(), docked))
	ref := deployJammer(t, w)

	res := sendDismantle(t, w, 7, 4, ref, 27)
	require.ErrorIs(t, res.Err, sector.ErrShipDocked)
	assert.Len(t, w.Snapshot(testSector).Statics.Jammers, 1)
	assert.Zero(t, inst.credits)
}

// Without a transactional installer the worker refuses to remove an object it
// cannot pay for — the nil-implementation doctrine of TASK-144, read backwards.
func TestUnit_Dismantle_WithoutInstallerRefused(t *testing.T) {
	t.Parallel()
	owner := domain.PlayerID(7)
	bare := sector.NewWorker(0, dismantleCfg(), clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {installerTestShip()}},
		sector.WithStatics(map[domain.SectorID]domain.SectorStatics{
			testSector: {Jammers: []domain.Jammer{{
				ID: 1, OwnerID: &owner, SectorID: testSector,
				Pos: domain.Vec2{X: 30, Y: -40}, Built: true, HP: 7500,
			}}},
		}))

	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, bare.Send(testSector, sector.DismantleStaticCommand{
		PlayerID: 7, ShipID: 1, Target: jammerRef(1), GoodsType: 27, Reply: reply,
	}))
	bare.Tick(context.Background())
	require.ErrorIs(t, (<-reply).Err, sector.ErrInstallerUnavailable)
	assert.Len(t, bare.Snapshot(testSector).Statics.Jammers, 1, "nothing removed for free")
}
