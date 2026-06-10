package race

import "spaceempire/back/internal/domain"

// Standing scale (race_relations.Standing): +10 ally, +5 friend, +1 friendly,
// 0 neutral, -5 unfriendly, -10 war.
const (
	StandingAlly    = 10
	StandingNeutral = 0
)

// standings is the default inter-race standing matrix, ported verbatim from the
// old `race_relations` table: row = the subject race's stance toward the column
// race. Asymmetric on purpose — Xenon likes Kha'ak (+10) but Kha'ak hates Xenon
// (-10). Pirate (6) has no rows in the original dump (its hostility is
// dynamic); MVP treats it via universallyHostile below.
var standings = map[domain.RaceID]map[domain.RaceID]int{
	1: {1: 10, 2: 10, 3: 1, 4: 1, 5: 5, 6: -5, 7: -10, 8: -10},
	2: {1: 10, 2: 10, 3: 1, 4: 1, 5: 5, 6: -5, 7: -10, 8: -10},
	3: {1: 1, 2: 1, 3: 10, 4: 10, 5: 10, 6: -5, 7: -10, 8: -10},
	4: {1: 1, 2: 1, 3: 10, 4: 10, 5: 10, 6: -5, 7: -10, 8: -10},
	5: {1: 1, 2: 1, 3: 10, 4: 10, 5: 10, 6: -5, 7: -10, 8: -10},
	7: {1: -10, 2: -10, 3: -10, 4: -10, 5: -10, 6: -10, 7: 10, 8: 10},
	8: {1: -10, 2: -10, 3: -10, 4: -10, 5: -10, 6: -10, 7: -10, 8: 10},
}

// universallyHostile gives the default stance for a from-race that has no
// explicit standing toward a target: pirates (6, dynamic in the original —
// MVP hostile-by-default) and the xenophobic Xenon (7) / Kha'ak (8) toward any
// unlisted target (e.g. the neutral race 0, i.e. factionless players). This is
// what reproduces the hostileRaces={6,7,8} behaviour from phase 8.3.
var universallyHostile = map[domain.RaceID]int{6: -5, 7: -10, 8: -10}

// DefaultStanding returns from-race's default stance toward to-race on the
// race_relations scale. Same race → ally. Falls back to universallyHostile for
// pirate/xenon/kha'ak toward unlisted targets, otherwise neutral.
func DefaultStanding(from, to domain.RaceID) int {
	if from == to {
		return StandingAlly
	}
	if row, ok := standings[from]; ok {
		if v, ok := row[to]; ok {
			return v
		}
	}
	if h, ok := universallyHostile[from]; ok {
		return h
	}
	return StandingNeutral
}

// IsHostile reports whether from-race attacks to-race by default (negative
// standing). Replaces the hostileRaces={6,7,8} hardcode (phase 8.3): a
// pirate/xenon/kha'ak object is hostile to a factionless player (Neutral),
// a main race (1–5) is not.
func IsHostile(from, to domain.RaceID) bool {
	return DefaultStanding(from, to) < 0
}
