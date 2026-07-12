package sector_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/bus"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/clock"
	"spaceempire/back/internal/sector"
)

// jumpDriveTime is the FakeClock reference the jump-drive tests pin their
// cooldown assertions to.
var jumpDriveTime = time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

// newJumpDriveWorker boots a single-sector worker (owning sector 1) wired for
// handoff over the shared handoffTopology (sectors 1 and 2), so a jump 1→2
// targets a real sector. The clock is a FakeClock pinned to jumpDriveTime so
// cooldown tests are deterministic.
func newJumpDriveWorker(t *testing.T, repo sector.ShipRepo, b bus.Bus, cfg sector.Config, initial []domain.Ship) *sector.Worker {
	t.Helper()
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = time.Second
	}
	return sector.NewWorker(
		0,
		cfg,
		clock.NewFakeClock(jumpDriveTime),
		repo,
		nil,
		map[domain.SectorID][]domain.Ship{1: initial},
		sector.WithHandoff(handoffTopology(), b),
	)
}

func jumpDriveReply(t *testing.T, w *sector.Worker, cmd sector.JumpDriveCommand) sector.CmdResult {
	t.Helper()
	reply := make(chan sector.CmdResult, 1)
	cmd.Reply = reply
	require.NoError(t, w.Send(1, cmd))
	w.Tick(context.Background())
	select {
	case r := <-reply:
		return r
	case <-time.After(time.Second):
		t.Fatal("no reply from JumpDriveCommand")
		return sector.CmdResult{}
	}
}

func jumpDriveShip(pid domain.PlayerID, level int) domain.Ship {
	return domain.Ship{
		ID: 1, PlayerID: pid, SectorID: 1,
		Pos: domain.Vec2{X: 0, Y: 0}, MaxSpeed: 10,
		Shield: 100, MaxShield: 100,
		Equipment: []domain.InstalledEquipment{{Type: "up_jump_drive", Level: level}},
	}
}

func TestUnit_JumpDriveCommand_Success(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{jumpDriveShip(pid, 1)})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err)
	assert.Empty(t, w.Snapshot(1).Ships, "ship must leave source sector")

	require.Len(t, repo.saves, 1, "Save must be invoked exactly once")
	saved := repo.saves[0]
	assert.Equal(t, domain.SectorID(2), saved.SectorID)
	assert.Equal(t, 0, saved.Shield, "faithful cost: shield drained to 0")
	assert.Equal(t, jumpDriveTime, saved.LastJumpAt, "cooldown stamp = worker clock now")

	msgs := b.snapshot()
	require.NotEmpty(t, msgs, "expect at least the intake event")
	assert.Equal(t, "sector.2.intake", msgs[0].topic)
	var ev sector.JumpEvent
	require.NoError(t, json.Unmarshal(msgs[0].payload, &ev))
	assert.Equal(t, domain.SectorID(1), ev.SourceSector)
	assert.Equal(t, domain.SectorID(2), ev.TargetSector)
	assert.Equal(t, domain.ShipID(1), ev.Ship.ID)
	assert.Equal(t, 0, ev.Ship.Shield, "relocated ship carries the drained shield")
}

func TestUnit_JumpDriveCommand_NoModule(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	ship := jumpDriveShip(pid, 1)
	ship.Equipment = nil // no up_jump_drive
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{ship})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	assert.ErrorIs(t, res.Err, sector.ErrEquipmentRequired)
	assert.Empty(t, repo.saves, "no relocation on rejection")
	assert.Empty(t, b.snapshot(), "no publish on rejection")
	assert.Len(t, w.Snapshot(1).Ships, 1, "ship stays in source sector")
}

func TestUnit_JumpDriveCommand_NoShieldGenerator(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	ship := jumpDriveShip(pid, 1)
	ship.Shield = 0
	ship.MaxShield = 0 // no working shield generator
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{ship})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	assert.ErrorIs(t, res.Err, sector.ErrShieldRequired)
	assert.Empty(t, repo.saves)
	assert.Len(t, w.Snapshot(1).Ships, 1)
}

func TestUnit_JumpDriveCommand_OnCooldown(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	ship := jumpDriveShip(pid, 1)
	ship.LastJumpAt = jumpDriveTime.Add(-30 * time.Minute) // 30 min ago, L1 cd = 60 min
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{ship})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	assert.ErrorIs(t, res.Err, sector.ErrJumpOnCooldown)
	assert.Empty(t, repo.saves, "no relocation while on cooldown")
	assert.Empty(t, b.snapshot())
	assert.Len(t, w.Snapshot(1).Ships, 1)
}

func TestUnit_JumpDriveCommand_CooldownExpired(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	ship := jumpDriveShip(pid, 1)
	ship.LastJumpAt = jumpDriveTime.Add(-61 * time.Minute) // past L1 cd = 60 min
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{ship})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err, "an expired cooldown must let the jump through")
	assert.Empty(t, w.Snapshot(1).Ships, "ship relocated out of source sector")
	require.Len(t, repo.saves, 1)
	assert.Equal(t, jumpDriveTime, repo.saves[0].LastJumpAt, "cooldown re-stamped to now")
}

func TestUnit_JumpDriveCommand_Level2ShorterCooldown(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	ship := jumpDriveShip(pid, 2)
	// 45 min ago: still on cooldown for L1 (60 min), but past L2's 30 min.
	ship.LastJumpAt = jumpDriveTime.Add(-45 * time.Minute)
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{ship})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err, "level 2 uses the 30-min cooldown, so 45 min is enough")
	assert.Empty(t, w.Snapshot(1).Ships)
}

func TestUnit_JumpDriveCommand_Docked(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	ship := jumpDriveShip(pid, 1)
	ship.Docked = &domain.EntityRef{Kind: domain.EntityKindStation, ID: 3}
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{ship})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	assert.ErrorIs(t, res.Err, sector.ErrShipDocked)
	assert.Empty(t, repo.saves)
	assert.Len(t, w.Snapshot(1).Ships, 1)
}

func TestUnit_JumpDriveCommand_Ownership(t *testing.T) {
	t.Parallel()

	w := newJumpDriveWorker(t, &fakeShipRepo{}, &fakeBus{}, sector.Config{}, []domain.Ship{jumpDriveShip(7, 1)})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: 999, ShipID: 1, TargetSectorID: 2})
	assert.ErrorIs(t, res.Err, sector.ErrForbidden)
}

func TestUnit_JumpDriveCommand_ShipNotFound(t *testing.T) {
	t.Parallel()

	w := newJumpDriveWorker(t, &fakeShipRepo{}, &fakeBus{}, sector.Config{}, nil)

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: 7, ShipID: 999, TargetSectorID: 2})
	assert.ErrorIs(t, res.Err, sector.ErrShipNotFound)
}

func TestUnit_JumpDriveCommand_ForbiddenSector(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	cfg := sector.Config{JumpDriveForbiddenSectors: []domain.SectorID{1}} // ship's source sector
	w := newJumpDriveWorker(t, repo, b, cfg, []domain.Ship{jumpDriveShip(pid, 1)})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	assert.ErrorIs(t, res.Err, sector.ErrJumpForbiddenSector)
	assert.Empty(t, repo.saves)
	assert.Len(t, w.Snapshot(1).Ships, 1)
}

func TestUnit_JumpDriveCommand_InvalidTargetSector(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{jumpDriveShip(pid, 1)})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 999})

	assert.ErrorIs(t, res.Err, sector.ErrInvalidSector)
	assert.Empty(t, repo.saves)
	assert.Len(t, w.Snapshot(1).Ships, 1)
}

func TestUnit_JumpDriveCommand_SelfSectorRejected(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{jumpDriveShip(pid, 1)})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 1})

	assert.ErrorIs(t, res.Err, sector.ErrInvalidSector)
	assert.Len(t, w.Snapshot(1).Ships, 1, "self-jump is a no-op")
}

func TestUnit_JumpDriveCommand_ExecuteJumpFailureRollsBackPayment(t *testing.T) {
	t.Parallel()

	// repo.Save fails inside executeJump (DB down), so the ship is NOT evicted.
	repo := &fakeShipRepo{saveErr: errors.New("db down")}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{jumpDriveShip(pid, 1)})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "save ship", "propagates the executeJump error")

	snap := w.Snapshot(1)
	require.Len(t, snap.Ships, 1, "ship must stay in the source sector when the jump fails")
	assert.Equal(t, 100, snap.Ships[0].Shield, "shield restored — no partial payment on a failed jump")
	assert.True(t, snap.Ships[0].LastJumpAt.IsZero(), "cooldown must not be stamped on a failed jump")
	assert.Empty(t, b.snapshot(), "no handoff published when Save failed")
}

// antijumpBlocker builds a ship carrying a powered up_antijump field at pos in
// sector 1. PlayerID 42 (≠ the jumper's 7) proves the block is faithful to SP
// DoJump: any owned ship jams the jump, with no hostility filter.
func antijumpBlocker(id domain.ShipID, pos domain.Vec2, energy int) domain.Ship {
	return domain.Ship{
		ID: id, PlayerID: 42, SectorID: 1, Pos: pos, Energy: energy,
		Equipment: []domain.InstalledEquipment{{Type: "up_antijump", Level: 1}},
	}
}

func TestUnit_JumpDriveCommand_BlockedByAntijump(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	// Jumper at origin; a powered antijump ship 100 units away (< default 640).
	ships := []domain.Ship{jumpDriveShip(pid, 1), antijumpBlocker(2, domain.Vec2{X: 100}, 50)}
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, ships)

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	assert.ErrorIs(t, res.Err, sector.ErrJumpBlockedByAntijump)
	assert.Empty(t, repo.saves, "no relocation when the jump is jammed")
	assert.Empty(t, b.snapshot(), "no handoff published when the jump is jammed")

	jumper, ok := snapshotShipByID(w.Snapshot(1), 1)
	require.True(t, ok, "jumper stays in the source sector")
	assert.Equal(t, 100, jumper.Shield, "shield not paid on a blocked jump")
	assert.True(t, jumper.LastJumpAt.IsZero(), "cooldown not stamped on a blocked jump")
}

func TestUnit_JumpDriveCommand_AntijumpOutOfRange(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	// Powered antijump ship 700 units away (> default 640) — does not reach.
	ships := []domain.Ship{jumpDriveShip(pid, 1), antijumpBlocker(2, domain.Vec2{X: 700}, 50)}
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, ships)

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err, "an out-of-range antijump field must not block the jump")
	_, ok := snapshotShipByID(w.Snapshot(1), 1)
	assert.False(t, ok, "jumper relocated out of the source sector")
	require.Len(t, repo.saves, 1)
	assert.Equal(t, domain.SectorID(2), repo.saves[0].SectorID)
}

func TestUnit_JumpDriveCommand_AntijumpUnownedBlocker(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	// In range and powered, but the carrier is unowned (PlayerID==0 — spacesuit/
	// legacy): faithful SP DoJump `object_owner != 0` means it projects no field.
	unowned := antijumpBlocker(2, domain.Vec2{X: 100}, 50)
	unowned.PlayerID = 0
	ships := []domain.Ship{jumpDriveShip(pid, 1), unowned}
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, ships)

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err, "an unowned antijump carrier must not block the jump")
	_, ok := snapshotShipByID(w.Snapshot(1), 1)
	assert.False(t, ok, "jumper relocated out of the source sector")
}

func TestUnit_JumpDriveCommand_AntijumpUnpowered(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	// In range but Energy==0 — an unpowered field does not block (stealth pattern).
	ships := []domain.Ship{jumpDriveShip(pid, 1), antijumpBlocker(2, domain.Vec2{X: 100}, 0)}
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, ships)

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err, "an unpowered antijump field must not block the jump")
	_, ok := snapshotShipByID(w.Snapshot(1), 1)
	assert.False(t, ok, "jumper relocated out of the source sector")
}

func TestUnit_JumpDriveCommand_AntijumpNoModule(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	// A powered ship in range but WITHOUT up_antijump — no field, no block.
	bystander := antijumpBlocker(2, domain.Vec2{X: 100}, 50)
	bystander.Equipment = nil
	ships := []domain.Ship{jumpDriveShip(pid, 1), bystander}
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, ships)

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err, "a ship without up_antijump must not block the jump")
	_, ok := snapshotShipByID(w.Snapshot(1), 1)
	assert.False(t, ok, "jumper relocated out of the source sector")
}

func TestUnit_JumpDriveCommand_AntijumpSelfNotBlocking(t *testing.T) {
	t.Parallel()

	repo := &fakeShipRepo{}
	b := &fakeBus{}
	pid := domain.PlayerID(7)
	// The jumper is the ONLY up_antijump carrier — its own field must not jam it.
	jumper := jumpDriveShip(pid, 1)
	jumper.Energy = 50
	jumper.Equipment = append(jumper.Equipment, domain.InstalledEquipment{Type: "up_antijump", Level: 1})
	w := newJumpDriveWorker(t, repo, b, sector.Config{}, []domain.Ship{jumper})

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: pid, ShipID: 1, TargetSectorID: 2})

	require.NoError(t, res.Err, "a ship's own antijump field must not block its own jump")
	assert.Empty(t, w.Snapshot(1).Ships, "jumper relocated out of the source sector")
}

func TestUnit_JumpDriveCommand_HandoffUnavailable(t *testing.T) {
	t.Parallel()

	// Worker without WithHandoff.
	w := sector.NewWorker(
		0,
		sector.Config{TickInterval: time.Second},
		clock.NewFakeClock(jumpDriveTime),
		nil, nil,
		map[domain.SectorID][]domain.Ship{1: {jumpDriveShip(7, 1)}},
	)

	res := jumpDriveReply(t, w, sector.JumpDriveCommand{PlayerID: 7, ShipID: 1, TargetSectorID: 2})
	assert.ErrorIs(t, res.Err, sector.ErrHandoffUnavailable)
}
