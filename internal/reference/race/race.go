// Package race is the in-code reference catalog of the eight playable races
// plus the neutral pseudo-race (id 0) and two service races (АОГ 98, Unknown
// 100), ported from the old StarWind `races` table and the `js/map.js` palette
// (phase 8.13). Static definitions live in code (like quest.defs and balance);
// the DB holds only dynamic per-player/clan standings (a later phase).
//
// It also carries the default inter-race standing matrix from `race_relations`
// (see standing.go), which replaces the hostileRaces={6,7,8} hardcode in app
// (phase 8.3).
package race

import "spaceempire/back/internal/domain"

// Def is one race's reference data: identity, UI palette and capital location.
// Values are verbatim from the old `races` table (name/state/fleet/capital) and
// `js/map.js` (Color), never invented.
type Def struct {
	ID             domain.RaceID
	Name           string // short UI name (js/map.js raceName)
	StateName      string // state/faction name (races.state_name)
	FleetName      string // adjective form (races.fleet_name)
	Color          string // map/radar hex colour (js/map.js raceColor)
	CentralSector  domain.SectorID
	CentralStation int64
}

// Neutral is the pseudo-race for unowned/civilian objects (id 0). It has no row
// in the old `races` table — only a colour in js/map.js. Factionless players
// are treated as Neutral for hostility checks.
const Neutral = domain.RaceID(0)

// defsList is the ordered race reference. Order is stable so All() is
// deterministic. Colours are the canonical js/map.js palette; 98/100 have no
// original colour, so they get a neutral grey.
var defsList = []Def{
	{ID: 0, Name: "Нейтральный", StateName: "", FleetName: "", Color: "#ffffff", CentralSector: 0, CentralStation: 0},
	{ID: 1, Name: "Аргон", StateName: "Аргонская Федерация", FleetName: "Аргонский", Color: "#ec7a7c", CentralSector: 1, CentralStation: 18},
	{ID: 2, Name: "Борон", StateName: "Королевство Борон", FleetName: "Боронский", Color: "#647ab4", CentralSector: 5, CentralStation: 34},
	{ID: 3, Name: "Паранид", StateName: "Империя Паранид", FleetName: "Паранидский", Color: "#f4c694", CentralSector: 9, CentralStation: 50},
	{ID: 4, Name: "Сплит", StateName: "Семья Ронкар", FleetName: "Сплитский", Color: "#fcf604", CentralSector: 13, CentralStation: 66},
	{ID: 5, Name: "Телади", StateName: "Корпорация", FleetName: "Теладийский", Color: "#7cc6a4", CentralSector: 17, CentralStation: 82},
	{ID: 6, Name: "Пират", StateName: "Пираты", FleetName: "Пиратский", Color: "#6c6e6c", CentralSector: 21, CentralStation: 98},
	{ID: 7, Name: "Ксенон", StateName: "Ксенон", FleetName: "Ксенонский", Color: "#ff3030", CentralSector: 25, CentralStation: 114},
	{ID: 8, Name: "Хаак", StateName: "Гегемония Хааков", FleetName: "Хаакский", Color: "#ff5a5a", CentralSector: 215, CentralStation: 130},
	{ID: 98, Name: "АОГ", StateName: "АОГ", FleetName: "АОГ", Color: "#808080", CentralSector: 0, CentralStation: 0},
	{ID: 100, Name: "Unknown", StateName: "", FleetName: "", Color: "#808080", CentralSector: 0, CentralStation: 0},
}

// defsByID indexes defsList for O(1) Lookup.
var defsByID = func() map[domain.RaceID]Def {
	m := make(map[domain.RaceID]Def, len(defsList))
	for _, d := range defsList {
		m[d.ID] = d
	}
	return m
}()

// Lookup returns a race definition by id.
func Lookup(id domain.RaceID) (Def, bool) {
	d, ok := defsByID[id]
	return d, ok
}

// All returns the full race reference, in stable order (a defensive copy).
func All() []Def {
	out := make([]Def, len(defsList))
	copy(out, defsList)
	return out
}
