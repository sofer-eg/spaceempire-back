package trade_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/trade"
)

func TestUnit_PriceTier_BandThirds(t *testing.T) {
	t.Parallel()
	// Band [16, 96]: width 80. Boundaries by cross-multiplication (3*offset vs
	// band): medium at offset>=80/3≈26.67 (price 43), high at offset>=160/3≈53.33
	// (price 70).
	const avg, max = int64(16), int64(96)
	cases := []struct {
		name  string
		price int64
		want  string
	}{
		{"at avg is low", 16, "low"},
		{"just below medium cut", 42, "low"},
		{"at medium cut is medium", 43, "medium"},
		{"just below high cut", 69, "medium"},
		{"at high cut is high", 70, "high"},
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

func TestUnit_PriceTier_NarrowBands(t *testing.T) {
	t.Parallel()
	// Narrow bands are the regression target: with the old integer cuts
	// (avg+band/3) band<3 truncated band/3 to 0, collapsing "medium" (and at
	// band==1 "low" too). Cross-multiplication keeps all three tiers reachable.
	cases := []struct {
		name     string
		price    int64
		avg, max int64
		want     string
	}{
		// band==2, [10,12]: medium at offset>=2/3 (price 11), high at offset>=4/3 (price 12).
		{"band2 at avg is low", 10, 10, 12, "low"},
		{"band2 mid is medium", 11, 10, 12, "medium"},
		{"band2 at max is high", 12, 10, 12, "high"},
		// band==1, [10,11]: medium at offset>=1/3 (price 11 -> 3>=1), high at offset>=2/3 of 1.
		{"band1 at avg is low", 10, 10, 11, "low"},
		{"band1 at max is high", 11, 10, 11, "high"},
		// out-of-band positions on a real band.
		{"below avg is low", 5, 10, 20, "low"},
		{"above max is high", 99, 10, 20, "high"},
		// flat band (max==avg): tiers relative to avg.
		{"flat at avg is high", 10, 10, 10, "high"},
		{"flat below avg is low", 9, 10, 10, "low"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, trade.PriceTier(tc.price, tc.avg, tc.max))
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
