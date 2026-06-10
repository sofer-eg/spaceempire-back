package sector_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

func TestUnit_Worker_LoadsStaticsOnStart(t *testing.T) {
	t.Parallel()

	statics := map[domain.SectorID]domain.SectorStatics{
		testSector: {
			Stations: []domain.Station{
				{ID: 7, SectorID: testSector, Pos: domain.Vec2{X: 100, Y: 200}, HP: 500, Shield: 800, Race: 1, Built: true},
			},
			Shipyards: []domain.Shipyard{
				{ID: 3, SectorID: testSector, Pos: domain.Vec2{X: -50, Y: 50}, HP: 1000, Shield: 1000, Race: 2, Built: true},
			},
			TradeStations: []domain.TradeStation{
				{ID: 9, SectorID: testSector, Pos: domain.Vec2{}, HP: 400, Shield: 600, Race: 1, Built: true},
			},
			Pirbases: []domain.Pirbase{
				{ID: 1, SectorID: testSector, Pos: domain.Vec2{X: 10, Y: 20}, HP: 100, Shield: 100, Race: 6, Built: true},
			},
		},
	}

	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		nil,
		map[domain.SectorID][]domain.Ship{testSector: nil},
		sector.WithStatics(statics),
	)

	snap := w.Snapshot(testSector)
	assert.Len(t, snap.Statics.Stations, 1)
	assert.Equal(t, domain.StationID(7), snap.Statics.Stations[0].ID)
	assert.Len(t, snap.Statics.Shipyards, 1)
	assert.Equal(t, domain.ShipyardID(3), snap.Statics.Shipyards[0].ID)
	assert.Len(t, snap.Statics.TradeStations, 1)
	assert.Equal(t, domain.TradeStationID(9), snap.Statics.TradeStations[0].ID)
	assert.Len(t, snap.Statics.Pirbases, 1)
	assert.Equal(t, domain.PirbaseID(1), snap.Statics.Pirbases[0].ID)
}
