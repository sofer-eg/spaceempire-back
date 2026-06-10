// Package contraband is the in-code reference of which goods are illegal for
// each NPC race (phase 9.4). Like the 8.13 race reference, the data is tiny so
// it lives in code, not the DB. The police of a race confiscate these goods
// from player ships and drop the player's standing.
//
// MVP: only Slaves (goods 323, seeded in 5.6) are contraband — illegal for the
// main races (1-5) and legal for pirates (6). Drugs / space-fuel are deferred:
// Slaves is the only canonical contraband in the catalog, and "Космическое
// топливо" is an ordinary trade good, not contraband.
package contraband

import "spaceempire/back/internal/domain"

// Slaves is goods 323 (phase 5.6) — the only contraband in the MVP.
const Slaves = domain.GoodsTypeID(323)

// illegalByRace lists each race's contraband goods. Absence (e.g. pirates, 6)
// means the race treats every good as legal and never confiscates.
var illegalByRace = map[domain.RaceID][]domain.GoodsTypeID{
	1: {Slaves}, // Argon
	2: {Slaves}, // Boron
	3: {Slaves}, // Paranid
	4: {Slaves}, // Split
	5: {Slaves}, // Teladi
}

// IsIllegal reports whether the goods type is contraband for the race.
func IsIllegal(race domain.RaceID, goods domain.GoodsTypeID) bool {
	for _, g := range illegalByRace[race] {
		if g == goods {
			return true
		}
	}
	return false
}

// Illegal returns the race's contraband goods (nil for races with none). The
// returned slice must not be mutated.
func Illegal(race domain.RaceID) []domain.GoodsTypeID {
	return illegalByRace[race]
}
