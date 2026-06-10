package app

import (
	"context"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/reference/contraband"
	raceref "spaceempire/back/internal/reference/race"
	"spaceempire/back/internal/sector"
)

// policeRaces are the races (1-5) whose navy acts as police and whose standing
// drives the wanted overlay. Pirates(6)/Xenon(7)/Kha'ak(8) use only the
// default race matrix (no police / no per-player standing).
func policeRaces() []domain.RaceID { return []domain.RaceID{1, 2, 3, 4, 5} }

func isMainRace(r domain.RaceID) bool { return r >= 1 && r <= 5 }

// PoliceScanConfig tunes the standing penalties (phase 9.4). Zero fields fall
// back to defaults.
type PoliceScanConfig struct {
	// ContrabandPenalty is the standing drop per contraband bust. Default 5.
	ContrabandPenalty int
	// KillPenalty is the standing drop for destroying a faction navy ship.
	// Default 10.
	KillPenalty int
}

func (c PoliceScanConfig) withDefaults() PoliceScanConfig {
	if c.ContrabandPenalty <= 0 {
		c.ContrabandPenalty = 5
	}
	if c.KillPenalty <= 0 {
		c.KillPenalty = 10
	}
	return c
}

// cargoInspector is the slice of cargo.Service the scanner needs (ISP):
// read a hold and atomically remove a stack. *cargo.Service satisfies it.
type cargoInspector interface {
	Inventory(ctx context.Context, owner domain.EntityRef, viewer domain.PlayerID) (domain.Inventory, error)
	Consume(ctx context.Context, owner domain.EntityRef, gtype domain.GoodsTypeID, qty int64) error
}

// standingAdjuster is the slice of racestanding.Service the scanner needs.
// *racestanding.Service satisfies it.
type standingAdjuster interface {
	Adjust(ctx context.Context, player domain.PlayerID, race domain.RaceID, delta int) (int, error)
	IsWanted(player domain.PlayerID, race domain.RaceID) bool
}

// policeScanner implements sector.PoliceScanner over cargo + the standing
// service + the contraband reference (phase 9.4). Confiscation is atomic
// (cargo.Consume runs in a tx); the standing adjustment is a separate atomic
// write.
type policeScanner struct {
	cargo    cargoInspector
	standing standingAdjuster
	npc      domain.PlayerID
	cfg      PoliceScanConfig
}

// Scan inspects a player's hold for goods illegal to policeRace, confiscates
// all illegal stacks, and drops the player's standing once per bust.
func (p policeScanner) Scan(ctx context.Context, policeRace domain.RaceID, target domain.ShipID, targetPlayer domain.PlayerID) (sector.PoliceScanResult, error) {
	// Only real players carry standing — skip the NPC owner / zero player.
	if targetPlayer == 0 || targetPlayer == p.npc {
		return sector.PoliceScanResult{}, nil
	}
	ref := domain.EntityRef{Kind: domain.EntityKindShip, ID: int64(target)}
	// Ship holds are unowned (goods_owner_id = 0); viewer 0 returns the whole
	// hold (phase 10.22).
	inv, err := p.cargo.Inventory(ctx, ref, 0)
	if err != nil {
		return sector.PoliceScanResult{}, err
	}
	var total int64
	var last domain.GoodsTypeID
	for _, item := range inv.Items {
		if item.Quantity <= 0 || !contraband.IsIllegal(policeRace, item.GoodsType) {
			continue
		}
		if err := p.cargo.Consume(ctx, ref, item.GoodsType, item.Quantity); err != nil {
			return sector.PoliceScanResult{}, err
		}
		total += item.Quantity
		last = item.GoodsType
	}
	if total == 0 {
		return sector.PoliceScanResult{}, nil
	}
	if _, err := p.standing.Adjust(ctx, targetPlayer, policeRace, -p.cfg.ContrabandPenalty); err != nil {
		return sector.PoliceScanResult{}, err
	}
	return sector.PoliceScanResult{
		Confiscated: true,
		GoodsType:   last,
		Quantity:    total,
		Wanted:      p.standing.IsWanted(targetPlayer, policeRace),
	}, nil
}

// OnRaceShipKilled drops the killer's standing with race when they destroy one
// of its navy ships. NPC killers are ignored.
func (p policeScanner) OnRaceShipKilled(ctx context.Context, killer domain.PlayerID, race domain.RaceID) error {
	if killer == 0 || killer == p.npc {
		return nil
	}
	_, err := p.standing.Adjust(ctx, killer, race, -p.cfg.KillPenalty)
	return err
}

// wantedOracle is the slice of racestanding.Service the targeter overlay needs.
type wantedOracle interface {
	IsWanted(player domain.PlayerID, race domain.RaceID) bool
}

// wantedOverlayTargeter wraps the 9.1 race-matrix targeter so a main-race ship
// (1-5) also attacks a real player who is "wanted" with that race (9.4). The
// default race matrix and player↔player/NPC↔NPC hostility are unchanged.
type wantedOverlayTargeter struct {
	base     raceMatrixTargeter
	standing wantedOracle
	npc      domain.PlayerID
}

func (t wantedOverlayTargeter) IsHostile(self, other domain.Ship) bool {
	if t.base.IsHostile(self, other) {
		return true
	}
	// Overlay: a main-race ship engages a wanted real player (race 0, not NPC).
	if isMainRace(self.Race) && other.Race == raceref.Neutral &&
		other.PlayerID != 0 && other.PlayerID != t.npc {
		return t.standing.IsWanted(other.PlayerID, self.Race)
	}
	return false
}
