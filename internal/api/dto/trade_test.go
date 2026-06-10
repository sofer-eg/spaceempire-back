package dto_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/domain"
	traderepo "spaceempire/back/internal/persistence/trade"
)

// TestUnit_MarketResponse_NilPriceSerializesAsNull locks the wire contract: a
// good the station only sells (BuyPrice nil) must emit "buyPrice":null, NOT an
// omitted field. The SPA splits a factory market into «продукция»/«сырьё» by
// which price is null; an omitted field arrives as undefined and a good would
// wrongly land in both sections.
func TestUnit_MarketResponse_NilPriceSerializesAsNull(t *testing.T) {
	t.Parallel()

	sell := int64(16)
	owner := domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	resp := dto.MarketResponseFromRepo(owner, []traderepo.MarketEntry{
		{Owner: owner, GoodsType: 1, BuyPrice: nil, SellPrice: &sell, Stock: 5000, MaxStock: 10000},
	})

	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"buyPrice":null`)
	assert.Contains(t, string(raw), `"sellPrice":16`)
}
