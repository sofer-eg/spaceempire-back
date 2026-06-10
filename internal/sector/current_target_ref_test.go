package sector_test

import (
	"context"
	"testing"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// TestUnit_MoveCommand_SetsAndClearsCurrentTargetRef covers the lifecycle the
// SPA highlight depends on: a MoveCommand with TargetRef stores it on the
// ship, and a subsequent MoveCommand without a ref clears it (the player
// clicked empty space).
func TestUnit_MoveCommand_SetsAndClearsCurrentTargetRef(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	ref := &domain.EntityRef{Kind: domain.EntityKindStation, ID: 42}
	if err := w.Send(testSector, sector.MoveCommand{
		ShipID:    1,
		Target:    domain.Vec2{X: 100, Y: 0},
		TargetRef: ref,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	w.Tick(ctx)

	got := w.Snapshot(testSector).Ships[0].CurrentTargetRef
	if got == nil || *got != *ref {
		t.Fatalf("CurrentTargetRef after MoveCommand = %+v, want %+v", got, ref)
	}

	// Bare MoveCommand (no ref) clears the highlight ref.
	if err := w.Send(testSector, sector.MoveCommand{
		ShipID: 1,
		Target: domain.Vec2{X: 200, Y: 0},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	w.Tick(ctx)

	if got := w.Snapshot(testSector).Ships[0].CurrentTargetRef; got != nil {
		t.Fatalf("CurrentTargetRef after bare MoveCommand = %+v, want nil", got)
	}
}

// TestUnit_SetCourseCommand_MirrorsApproachIntoCurrentTargetRef verifies that
// arming the autopilot with an Approach copies the EntityRef into the ship
// so the SPA can paint the highlight before the ship even arrives.
func TestUnit_SetCourseCommand_MirrorsApproachIntoCurrentTargetRef(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	approach := &domain.EntityRef{Kind: domain.EntityKindShipyard, ID: 7}
	if err := w.Send(testSector, sector.SetCourseCommand{
		ShipID: 1,
		Course: &domain.Course{
			Sector:   testSector,
			Pos:      domain.Vec2{X: 50, Y: 50},
			Approach: approach,
		},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	w.Tick(ctx)

	got := w.Snapshot(testSector).Ships[0].CurrentTargetRef
	if got == nil || *got != *approach {
		t.Fatalf("CurrentTargetRef = %+v, want %+v", got, approach)
	}

	// A course without Approach clears the ref again.
	if err := w.Send(testSector, sector.SetCourseCommand{
		ShipID: 1,
		Course: &domain.Course{Sector: testSector, Pos: domain.Vec2{X: 0, Y: 0}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	w.Tick(ctx)

	if got := w.Snapshot(testSector).Ships[0].CurrentTargetRef; got != nil {
		t.Fatalf("CurrentTargetRef after non-approach course = %+v, want nil", got)
	}
}

// TestUnit_PlainArrival_ClearsCurrentTargetRef confirms the "ship reached
// destination and is not parking" branch: once the per-tick Target hits
// the destination and the ship is not in approach mode, the highlight
// ref drops automatically.
func TestUnit_PlainArrival_ClearsCurrentTargetRef(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 10}},
	)

	ref := &domain.EntityRef{Kind: domain.EntityKindStation, ID: 5}
	if err := w.Send(testSector, sector.MoveCommand{
		ShipID:    1,
		Target:    domain.Vec2{X: 5, Y: 0}, // close enough to arrive in one tick
		TargetRef: ref,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// First tick: command applied, ship steers toward target.
	w.Tick(ctx)
	// Second tick: ship reaches target (overshoot-clamped to arrival).
	w.Tick(ctx)

	snap := w.Snapshot(testSector).Ships[0]
	if snap.Target != nil {
		t.Fatalf("Target after arrival = %+v, want nil", snap.Target)
	}
	if snap.CurrentTargetRef != nil {
		t.Fatalf("CurrentTargetRef after plain arrival = %+v, want nil", snap.CurrentTargetRef)
	}
}
