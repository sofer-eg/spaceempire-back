package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnit_ConvertBalance_DropsExcludedGoods pins the one thing this converter
// does beyond copying: the two English-named catalog entries never reach
// balance.yaml (TASK-177). The exclusion has to live here rather than in the
// YAML, because the file is regenerated from the PHP source and a hand-edit
// would be silently undone on the next run.
func TestUnit_ConvertBalance_DropsExcludedGoods(t *testing.T) {
	t.Parallel()

	rows, err := convert(map[string]phpEntry{
		"104": {Name: "Поцелуй Evening", Space: 1, ObjectTypeID: 10},
		"113": {Name: "Снежинка", Space: 1, ObjectTypeID: 10},
		"114": {Name: `Gold ring with diamonds "charm"`, Space: 1, ObjectTypeID: 10},
		"115": {Name: "Эдельвейс Барностра", Space: 1, ObjectTypeID: 10},
	})
	require.NoError(t, err)

	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	assert.Equal(t, []int{113, 115}, ids,
		"104 and 114 are excluded by TASK-177; everything else is copied through, sorted by id")
}

// TestUnit_ConvertBalance_CopiesFieldsThrough guards the rest of the mapping:
// the canonical id is the PHP array key, not the entry's own "id" field (which
// is the legacy subtype and disagrees with the key for some entries).
func TestUnit_ConvertBalance_CopiesFieldsThrough(t *testing.T) {
	t.Parallel()

	rows, err := convert(map[string]phpEntry{
		"116": {
			Name:          "Артефакт",
			MinWarRate:    1,
			MinTradeRate:  2,
			MinRaceRate:   3,
			AvgPrice:      61,
			MaxPrice:      366,
			ProductionStd: 1,
			Space:         2,
			ObjectTypeID:  10,
		},
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	assert.Equal(t, yamlGoods{
		ID:            116,
		Name:          "Артефакт",
		MinWarRate:    1,
		MinTradeRate:  2,
		MinRaceRate:   3,
		AvgPrice:      61,
		MaxPrice:      366,
		ProductionStd: 1,
		Space:         2,
		ObjectTypeID:  10,
	}, rows[0])
}

func TestUnit_ConvertBalance_RejectsNonIntegerKey(t *testing.T) {
	t.Parallel()

	_, err := convert(map[string]phpEntry{"weapons": {Name: "Оружие"}})
	assert.Error(t, err)
}
