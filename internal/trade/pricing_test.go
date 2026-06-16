package trade_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/trade"
)

func TestUnit_PriceTier_BandThirds(t *testing.T) {
	t.Parallel()
	// Band [16, 96]: width 80. Integer cuts: lowCut = 16 + 80/3 = 42,
	// highCut = 16 + 160/3 = 69. A price at lowCut is medium; at highCut is high.
	const avg, max = int64(16), int64(96)
	cases := []struct {
		name  string
		price int64
		want  string
	}{
		{"at avg is low", 16, "low"},
		{"just below low cut", 41, "low"},
		{"at low cut is medium", 42, "medium"},
		{"just below high cut", 68, "medium"},
		{"at high cut is high", 69, "high"},
		{"at max is high", 96, "high"},
		{"above max is high", 200, "high"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, trade.PriceTier(tc.price, avg, max))
		})
	}
}

func TestUnit_PriceTier_FlatBand_RelativeToAvg(t *testing.T) {
	t.Parallel()
	// A good with no usable band (max<=avg) — e.g. a flat-priced
	// trade-station/pirbase ware — tiers relative to AvgPrice alone.
	const avg = int64(800)
	assert.Equal(t, "high", trade.PriceTier(800, avg, avg), "at avg is high")
	assert.Equal(t, "high", trade.PriceTier(1000, avg, avg), "above avg is high")
	assert.Equal(t, "low", trade.PriceTier(500, avg, avg), "below avg is low")
}

func TestUnit_PriceTier_ZeroBand_NeverPanics(t *testing.T) {
	t.Parallel()
	// avg==0 (special items such as slaves): everything >=0 tiers as high,
	// negative (never happens) as low. Just asserts no panic / sane output.
	assert.Equal(t, "high", trade.PriceTier(0, 0, 0))
	assert.Equal(t, "high", trade.PriceTier(50, 0, 0))
}
