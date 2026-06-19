package dto

import "spaceempire/back/internal/domain"

type Ship struct {
	ID       int64 `json:"id"`
	PlayerID int64 `json:"playerID"`
	// Name is the ship's display name (phase 10.10). Empty for NPC/legacy
	// ships — the SPA's shipDisplayName then falls back to the race name (NPC)
	// or SHIP-<id>. Race carries the ship's faction so the client can build
	// that fallback and tint the contact (phases 10.6/10.7).
	Name     string  `json:"name,omitempty"`
	Race     int     `json:"race,omitempty"`
	SectorID int64   `json:"sectorID"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	// Vx/Vy is the instantaneous velocity; the SPA extrapolates between
	// snapshots so the ship visually drifts under inertia.
	Vx float64 `json:"vx"`
	Vy float64 `json:"vy"`
	// DirectionX/Y is the ship's nose unit vector (mirrors the SP's
	// direction_x/direction_y columns). The canvas rotates the triangle
	// using atan2(DirectionY, DirectionX); useWorldState tracks
	// prev{DirectionX,DirectionY} for shortest-arc interpolation.
	DirectionX float64 `json:"directionX"`
	DirectionY float64 `json:"directionY"`
	// MaxSpeed/Acceleration/TurnRate are class characteristics carried
	// to the client so the PilotPanel can display them and the SPA can
	// run identical client-side prediction if/when added.
	MaxSpeed     float64 `json:"maxSpeed"`
	Acceleration float64 `json:"acceleration"`
	TurnRate     float64 `json:"turnRate"`
	// HP / Shield / Energy are the ship's current pools. MaxHP /
	// MaxShield / MaxEnergy travel alongside because the client needs
	// them for HUD bars; they only change when the ship class changes
	// (not yet implemented), so the over-the-wire cost is one int per
	// pool per patch — acceptable.
	HP          int        `json:"hp"`
	MaxHP       int        `json:"maxHP"`
	Shield      int        `json:"shield"`
	MaxShield   int        `json:"maxShield"`
	Energy      int        `json:"energy"`
	MaxEnergy   int        `json:"maxEnergy"`
	TargetX     *float64   `json:"targetX,omitempty"`
	TargetY     *float64   `json:"targetY,omitempty"`
	FinalTarget *Course    `json:"finalTarget,omitempty"`
	Docked      *EntityRef `json:"docked,omitempty"`
	// CurrentTargetRef, when set, marks the entity the player is "flying
	// to" so the SPA paints a persistent highlight (separate from hover).
	// Cleared on dock/undock or plain-arrival; preserved through approach
	// parking. See domain.Ship for the full lifecycle.
	CurrentTargetRef *EntityRef `json:"currentTargetRef,omitempty"`
	// AttackTarget, when set, marks the entity the laser tick is shooting
	// at. Phase 4.2 only emits EntityKindShip targets here. Cleared on
	// cease-fire, target death, or sector handoff.
	AttackTarget *EntityRef `json:"attackTarget,omitempty"`
	// MiningTarget, when set, is the id of the asteroid the ship is sustained-
	// mining (phase 10.3.6/10.3.21). A bare asteroid id (asteroids are not an
	// EntityKind). The SPA reads it on its own active ship to collapse the
	// «Бурить»/«Прекратить добычу» affordance into one toggle, mirroring how
	// AttackTarget flips «Атаковать»/«Прекратить огонь».
	MiningTarget *int64 `json:"miningTarget,omitempty"`
	// IsSpacesuit marks the weak pilot suit a player flies after their ship is
	// destroyed (phase 10.1) so the SPA can show a "СКАФАНДР" indicator.
	IsSpacesuit bool `json:"isSpacesuit,omitempty"`
	// IsOpen marks a ship other players may board as a passenger (phase 10.23).
	// The SPA shows the access toggle (own ship) and a "сесть пассажиром" vs
	// "вход закрыт" hangar affordance (other players' ships).
	IsOpen bool `json:"isOpen,omitempty"`
	// IsNPC marks ships owned by the system NPC player (traders, miners,
	// passengers). The SPA colours them amber instead of red (enemy player).
	// Set by the WS/state handler via MarkNPC; not derived from domain.Ship
	// directly to avoid threading npcPlayerID through the DTO package.
	IsNPC bool `json:"isNPC,omitempty"`
	// HullCategory is the hull-shape category (M1/M2/M3/M4/M5/M6/TL/XX/TS)
	// resolved from the ship's class via the HullCategoryResolver injected by
	// the api layer (phase 10.13). The SPA maps it to a per-class silhouette on
	// the sector map. Empty when the class is unknown (spacesuit / legacy ship
	// / nil resolver) — the client then falls back to its maxSpeed heuristic.
	HullCategory string `json:"hullCategory,omitempty"`
	// ShipClassID is the ct_ship_classes blueprint id (phase 10.14). The
	// shipyard outfit screen uses it to look up the ship's class number and
	// filter the equipment catalog. 0 = spacesuit / legacy ship.
	ShipClassID int64 `json:"shipClassID,omitempty"`
	// RadarRange is the ship's personal small-radar radius in world units
	// (phase 10.20). The SPA draws it as a ring around the player's own ship.
	// 0 = legacy/spacesuit (the server used the cfg.AOIRadius fallback).
	RadarRange float64 `json:"radarRange,omitempty"`
	// IsHidden marks a cloaked ship (phase 10.20 L4, up_hide). Only ever set on
	// ships the subscriber can see (own ship, close-detected, or allied) — a
	// cloaked enemy is filtered out entirely. The SPA shows a stealth indicator
	// for the player's own cloaked ship.
	IsHidden bool `json:"isHidden,omitempty"`
	// Equipment is the list of installed ct_updates modules (phase 10.14).
	// Omitted when empty (NPC/legacy ships), so it only adds bytes for outfitted
	// player ships. The outfit screen reads it to show the current fit.
	Equipment []InstalledEquipment `json:"equipment,omitempty"`
}

// InstalledEquipment mirrors domain.InstalledEquipment on the wire (phase
// 10.14). EquipmentID pins the catalog row; Type is the module key
// (up_engine/…); Level is the install level.
type InstalledEquipment struct {
	EquipmentID int64  `json:"equipmentID"`
	Type        string `json:"type"`
	Level       int    `json:"level"`
}

// HullCategoryResolver maps a ship's class id to its hull-shape category
// ("M1".."TS"), or "" when the class is unknown. It lets the api layer inject
// the balance ship-class catalog without the dto package importing balance.
// A nil resolver yields an empty HullCategory.
type HullCategoryResolver func(domain.ShipClassID) string

// Course mirrors domain.Course on the wire. Used by SetCourseRequest and
// embedded in Ship snapshots for the SPA's autopilot UI. Approach, when
// set, asks the autopilot to park at DockRange/2 from the referenced
// static and wait for an explicit DockCommand — there is no automatic
// docking anymore.
type Course struct {
	SectorID int64      `json:"sectorID"`
	X        float64    `json:"x"`
	Y        float64    `json:"y"`
	Approach *EntityRef `json:"approach,omitempty"`
}

// EntityRef mirrors domain.EntityRef on the wire. Kind values match the
// EntityKind constants (1=ship, 2=station, 3=shipyard, 4=trade_station,
// 5=pirbase).
type EntityRef struct {
	Kind int   `json:"kind"`
	ID   int64 `json:"id"`
}

func ShipFromDomain(s domain.Ship, hull HullCategoryResolver) Ship {
	out := Ship{
		ID:           int64(s.ID),
		PlayerID:     int64(s.PlayerID),
		Name:         s.Name,
		Race:         int(s.Race),
		SectorID:     int64(s.SectorID),
		X:            s.Pos.X,
		Y:            s.Pos.Y,
		Vx:           s.Vel.X,
		Vy:           s.Vel.Y,
		DirectionX:   s.Direction.X,
		DirectionY:   s.Direction.Y,
		MaxSpeed:     s.MaxSpeed,
		Acceleration: s.Acceleration,
		TurnRate:     s.TurnRate,
		HP:           s.HP,
		MaxHP:        s.MaxHP,
		Shield:       s.Shield,
		MaxShield:    s.MaxShield,
		Energy:       s.Energy,
		MaxEnergy:    s.MaxEnergy,
		IsSpacesuit:  s.IsSpacesuit,
		IsOpen:       s.IsOpen,
		ShipClassID:  int64(s.ShipClassID),
		RadarRange:   s.RadarRange,
		IsHidden:     s.IsHidden,
	}
	if hull != nil {
		out.HullCategory = hull(s.ShipClassID)
	}
	if len(s.Equipment) > 0 {
		out.Equipment = make([]InstalledEquipment, len(s.Equipment))
		for i, m := range s.Equipment {
			out.Equipment[i] = InstalledEquipment{
				EquipmentID: int64(m.EquipmentID),
				Type:        m.Type,
				Level:       m.Level,
			}
		}
	}
	if s.Target != nil {
		x, y := s.Target.X, s.Target.Y
		out.TargetX, out.TargetY = &x, &y
	}
	if s.FinalTarget != nil {
		course := &Course{
			SectorID: int64(s.FinalTarget.Sector),
			X:        s.FinalTarget.Pos.X,
			Y:        s.FinalTarget.Pos.Y,
		}
		if s.FinalTarget.Approach != nil {
			course.Approach = &EntityRef{
				Kind: int(s.FinalTarget.Approach.Kind),
				ID:   s.FinalTarget.Approach.ID,
			}
		}
		out.FinalTarget = course
	}
	if s.Docked != nil {
		out.Docked = &EntityRef{
			Kind: int(s.Docked.Kind),
			ID:   s.Docked.ID,
		}
	}
	if s.CurrentTargetRef != nil {
		out.CurrentTargetRef = &EntityRef{
			Kind: int(s.CurrentTargetRef.Kind),
			ID:   s.CurrentTargetRef.ID,
		}
	}
	if s.AttackTarget != nil {
		out.AttackTarget = &EntityRef{
			Kind: int(s.AttackTarget.Kind),
			ID:   s.AttackTarget.ID,
		}
	}
	if s.MiningTarget != nil {
		id := int64(*s.MiningTarget)
		out.MiningTarget = &id
	}
	return out
}

func ShipsFromDomain(in []domain.Ship, hull HullCategoryResolver) []Ship {
	out := make([]Ship, len(in))
	for i, s := range in {
		out[i] = ShipFromDomain(s, hull)
	}
	return out
}

// MarkNPC sets IsNPC=true on every ship whose PlayerID matches npcID.
// npcID==0 is a no-op (no NPC player loaded; safe for tests).
func MarkNPC(ships []Ship, npcID int64) {
	if npcID == 0 {
		return
	}
	for i := range ships {
		if ships[i].PlayerID == npcID {
			ships[i].IsNPC = true
		}
	}
}
