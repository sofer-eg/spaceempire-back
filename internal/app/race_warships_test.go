package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/balance"
	"spaceempire/back/internal/domain"
)

func TestUnit_RaceWarshipClassIDs(t *testing.T) {
	t.Parallel()
	sc, err := balance.NewShipClasses([]balance.ShipClass{
		{ID: 81, Race: 8, Class: 2, Name: "Khaak M2"},
		{ID: 11, Race: 1, Class: 3, Name: "Argon M3"},
		{ID: 12, Race: 1, Class: 4, Name: "Argon M4"},
		{ID: 73, Race: 7, Class: 5, Name: "Xenon M5"},
		{ID: 13, Race: 1, Class: 6, Name: "Argon M6"},
		{ID: 14, Race: 1, Class: 9, Name: "Argon TS"}, // civilian — excluded
		{ID: 15, Race: 1, Class: 1, Name: "Argon M1"}, // carrier — excluded
	})
	require.NoError(t, err)

	// Squad membership covers the full combat set M2..M6 (TASK-207): invasion
	// Xenon M5 and Kha'ak M2 join their group's squads; civilians never do.
	ids := raceWarshipClassIDs(sc)
	assert.Equal(t, map[domain.ShipClassID]bool{81: true, 11: true, 12: true, 73: true, 13: true}, ids)
}
