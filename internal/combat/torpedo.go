package combat

import (
	"math"
	"time"

	"spaceempire/back/internal/domain"
)

// TorpedoSpec captures the per-class torpedo magnitudes for the spaceempire
// balance profile (ЧТЗ doc-1 §5.1). It mirrors MissileSpec / DroneSpec: the
// combat package owns weapon magnitudes as Go data, not YAML. The fields are
// exactly those copied into a domain.Torpedo at launch (Damage, Speed, Accel,
// TurnRate, HitRadius, SplashRadius, HP) plus TTL → ExpiresAt.
//
// Sub-task .4 adds combat.DefaultTorpedoSpec(class) (and LaunchTorpedo /
// TickTorpedo) on top of torpedoSpecsByClass; this sub-task only declares the
// per-class magnitude source.
type TorpedoSpec struct {
	// Damage is applied at detonation to every target inside SplashRadius
	// (torpedoes are an area weapon, ЧТЗ §3 FR-007).
	Damage int
	// Speed is the upper bound on |Vel| in world units per second. Torpedoes
	// are deliberately slower than missiles (DefaultMissileSpec.Speed): heavy
	// ordnance that is hard to dodge up close but easy to outrun.
	Speed float64
	// Accel is the per-second engine acceleration along Direction.
	Accel float64
	// TurnRate is the maximum heading change per second (radians). Lower than
	// a missile's — a torpedo lumbers toward its target.
	TurnRate float64
	// HitRadius is the detonation-trigger distance to the target.
	HitRadius float64
	// SplashRadius is the area-damage radius at detonation. Always > 0 — the
	// trait that sets torpedoes apart from missiles and drones (friendly-fire
	// included, ЧТЗ §3 FR-007).
	SplashRadius float64
	// HP is the torpedo's own hull: it is a targetable, shoot-downable object
	// (ЧТЗ §3 FR-008), unlike a fire-and-forget missile.
	HP int
	// TTL is the wall-clock lifetime from launch to forced expire.
	TTL time.Duration
}

// torpedoSpecsByClass holds the balance profile for each torpedo ammunition
// class: 2 = gt23 "Огненная Буря", 3 = gt24 "Святая Торпеда". The magnitudes
// are a spaceempire balance decision — relative parity with StarWind ct_drones,
// not the literal 100k/1M numbers (ЧТЗ §5.1, C-01). A torpedo hits far harder
// than a missile (DefaultMissileSpec.Damage = 30) but is slower, less nimble,
// has a finite TTL, carries its own HP and deals area damage. Class 3 beats
// class 2 on every axis (faster, harder-hitting, longer-lived, bigger blast)
// and is the pricier fit. Sub-task .4 consumes this via DefaultTorpedoSpec.
var torpedoSpecsByClass = map[int]TorpedoSpec{
	2: {
		Damage:       150, // 5× a missile — the heavy-hitter profile
		Speed:        30,  // < missile (80): noticeably slower
		Accel:        15,
		TurnRate:     math.Pi / 3, // 60°/s — lumbering vs a missile's 180°/s
		HitRadius:    14,
		SplashRadius: 40,
		HP:           40, // shoot-downable
		TTL:          30 * time.Second,
	},
	3: {
		Damage:       600, // 4× class 2, 20× a missile
		Speed:        50,  // faster than class 2, still < missile
		Accel:        30,
		TurnRate:     math.Pi / 2, // 90°/s — nimbler than class 2, still < missile
		HitRadius:    16,
		SplashRadius: 70, // bigger blast
		HP:           60, // sturdier
		TTL:          60 * time.Second,
	},
}

// Torpedo strafe / friction coefficients. Unlike per-class Damage/Speed these
// are uniform across both classes (the TorpedoSpec carries no field for them),
// so they live as package constants — same values the missile integrator uses
// (SP mis_strafe = 0.8 * accel, mis_rub = -0.1 * speed).
const (
	torpedoStrafeK   = 0.8
	torpedoFrictionK = 0.1
)

// DefaultTorpedoSpec returns the balance profile for an ammunition class: 2
// (gt23 "Огненная Буря") or 3 (gt24 "Святая Торпеда"). The HTTP handler already
// validates class ∈ {2,3} before a launch reaches the worker; an unknown class
// falls back to the class-2 profile so this accessor never yields a degenerate
// (zero-TTL, zero-Speed) spec. Consumed by LaunchTorpedo at spawn.
func DefaultTorpedoSpec(class int) TorpedoSpec {
	if spec, ok := torpedoSpecsByClass[class]; ok {
		return spec
	}
	return torpedoSpecsByClass[2]
}

// TorpedoOutcome reports the per-tick verdict TickTorpedo returns to the sector
// worker. Shoot-down (HP<=0) is a separate sub-task (TASK-100.3.5.6); this set
// only covers the homing-and-detonation life-cycle.
type TorpedoOutcome uint8

const (
	// TorpedoKeep means the torpedo is still in flight; the worker keeps it in
	// the live set and marks it dirty for the periodic persistence batch.
	TorpedoKeep TorpedoOutcome = iota
	// TorpedoHit means the torpedo reached within HitRadius of an alive target
	// this tick. The worker detonates it (emits an impact carrying the splash
	// centre + radius and removes the torpedo). The area damage itself is
	// applied by TASK-100.3.5.5; this sub-task only marks the detonation.
	TorpedoHit
	// TorpedoExpired means the torpedo's TTL ran out without reaching its
	// target. The worker removes it without any damage.
	TorpedoExpired
)

// LaunchTorpedo builds a fresh Torpedo fired from attacker at target. It mirrors
// LaunchMissile: the torpedo spawns at the attacker's position, inherits its
// velocity (a strafing pilot does not eject backwards-drifting ordnance) and
// heading, and seeds LastTargetPos so it has a fallback course once the target
// dies or leaves the sector. The per-class magnitudes (Damage/Speed/Accel/
// TurnRate/HitRadius/SplashRadius/HP and TTL→ExpiresAt) are copied from spec
// into the persisted row, so a torpedo restored from the DB ticks with its own
// stored profile. Pure builder — the sector worker allocates the id (DB-assigned)
// and inserts it into the live set.
func LaunchTorpedo(
	id domain.TorpedoID,
	class int,
	spec TorpedoSpec,
	attacker *domain.Ship,
	target domain.EntityRef,
	targetPos domain.Vec2,
	now time.Time,
) *domain.Torpedo {
	dir := attacker.Direction
	if dir.IsZero() {
		dir = domain.Vec2{X: 1, Y: 0}
	}
	return &domain.Torpedo{
		ID:            id,
		SectorID:      attacker.SectorID,
		OwnerShipID:   attacker.ID,
		PlayerID:      attacker.PlayerID,
		Pos:           attacker.Pos,
		Vel:           attacker.Vel,
		Direction:     dir,
		Target:        target,
		LastTargetPos: targetPos,
		Class:         class,
		Damage:        spec.Damage,
		Speed:         spec.Speed,
		Accel:         spec.Accel,
		TurnRate:      spec.TurnRate,
		HitRadius:     spec.HitRadius,
		SplashRadius:  spec.SplashRadius,
		HP:            spec.HP,
		ExpiresAt:     now.Add(spec.TTL),
	}
}

// TickTorpedo integrates one torpedo by dt seconds, homing toward its target.
// It is the heavy-ordnance sibling of TickMissile and reads its homing
// magnitudes from the torpedo itself (TurnRate/Accel/Speed/HitRadius — copied
// from the per-class spec at launch and persisted), so a torpedo restored from
// the DB keeps its profile without a spec lookup. Strafe and friction
// coefficients are uniform across classes (torpedoStrafeK / torpedoFrictionK).
//
// targetAlive=true when the sector worker found the target (a ship or a
// destructible static) alive in its live set; targetPos must then be the
// target's current Pos and the torpedo refreshes LastTargetPos before steering.
// When targetAlive=false the torpedo steers blindly toward t.LastTargetPos and
// hit checks are suppressed — a lost target can only run out the TTL (same
// fallback as TickMissile).
//
// All mutations of t happen in this function; the worker relies on the
// one-writer-per-sector invariant so no other goroutine touches t.
func TickTorpedo(
	t *domain.Torpedo,
	targetPos domain.Vec2,
	targetAlive bool,
	dt float64,
	now time.Time,
) TorpedoOutcome {
	if t == nil {
		return TorpedoExpired
	}
	if !now.Before(t.ExpiresAt) {
		return TorpedoExpired
	}
	if targetAlive {
		t.LastTargetPos = targetPos
	} else {
		targetPos = t.LastTargetPos
	}

	delta := targetPos.Sub(t.Pos)
	rangeEq := delta.Length()

	noTurn := false
	var targetDir domain.Vec2
	if rangeEq > 1 {
		targetDir = delta.Scale(1.0 / rangeEq)
	} else {
		targetDir = t.Direction
		noTurn = true
	}

	speedEq := t.Vel.Length()
	var speedDir domain.Vec2
	if speedEq > 1 {
		speedDir = t.Vel.Scale(1.0 / speedEq)
	} else {
		speedDir = t.Direction
	}

	// Rotate Direction toward the target by up to TurnRate·dt, snapping on
	// overshoot (or instantly when TurnRate·dt >= π). `turning` records that we
	// are mid-turn so acceleration can be braked on a bad turn below.
	newDir := t.Direction
	turning := false
	radStep := t.TurnRate * dt
	if !noTurn {
		if radStep >= math.Pi {
			newDir = targetDir
		} else {
			ckX := targetDir.X*t.Direction.X + targetDir.Y*t.Direction.Y
			ckY := targetDir.X*(-t.Direction.Y) + targetDir.Y*t.Direction.X
			if math.Abs(ckY) < 0.01 && ckX > 0 {
				newDir = targetDir
			} else {
				step := radStep
				if ckY < 0 {
					step = -radStep
				}
				cs := math.Cos(step)
				sn := math.Sin(step)
				newDir = domain.Vec2{
					X: cs*t.Direction.X - sn*t.Direction.Y,
					Y: sn*t.Direction.X + cs*t.Direction.Y,
				}
				newCkY := targetDir.X*(-newDir.Y) + targetDir.Y*newDir.X
				if (ckY > 0) != (newCkY > 0) {
					newDir = targetDir
				} else {
					turning = true
				}
			}
		}
	}

	// Acceleration along newDir, braked on a bad turn so the torpedo does not
	// power through the corner (SP brake on dot < 0).
	accel := t.Accel * dt
	if turning {
		if dot := newDir.X*targetDir.X + newDir.Y*targetDir.Y; dot < 0 {
			accel = (torpedoFrictionK + t.Accel*0.1) * dt
		}
	}
	acc := newDir.Scale(accel)

	// Strafe compensation cancels the velocity component perpendicular to the
	// target line, up to torpedoStrafeK*Accel*dt magnitude.
	addStrafe := domain.Vec2{}
	if strafeMax := torpedoStrafeK * t.Accel * dt; strafeMax > 0 {
		perp := domain.Vec2{X: -targetDir.Y, Y: targetDir.X}
		side := (t.Vel.X+acc.X)*perp.X + (t.Vel.Y+acc.Y)*perp.Y
		if math.Abs(side) > 0.001 {
			mag := math.Abs(side)
			if mag > strafeMax {
				mag = strafeMax
			}
			sign := 1.0
			if side > 0 {
				sign = -1.0
			}
			addStrafe = perp.Scale(sign * mag)
		}
	}

	// Proportional friction against current velocity.
	rub := speedDir.Scale(-torpedoFrictionK * speedEq * dt)

	newVel := t.Vel.Add(acc).Add(rub).Add(addStrafe)
	if mag := newVel.Length(); mag > t.Speed {
		newVel = newVel.Scale(t.Speed / mag)
	}

	oldPos := t.Pos
	newPos := t.Pos.Add(newVel.Scale(dt))
	t.Pos = newPos
	t.Vel = newVel
	t.Direction = newDir

	if targetAlive {
		// Detonate when the swept segment oldPos→newPos passes within HitRadius
		// of the target (a pure endpoint check would miss a fast torpedo that
		// flies through the target between integration steps).
		if pointSegmentDistance(targetPos, oldPos, newPos) <= t.HitRadius {
			return TorpedoHit
		}
	}
	return TorpedoKeep
}

// ApplyDamageInRadius deals damage to every alive Damageable — ships and
// destructible statics alike — whose position lies within radius of center, and
// returns the refs of every target it actually hit. This is the torpedo splash
// primitive (ЧТЗ doc-1 §3 FR-007), the only area-of-effect path in the project;
// every other weapon (laser/missile/drone) is single-target.
//
// The blast is INDISCRIMINATE: targets are never filtered by owner, ally, or
// race, so the firing player's own ships — including the launching ship itself —
// take damage if they sit in range (friendly-fire, ЧТЗ R-02 closed by the
// owner). Damage only lowers HP: a target that crosses to HP<=0 is left for the
// sector's own kill sweep, exactly as a laser hit does.
//
// Attribution mirrors the missile path: every ship hit has LastAttacker set to
// attacker so a kill it causes pays out bounties / war reputation / police
// standing. Statics carry no player attribution (matching killStatic).
//
// Only objects inside the radius are visited, via a squared-distance test — the
// same in-radius spatial filter missilesInRadius / staticRefsInRadius use — so a
// blast never pushes the whole sector through the damage pipeline (NFR-004).
// A non-positive damage or radius is a no-op.
func ApplyDamageInRadius(
	ships map[domain.ShipID]*domain.Ship,
	statics map[domain.EntityRef]*domain.DestructibleStatic,
	center domain.Vec2,
	radius float64,
	damage int,
	attacker domain.PlayerID,
) []domain.EntityRef {
	if damage <= 0 || radius <= 0 {
		return nil
	}
	r2 := radius * radius
	var hits []domain.EntityRef
	for id, ship := range ships {
		if ship.HP <= 0 || !inRadius2(ship.Pos, center, r2) {
			continue
		}
		ApplyDamage(ship, damage)
		ship.LastAttacker = attacker
		hits = append(hits, domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(id)})
	}
	for ref, d := range statics {
		if d.HP <= 0 || !inRadius2(d.Pos, center, r2) {
			continue
		}
		ApplyDamage(d, damage)
		hits = append(hits, ref)
	}
	return hits
}

// inRadius2 reports whether p lies within the circle of squared radius r2 about
// center. Squared distance avoids a sqrt per candidate — the same test the AOI
// filters use.
func inRadius2(p, center domain.Vec2, r2 float64) bool {
	dx := p.X - center.X
	dy := p.Y - center.Y
	return dx*dx+dy*dy <= r2
}
