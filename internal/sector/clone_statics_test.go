package sector

import (
	"testing"

	"spaceempire/back/internal/domain"
)

// TestUnit_cloneStatics_CopiesAllKinds is a regression anchor for the
// welcome-snapshot static serialization: cloneStatics must copy EVERY static
// kind. A previously missing LaserTowers branch left towers out of the snapshot
// sent to new subscribers (statics.laserTowers == null on the client), so a
// player could never pick a laser tower as a weapon target — defeating the
// LaserTower picking path added in TASK-113 (FR-04/AC-6). Satellites rode the
// same machinery and were copied; towers were not.
func TestUnit_cloneStatics_CopiesAllKinds(t *testing.T) {
	in := domain.SectorStatics{
		Stations:      []domain.Station{{ID: 1}},
		Shipyards:     []domain.Shipyard{{ID: 1}},
		TradeStations: []domain.TradeStation{{ID: 1}},
		Pirbases:      []domain.Pirbase{{ID: 1}},
		LaserTowers:   []domain.LaserTower{{ID: 1}},
		Satellites:    []domain.Satellite{{ID: 1}},
	}

	out := cloneStatics(in)

	if got := len(out.Stations); got != 1 {
		t.Errorf("Stations not copied: got %d, want 1", got)
	}
	if got := len(out.Shipyards); got != 1 {
		t.Errorf("Shipyards not copied: got %d, want 1", got)
	}
	if got := len(out.TradeStations); got != 1 {
		t.Errorf("TradeStations not copied: got %d, want 1", got)
	}
	if got := len(out.Pirbases); got != 1 {
		t.Errorf("Pirbases not copied: got %d, want 1", got)
	}
	if got := len(out.LaserTowers); got != 1 {
		t.Errorf("LaserTowers not copied (regression: tower invisible to client): got %d, want 1", got)
	}
	if got := len(out.Satellites); got != 1 {
		t.Errorf("Satellites not copied: got %d, want 1", got)
	}
}
