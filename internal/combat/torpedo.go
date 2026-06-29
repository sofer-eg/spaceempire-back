package combat

import (
	"math"
	"time"
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
