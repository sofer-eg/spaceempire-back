package sector_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

const (
	tpPlayer = domain.PlayerID(5)
	tpDest   = domain.ShipID(1) // the up_transporter ship (receiver)
	tpSrc    = domain.ShipID(2) // the source ship cargo is pulled from
	tpGoods  = domain.GoodsTypeID(42)
)

// transportWorker wires two ships owned by tpPlayer: a receiver (id 1, with the
// given equipment + energy) at the origin and a source (id 2) `srcDist` units
// away owned by srcOwner. The shared fakeLogistics tracks the cargo move.
func transportWorker(t *testing.T, equipment []domain.InstalledEquipment, energy int, srcDist float64, srcOwner domain.PlayerID, logistics sector.TraderLogistics) *sector.Worker {
	t.Helper()
	dest := domain.Ship{
		ID: tpDest, PlayerID: tpPlayer, SectorID: testSector,
		Pos: domain.Vec2{X: 0, Y: 0}, HP: 100, MaxHP: 100,
		Energy: energy, MaxEnergy: 1000, Equipment: equipment,
	}
	src := domain.Ship{
		ID: tpSrc, PlayerID: srcOwner, SectorID: testSector,
		Pos: domain.Vec2{X: srcDist, Y: 0}, HP: 100, MaxHP: 100,
	}
	return sector.NewWorker(0,
		sector.Config{TickInterval: time.Second, AOIRadius: 2000, TransporterRange: 100, TransporterEnergyCost: 50},
		clock.NewRealClock(), nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {dest, src}},
		sector.WithTraderLogistics(logistics),
	)
}

func sendTransport(t *testing.T, w *sector.Worker, qty int64) sector.CmdResult {
	t.Helper()
	reply := make(chan sector.CmdResult, 1)
	require.NoError(t, w.Send(testSector, sector.TransportCargoCommand{
		PlayerID: tpPlayer, ShipID: tpDest, SourceShipID: tpSrc,
		GoodsType: tpGoods, Quantity: qty, Reply: reply,
	}))
	w.Tick(context.Background())
	return <-reply
}

var withTransporter = []domain.InstalledEquipment{{Type: "up_transporter", Level: 1}}

// TestUnit_Transport_MovesCargo_DebitsEnergy proves a valid teleport hauls the
// cargo source→receiver and spends the receiver's action energy.
func TestUnit_Transport_MovesCargo_DebitsEnergy(t *testing.T) {
	t.Parallel()
	logistics := newFakeLogistics()
	srcRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(tpSrc)}
	destRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(tpDest)}
	logistics.store[srcRef] = 100
	w := transportWorker(t, withTransporter, 1000, 5, tpPlayer, logistics)

	require.NoError(t, sendTransport(t, w, 30).Err)

	assert.EqualValues(t, 70, logistics.store[srcRef], "30 of 100 pulled from the source")
	assert.EqualValues(t, 30, logistics.store[destRef], "30 teleported into the receiver")
	dest, ok := snapshotShipByID(w.Snapshot(testSector), tpDest)
	require.True(t, ok)
	assert.Equal(t, 950, dest.Energy, "teleport debits TransporterEnergyCost (50)")
}

// TestUnit_Transport_NoModule_Rejected proves a receiver without up_transporter
// cannot teleport (ErrEquipmentRequired) and moves nothing.
func TestUnit_Transport_NoModule_Rejected(t *testing.T) {
	t.Parallel()
	logistics := newFakeLogistics()
	srcRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(tpSrc)}
	logistics.store[srcRef] = 100
	w := transportWorker(t, nil, 1000, 5, tpPlayer, logistics)

	res := sendTransport(t, w, 30)

	require.ErrorIs(t, res.Err, sector.ErrEquipmentRequired)
	assert.EqualValues(t, 100, logistics.store[srcRef], "nothing moved without the module")
}

// TestUnit_Transport_OutOfRange_Rejected proves a source beyond TransporterRange
// is rejected.
func TestUnit_Transport_OutOfRange_Rejected(t *testing.T) {
	t.Parallel()
	logistics := newFakeLogistics()
	srcRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(tpSrc)}
	logistics.store[srcRef] = 100
	w := transportWorker(t, withTransporter, 1000, 250, tpPlayer, logistics) // 250 > range 100

	res := sendTransport(t, w, 30)

	require.ErrorIs(t, res.Err, sector.ErrTransporterOutOfRange)
	assert.EqualValues(t, 100, logistics.store[srcRef], "nothing moved out of range")
}

// TestUnit_Transport_NotOwned_Rejected proves the source must belong to the same
// player.
func TestUnit_Transport_NotOwned_Rejected(t *testing.T) {
	t.Parallel()
	logistics := newFakeLogistics()
	w := transportWorker(t, withTransporter, 1000, 5, domain.PlayerID(99), logistics) // source owned by another player

	res := sendTransport(t, w, 30)

	require.ErrorIs(t, res.Err, sector.ErrForbidden)
}

// TestUnit_Transport_NotEnoughEnergy_Rejected proves a receiver below the action
// cost cannot teleport.
func TestUnit_Transport_NotEnoughEnergy_Rejected(t *testing.T) {
	t.Parallel()
	logistics := newFakeLogistics()
	srcRef := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(tpSrc)}
	logistics.store[srcRef] = 100
	w := transportWorker(t, withTransporter, 10, 5, tpPlayer, logistics) // energy 10 < cost 50

	res := sendTransport(t, w, 30)

	require.ErrorIs(t, res.Err, sector.ErrNotEnoughEnergy)
	assert.EqualValues(t, 100, logistics.store[srcRef], "nothing moved without energy")
}
