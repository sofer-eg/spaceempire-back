package sector

import (
	"context"
	"encoding/json"
	"fmt"

	"spaceempire/back/internal/domain"
)

// PoliceScanner is the app-side police capability injected into the worker
// (phase 9.4). The worker decides which ships scan and whom (a police-race
// ship near a controller-less real player); the scanner does the cargo /
// standing work, keeping the sector package free of cargo/standing deps —
// the same split as TraderLogistics / MinerLogistics. Wired via WithPolice;
// nil disables police entirely. The real implementation lives in app/.
type PoliceScanner interface {
	// Scan inspects a player ship's hold for goods illegal to policeRace. On
	// contraband it confiscates it (immediate-persist) and drops the player's
	// standing, returning Confiscated=true. A clean hold (or an NPC/ineligible
	// target) returns the zero result.
	Scan(ctx context.Context, policeRace domain.RaceID, target domain.ShipID, targetPlayer domain.PlayerID) (PoliceScanResult, error)
	// OnRaceShipKilled drops a player's standing with race when they destroy
	// one of its navy ships. NPC killers are ignored by the implementation.
	OnRaceShipKilled(ctx context.Context, killer domain.PlayerID, race domain.RaceID) error
}

// PoliceScanResult reports what a scan found, for the worker to broadcast.
type PoliceScanResult struct {
	Confiscated bool
	GoodsType   domain.GoodsTypeID
	Quantity    int64
	// Wanted is the player's wanted status with the police race after the
	// confiscation (drives the "wanted" badge in the journal event).
	Wanted bool
}

// PoliceConfig tunes the scan step. Zero fields fall back to defaults.
type PoliceConfig struct {
	// Races whose navy acts as police (the main races 1-5). A ship of one of
	// these races scans nearby players. Empty disables scanning.
	Races []domain.RaceID
	// ScanRange is the radius (world units) within which a police ship
	// inspects a player ship. Default 100.
	ScanRange float64
	// CooldownTicks is how many ticks a player ship is exempt from re-scan
	// after any scan (clean or not), so police do not re-read the same hold
	// every tick. Default 10.
	CooldownTicks uint64
}

func (c PoliceConfig) withDefaults() PoliceConfig {
	if c.ScanRange <= 0 {
		c.ScanRange = 100
	}
	if c.CooldownTicks == 0 {
		c.CooldownTicks = 10
	}
	return c
}

// PoliceScanTopic is the per-player bus topic a contraband confiscation is
// published to (phase 9.4). The WS handler subscribes to its own player's
// topic, mirroring rent.OverdueTopic.
func PoliceScanTopic(player domain.PlayerID) string {
	return fmt.Sprintf("police.scan.%d", int64(player))
}

// PoliceScanEvent is broadcast to a player when a race's police confiscate
// contraband from their ship. Race lets the SPA name the faction; Wanted
// drives the "WANTED" flag in the journal line.
type PoliceScanEvent struct {
	PlayerID  domain.PlayerID    `json:"playerId"`
	Race      domain.RaceID      `json:"race"`
	SectorID  domain.SectorID    `json:"sectorId"`
	GoodsType domain.GoodsTypeID `json:"goodsType"`
	Quantity  int64              `json:"quantity"`
	Wanted    bool               `json:"wanted"`
}

// tickPoliceScan is the per-tick police inspection step (phase 9.4). For each
// police-race ship it scans every real-player ship in range that is off
// cooldown: contraband is confiscated and the player's standing drops (via the
// injected scanner), and a per-player event is published. Real players are
// distinguished from NPCs by having no AI controller. Runs after fireLasers,
// inside the single tick goroutine, so the scanner's DB work blocks the tick
// like production/Transfer.
func (w *Worker) tickPoliceScan(ctx context.Context, s *sectorState) {
	if w.police == nil || len(w.policeRaces) == 0 {
		return
	}
	rangeSq := w.policeCfg.ScanRange * w.policeCfg.ScanRange
	for _, police := range s.ships {
		if police.HP <= 0 || !w.policeRaces[police.Race] {
			continue
		}
		for targetID, target := range s.ships {
			if targetID == police.ID || target.HP <= 0 {
				continue
			}
			// Only real players are scanned: every NPC has an AI controller,
			// players have none.
			if _, isNPC := s.controllers[targetID]; isNPC {
				continue
			}
			if until, ok := s.policeScanCooldown[targetID]; ok && s.tick < until {
				continue
			}
			d := target.Pos.Sub(police.Pos)
			if d.X*d.X+d.Y*d.Y > rangeSq {
				continue
			}
			res, err := w.police.Scan(ctx, police.Race, targetID, target.PlayerID)
			if err != nil {
				w.logger.ErrorContext(ctx, "police scan failed",
					"err", err, "police", int64(police.ID), "target", int64(targetID),
					"race", int(police.Race), "sector", int64(s.sectorID))
				continue
			}
			s.policeScanCooldown[targetID] = s.tick + w.policeCfg.CooldownTicks
			if res.Confiscated {
				w.publishPoliceScan(ctx, s, target.PlayerID, police.Race, res)
			}
		}
	}
}

// publishPoliceScan emits the per-player confiscation event on the bus. Best-
// effort: a nil bus (pure unit tests) or a publish error is logged, never
// blocking the tick.
func (w *Worker) publishPoliceScan(ctx context.Context, s *sectorState, player domain.PlayerID, race domain.RaceID, res PoliceScanResult) {
	if w.bus == nil {
		return
	}
	payload, err := json.Marshal(PoliceScanEvent{
		PlayerID:  player,
		Race:      race,
		SectorID:  s.sectorID,
		GoodsType: res.GoodsType,
		Quantity:  res.Quantity,
		Wanted:    res.Wanted,
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "police: marshal scan event", "err", err, "player", int64(player))
		return
	}
	if err := w.bus.Publish(ctx, PoliceScanTopic(player), payload); err != nil {
		w.logger.ErrorContext(ctx, "police: publish scan event", "err", err, "player", int64(player))
	}
}

// WithPolice wires the police inspection step (phase 9.4): contraband scanning
// and the standing penalty for destroying faction ships. cfg.Races are the
// races whose navy acts as police. Nil scanner or empty races disables it.
func WithPolice(scanner PoliceScanner, cfg PoliceConfig) Option {
	return func(w *Worker) {
		w.police = scanner
		w.policeCfg = cfg.withDefaults()
		w.policeRaces = make(map[domain.RaceID]bool, len(cfg.Races))
		for _, r := range cfg.Races {
			w.policeRaces[r] = true
		}
	}
}
