package sector

import (
	"errors"

	"spaceempire/back/internal/domain"
)

// jumpDriveModuleType is the ct_updates type gating the seamless jump.
const jumpDriveModuleType = "up_jump_drive"

// antijumpModuleType is the ct_updates type whose powered field jams a nearby
// seamless jump (TASK-100.3.8, SP DoJump hyper-interference gate).
const antijumpModuleType = "up_antijump"

// jumpArrivalSpread is the half-width of the random arrival box around the
// target sector's centre (TASK-100.3.7, faithful SP DoJump: pos = 1600 -
// rand()*3200, i.e. uniform in [-1600, 1600] around the centre).
const jumpArrivalSpread = 1600.0

var (
	// ErrShieldRequired is reported by JumpDriveCommand when the ship has no
	// working shield generator (MaxShield 0 — no up_shield or one knocked off in
	// combat). A jump's faithful cost is draining the shield, so a ship that
	// cannot raise one may not jump. HTTP maps it to 422.
	ErrShieldRequired = errors.New("sector: shield generator required")
	// ErrJumpOnCooldown is reported by JumpDriveCommand when the jump drive has
	// not finished its real-time cooldown since the last hop. HTTP maps it to 429.
	ErrJumpOnCooldown = errors.New("sector: jump drive on cooldown")
	// ErrJumpForbiddenSector is reported by JumpDriveCommand when the ship's
	// current sector is in Config.JumpDriveForbiddenSectors. HTTP maps it to 400.
	ErrJumpForbiddenSector = errors.New("sector: jump forbidden in this sector")
	// ErrInvalidSector is reported by JumpDriveCommand when the target sector is
	// unknown to the topology or equals the ship's current sector. HTTP maps it
	// to 400.
	ErrInvalidSector = errors.New("sector: invalid target sector")
	// ErrJumpBlockedByAntijump is reported by JumpDriveCommand when the seamless
	// jump is jammed by hyper-interference in the same sector: another powered
	// up_antijump ship within Config.AntijumpRange (TASK-100.3.8) or a deployed
	// hyper-interference generator within Config.JammerRange (TASK-131). Both
	// are branches of the same SP DoJump gate, and the player sees the same
	// "прыжок невозможен" outcome, so they share one sentinel. HTTP maps it to
	// 409.
	ErrJumpBlockedByAntijump = errors.New("sector: jump blocked by antijump field")
)

// JumpDriveCommand is the player-issued seamless (gateless) sector jump
// (TASK-100.3.7, port of SP DoJump mode 0). Unlike JumpCommand it needs no
// gate: the ship folds space to any existing sector at the cost of its shield,
// gated on an installed up_jump_drive and a real-time cooldown by module level.
// The worker that owns the ship's current sector validates everything, then
// reuses executeJump for the relocation + handoff.
type JumpDriveCommand struct {
	PlayerID       domain.PlayerID
	ShipID         domain.ShipID
	TargetSectorID domain.SectorID
	Reply          chan<- CmdResult
}

func (c JumpDriveCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case w.topology == nil || w.bus == nil:
		res.Err = ErrHandoffUnavailable
	case ship.Docked != nil:
		// MVP scoping: a seamless jump out of a dock would have to release the
		// station/host-ship clamps (docking internals) — out of this task's
		// scope. The value of a jump drive is escaping open space, so we require
		// the player to undock first. Follow-up may lift this (SP DoJump mode 0
		// set location=0 inline).
		res.Err = ErrShipDocked
	case shipEquipmentLevel(ship, jumpDriveModuleType) < 1:
		res.Err = ErrEquipmentRequired
	case ship.MaxShield <= 0:
		// Faithful cost: the jump drains the shield, so a ship with no working
		// generator (no up_shield, or one knocked off) cannot pay it (SP DoJump
		// shield_lvl<1).
		res.Err = ErrShieldRequired
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}

	// Real-time cooldown by module level (SP DoJump: level 1 = 3600 s, level 2 =
	// 1800 s). A zero LastJumpAt means the ship never jumped — no cooldown.
	cd := w.cfg.jumpDriveCooldown(shipEquipmentLevel(ship, jumpDriveModuleType))
	if !ship.LastJumpAt.IsZero() && w.clock.Now().Sub(ship.LastJumpAt) < cd {
		res.Err = ErrJumpOnCooldown
		replyOnce(c.Reply, res)
		return
	}

	// No-jump zone: block leaving a forbidden source sector (SP DoJump 215/203).
	if w.cfg.sectorForbidden(s.sectorID) {
		res.Err = ErrJumpForbiddenSector
		replyOnce(c.Reply, res)
		return
	}

	// Mode 0 targets ANY existing sector (no hop limit) — but not the ship's own
	// (a self-jump would round-trip through this sector's intake for nothing).
	if c.TargetSectorID == s.sectorID {
		res.Err = ErrInvalidSector
		replyOnce(c.Reply, res)
		return
	}
	center, ok := w.sectorCenter(c.TargetSectorID)
	if !ok {
		res.Err = ErrInvalidSector
		replyOnce(c.Reply, res)
		return
	}

	// Hyper-interference (SP DoJump): a powered up_antijump ship in range
	// (TASK-100.3.8) or a deployed hyper-interference generator in range
	// (TASK-131, the SP class-7 drone fallback) jams the jump. Checked BEFORE
	// paying the shield/cooldown so a blocked jump costs nothing.
	if w.antijumpActive(s, ship) || w.jammerActive(s, ship) {
		res.Err = ErrJumpBlockedByAntijump
		replyOnce(c.Reply, res)
		return
	}

	// All gates passed — pay the shield, stamp the cooldown, relocate. Setting
	// these on the RAM ship before executeJump makes executeJump's `*ship` copy
	// carry them into the target sector and the persisted row (Shield=0,
	// LastJumpAt=now survive the handoff). executeJump only evicts the ship on
	// success; on failure (e.g. repo.Save with the DB down) the ship stays in
	// this sector's live RAM, so the payment must be rolled back — otherwise the
	// player is left with a drained shield and a running cooldown but no jump.
	prevShield, prevJump := ship.Shield, ship.LastJumpAt
	ship.Shield = 0
	ship.LastJumpAt = w.clock.Now()
	arrival := jumpArrivalPos(center, w.rng)
	if err := executeJump(w, s, ship, c.TargetSectorID, arrival); err != nil {
		ship.Shield, ship.LastJumpAt = prevShield, prevJump
		res.Err = err
	}
	replyOnce(c.Reply, res)
}

// antijumpActive reports whether another powered up_antijump ship is within
// Config.AntijumpRange of the jumper in the same sector (TASK-100.3.8). Faithful
// to SP DoJump: ANY owned ship carrying an active field jams the jump — no
// hostility filter. Energy<=0 means the field has no power (the "Energy==0 =
// module unpowered" pattern, as with stealth), so it does not block. The jumper's
// own field never blocks its own jump.
//
// "Owned" ports SP DoJump's `object_owner != 0`: an unowned carrier
// (PlayerID==0 — spacesuit/legacy/unknown) projects no field. NPCs are owned by
// a system player (PlayerID != 0), so they still jam per the agreed "any owned
// ship" rule.
func (w *Worker) antijumpActive(s *sectorState, jumper *domain.Ship) bool {
	for id, other := range s.ships {
		if id == jumper.ID {
			continue
		}
		if other.PlayerID == 0 {
			continue
		}
		if shipEquipmentLevel(other, antijumpModuleType) < 1 {
			continue
		}
		if other.Energy <= 0 {
			continue
		}
		if other.Pos.Sub(jumper.Pos).Length() <= w.cfg.AntijumpRange {
			return true
		}
	}
	return false
}

// jammerActive reports whether a live hyper-interference generator ("Генератор
// гипер-помех", TASK-131) sits within Config.JammerRange of the jumper. Ports
// SP DoJump's fallback `select id from drones where class=7 and
// sector=object_sector`: it fires only for the jumper's OWN sector (jumping
// INTO a jammed sector is allowed, as in the original) and carries NO owner
// filter — the generator jams its owner and their allies exactly as it jams
// everyone else. Only Built generators count; a destroyed one has already left
// statics.Jammers via killStatic.
//
// The only deliberate departure from SP is the radius: the original blanketed
// the whole sector, we scope it to Config.JammerRange (see the Config comment).
func (w *Worker) jammerActive(s *sectorState, jumper *domain.Ship) bool {
	for i := range s.statics.Jammers {
		jam := s.statics.Jammers[i]
		if !jam.Built {
			continue
		}
		if jam.Pos.Sub(jumper.Pos).Length() <= w.cfg.JammerRange {
			return true
		}
	}
	return false
}

// sectorCenter returns the centre point of the given sector's bounds and whether
// the sector exists in the topology. Sectors are origin-centred in the current
// map, so this is (0,0) for them; deriving it from Bounds keeps offset sectors
// correct too.
func (w *Worker) sectorCenter(id domain.SectorID) (domain.Vec2, bool) {
	for _, sec := range w.topology.Sectors() {
		if sec.ID == id {
			return domain.Vec2{
				X: (sec.Bounds.Min.X + sec.Bounds.Max.X) / 2,
				Y: (sec.Bounds.Min.Y + sec.Bounds.Max.Y) / 2,
			}, true
		}
	}
	return domain.Vec2{}, false
}

// jumpArrivalPos scatters the arriving ship into a ±jumpArrivalSpread box around
// the target sector's centre (SP DoJump mode 0). Uses the worker's loot RNG —
// arrival precision is not security-sensitive and tests assert sector
// membership, not the exact point.
func jumpArrivalPos(center domain.Vec2, rng RNG) domain.Vec2 {
	return domain.Vec2{
		X: center.X + jumpArrivalSpread - rng.Float64()*2*jumpArrivalSpread,
		Y: center.Y + jumpArrivalSpread - rng.Float64()*2*jumpArrivalSpread,
	}
}
