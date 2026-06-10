package sector_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

func TestUnit_MoveCommand_Reply_Success(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	reply := make(chan sector.CmdResult, 1)
	if err := w.Send(testSector, sector.MoveCommand{
		ShipID: 1,
		Target: domain.Vec2{X: 10, Y: 0},
		Reply:  reply,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	w.Tick(ctx)

	select {
	case res := <-reply:
		if res.Err != nil {
			t.Fatalf("reply Err = %v, want nil", res.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("no reply within 1s")
	}
}

func TestUnit_MoveCommand_Reply_ShipNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		nil,
	)

	reply := make(chan sector.CmdResult, 1)
	if err := w.Send(testSector, sector.MoveCommand{
		ShipID: 42,
		Target: domain.Vec2{X: 10, Y: 0},
		Reply:  reply,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	w.Tick(ctx)

	select {
	case res := <-reply:
		if !errors.Is(res.Err, sector.ErrShipNotFound) {
			t.Fatalf("reply Err = %v, want ErrShipNotFound", res.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("no reply within 1s")
	}
}

func TestUnit_MoveCommand_NoReply_OK(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	w := newSingleSectorWorker(t,
		sector.Config{TickInterval: time.Second},
		clock.NewRealClock(),
		nil,
		[]domain.Ship{{ID: 1, Pos: domain.Vec2{}, MaxSpeed: 1}},
	)

	if err := w.Send(testSector, sector.MoveCommand{
		ShipID: 1,
		Target: domain.Vec2{X: 5, Y: 0},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	w.Tick(ctx)

	if got := w.Snapshot(testSector).Ships[0].Target; got == nil || got.X != 5 {
		t.Fatalf("Target = %+v, want X=5", got)
	}
}
