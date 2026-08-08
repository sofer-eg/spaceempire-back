package app

import (
	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

// raceWarshipClassIDs collects the catalog ship-class IDs whose gameplay class
// is a combat class (balance.ShipClass.IsWarship, M2..M6) — the Race-AI squad
// membership set (TASK-207). Single source for race.Config.WarshipClassIDs;
// the spawners' per-race class filters are spawn policy and stay separate.
func raceWarshipClassIDs(classes *balance.ShipClasses) map[domain.ShipClassID]bool {
	out := make(map[domain.ShipClassID]bool)
	for _, sc := range classes.AllShipClasses() {
		if sc.IsWarship() {
			out[sc.ID] = true
		}
	}
	return out
}
