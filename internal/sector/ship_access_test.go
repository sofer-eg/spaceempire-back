package sector_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

func newAccessWorker(t *testing.T) *sector.Worker {
	t.Helper()
	return sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil, nil,
		map[domain.SectorID][]domain.Ship{testSector: {{ID: 1, PlayerID: 7, SectorID: testSector}}},
	)
}

func TestUnit_SetShipAccessCommand_TogglesIsOpen(t *testing.T) {
	t.Parallel()
	w := newAccessWorker(t)

	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.SetShipAccessCommand{PlayerID: 7, ShipID: 1, Open: true, Reply: reply}
	}))
	assert.True(t, w.Snapshot(testSector).Ships[0].IsOpen, "ship opened for boarding")

	require.NoError(t, sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.SetShipAccessCommand{PlayerID: 7, ShipID: 1, Open: false, Reply: reply}
	}))
	assert.False(t, w.Snapshot(testSector).Ships[0].IsOpen, "ship closed again")
}

func TestUnit_SetShipAccessCommand_ForbiddenForOtherPlayer(t *testing.T) {
	t.Parallel()
	w := newAccessWorker(t)
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.SetShipAccessCommand{PlayerID: 999, ShipID: 1, Open: true, Reply: reply}
	})
	require.ErrorIs(t, err, sector.ErrForbidden)
	assert.False(t, w.Snapshot(testSector).Ships[0].IsOpen)
}

func TestUnit_SetShipAccessCommand_UnknownShip(t *testing.T) {
	t.Parallel()
	w := newAccessWorker(t)
	err := sendAndWait(t, w, func(reply chan<- sector.CmdResult) sector.Command {
		return sector.SetShipAccessCommand{PlayerID: 7, ShipID: 999, Open: true, Reply: reply}
	})
	require.ErrorIs(t, err, sector.ErrShipNotFound)
}
