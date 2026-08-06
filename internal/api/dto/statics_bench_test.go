package dto_test

import (
	"testing"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
)

// benchStatics is a deliberately fat sector layout: 40 static objects, more
// than any sector of the live world carries, so the numbers below are an upper
// bound rather than a typical case.
func benchStatics() (domain.SectorStatics, []domain.DestructibleStatic) {
	var s domain.SectorStatics
	for i := 1; i <= 10; i++ {
		s.Stations = append(s.Stations, domain.Station{
			ID: domain.StationID(i), SectorID: 1, Type: 7, Pos: domain.Vec2{X: float64(i), Y: float64(i)},
			HP: 7500, Shield: 100, MaxShield: 500, ShieldRecharge: 20, Built: true,
		})
		s.TradeStations = append(s.TradeStations, domain.TradeStation{
			ID: domain.TradeStationID(i), SectorID: 1, Type: 3, Pos: domain.Vec2{X: float64(i), Y: 0},
			HP: 5000, Shield: 100, MaxShield: 500, ShieldRecharge: 20, Built: true,
		})
		s.LaserTowers = append(s.LaserTowers, domain.LaserTower{
			ID: domain.LaserTowerID(i), SectorID: 1, Pos: domain.Vec2{X: 0, Y: float64(i)},
			HP: 1500, Shield: 50, MaxShield: 200, ShieldRecharge: 10, Built: true,
		})
		s.Jammers = append(s.Jammers, domain.Jammer{
			ID: domain.JammerID(i), SectorID: 1, Pos: domain.Vec2{X: float64(-i), Y: 0},
			HP: 7500, Shield: 50, MaxShield: 200, ShieldRecharge: 10, Built: true,
		})
	}
	return s, domain.DestructiblesFromStatics(s)
}

// BenchmarkStaticsFromDomain is the cost the welcome frame already paid before
// TASK-186: one full DTO copy of the sector layout, per connection.
func BenchmarkStaticsFromDomain(b *testing.B) {
	statics, _ := benchStatics()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = dto.StaticsFromDomain(statics)
	}
}

// BenchmarkDestructiblesFromDomain is everything TASK-186 added to that cost:
// the live combat state is already a value-copy slice the tick published, so a
// connection only encodes it — four scalars per object against the whole typed
// layout above. Run both to confirm the welcome frame did not turn into a
// second copy of the sector (AC#3).
func BenchmarkDestructiblesFromDomain(b *testing.B) {
	_, live := benchStatics()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = dto.DestructiblesFromDomain(live)
	}
}
