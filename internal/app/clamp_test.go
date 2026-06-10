package app

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"spaceempire/back/internal/domain"
)

func TestUnit_ClampOutOfBounds(t *testing.T) {
	t.Parallel()

	const bounds = 1000.0
	sameSectorTarget := &domain.Course{Sector: 1, Pos: domain.Vec2{X: 500, Y: -300}}
	otherSectorTarget := &domain.Course{Sector: 2, Pos: domain.Vec2{X: 500, Y: -300}}
	dockedRef := &domain.EntityRef{Kind: domain.EntityKindStation, ID: 7}

	tests := []struct {
		name    string
		ship    domain.Ship
		wantPos domain.Vec2
	}{
		{
			name:    "inside bounds untouched",
			ship:    domain.Ship{ID: 1, SectorID: 1, Pos: domain.Vec2{X: 100, Y: 100}},
			wantPos: domain.Vec2{X: 100, Y: 100},
		},
		{
			name:    "out of bounds without final target falls back to origin",
			ship:    domain.Ship{ID: 2, SectorID: 1, Pos: domain.Vec2{X: 5000, Y: 0}},
			wantPos: domain.Vec2{X: 0, Y: 0},
		},
		{
			name:    "out of bounds with final target in same sector uses target pos",
			ship:    domain.Ship{ID: 3, SectorID: 1, Pos: domain.Vec2{X: 5000, Y: 0}, FinalTarget: sameSectorTarget},
			wantPos: domain.Vec2{X: 500, Y: -300},
		},
		{
			name:    "out of bounds with final target in other sector falls back to origin",
			ship:    domain.Ship{ID: 4, SectorID: 1, Pos: domain.Vec2{X: 5000, Y: 0}, FinalTarget: otherSectorTarget},
			wantPos: domain.Vec2{X: 0, Y: 0},
		},
		{
			name:    "docked ship far from origin is not corrected",
			ship:    domain.Ship{ID: 5, SectorID: 1, Pos: domain.Vec2{X: 9000, Y: 9000}, Docked: dockedRef},
			wantPos: domain.Vec2{X: 9000, Y: 9000},
		},
		{
			// (900, 900) is inside the ±bounds box but outside the
			// circle (||Pos|| = 900*sqrt2 ~ 1272 > 1000) — pins the
			// circle geometry chosen for 3.22.
			name:    "corner outside circle but inside box is corrected",
			ship:    domain.Ship{ID: 6, SectorID: 1, Pos: domain.Vec2{X: 900, Y: 900}},
			wantPos: domain.Vec2{X: 0, Y: 0},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ships := []domain.Ship{tt.ship}
			clampOutOfBounds(logger, tt.ship.SectorID, ships, bounds)
			assert.Equal(t, tt.wantPos, ships[0].Pos)
		})
	}
}
