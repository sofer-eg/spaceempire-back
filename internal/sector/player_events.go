package sector

import (
	"context"
	"encoding/json"

	"spaceempire/back/internal/domain"
)

// PlayerDockedTopic is the fixed bus topic on which a player docking to a
// station or trade-station is announced (TASK-89 pacer trigger). Unlike the
// per-player police/handoff topics this is a single fixed topic — the PlayerID
// travels in the payload — mirroring EntityKilledTopic: the quest pacer
// subscribes once and fans out by payload.
const PlayerDockedTopic = "player.docked"

// PlayerDockedEvent is published when a player-owned ship successfully docks to
// a station or trade-station. SectorID is the ship's sector at dock time; the
// pacer uses it as the player's location for procedural generation.
type PlayerDockedEvent struct {
	PlayerID domain.PlayerID `json:"player_id"`
	SectorID domain.SectorID `json:"sector_id"`
}

// PlayerJumpedTopic is the fixed bus topic on which a player's inter-sector
// jump is announced (TASK-89 pacer trigger). Fixed topic like PlayerDockedTopic
// / EntityKilledTopic; distinct from the per-player PlayerHandoffTopic that WS
// needs for its patch re-bind.
const PlayerJumpedTopic = "player.jumped"

// PlayerJumpedEvent is published when a player-owned ship completes an
// inter-sector jump (gate handoff or jump drive). TargetSector is where the
// ship arrived; the pacer uses it as the player's location.
type PlayerJumpedEvent struct {
	PlayerID     domain.PlayerID `json:"player_id"`
	TargetSector domain.SectorID `json:"target_sector"`
}

// publishPlayerDocked emits PlayerDockedEvent for a player-owned ship that
// docked to a station or trade-station. Best-effort: a nil bus (unit tests) or
// a publish error is logged, never blocking the tick. Shipyard/pirbase docks,
// ship-to-ship docks and NPC ships do not fire (SRS §5 trigger scope).
func (w *Worker) publishPlayerDocked(s *sectorState, ship *domain.Ship, target domain.EntityRef) {
	if w.bus == nil || ship.PlayerID == 0 {
		return
	}
	if target.Kind != domain.EntityKindStation && target.Kind != domain.EntityKindTradeStation {
		return
	}
	ctx := context.Background()
	payload, err := json.Marshal(PlayerDockedEvent{PlayerID: ship.PlayerID, SectorID: s.sectorID})
	if err != nil {
		w.logger.ErrorContext(ctx, "quest: marshal player docked event", "err", err, "player", int64(ship.PlayerID))
		return
	}
	if err := w.bus.Publish(ctx, PlayerDockedTopic, payload); err != nil {
		w.logger.ErrorContext(ctx, "quest: publish player docked event", "err", err, "player", int64(ship.PlayerID))
	}
}

// publishPlayerJumped emits PlayerJumpedEvent for a player-owned ship that
// completed an inter-sector jump. Best-effort, mirroring publishPlayerDocked;
// the caller has already confirmed ship.PlayerID != 0.
func (w *Worker) publishPlayerJumped(ship *domain.Ship, targetSector domain.SectorID) {
	if w.bus == nil {
		return
	}
	ctx := context.Background()
	payload, err := json.Marshal(PlayerJumpedEvent{PlayerID: ship.PlayerID, TargetSector: targetSector})
	if err != nil {
		w.logger.Warn("quest: marshal player jumped event", "err", err, "player", int64(ship.PlayerID))
		return
	}
	if err := w.bus.Publish(ctx, PlayerJumpedTopic, payload); err != nil {
		w.logger.Warn("quest: publish player jumped event", "err", err, "player", int64(ship.PlayerID))
	}
}
