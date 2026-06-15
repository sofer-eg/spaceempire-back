package sector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
)

// ErrInvalidGate is returned when JumpCommand references a gate that does
// not exist or does not touch the ship's current sector.
var ErrInvalidGate = errors.New("sector: invalid gate")

// ErrGateOutOfRange is returned when the ship is further than Config.GateRange
// from the gate's exit position on the source sector side.
var ErrGateOutOfRange = errors.New("sector: ship out of gate range")

// ErrHandoffUnavailable is returned when JumpCommand fires on a worker that
// has no topology / bus wired in (typical for unit tests that don't exercise
// handoff).
var ErrHandoffUnavailable = errors.New("sector: handoff not configured")

// JumpEvent is the over-the-bus payload that hands a ship from worker A to
// worker B. It is fully self-contained: B reconstructs the in-RAM ship
// from this alone, the BD row having already been updated by A.
//
// ControllerKind/ControllerState carry the ship's AI controller across the
// handoff (phase 5.3 — the AI-controller handoff deferred in 5.1). When
// ControllerKind is non-empty the intake worker rebuilds the controller via
// its registry so an NPC keeps thinking — and keeps its route phase — after
// a gate jump. Empty for player ships (no controller).
type JumpEvent struct {
	Ship            domain.Ship     `json:"ship"`
	SourceSector    domain.SectorID `json:"source_sector"`
	TargetSector    domain.SectorID `json:"target_sector"`
	ExitPos         domain.Vec2     `json:"exit_pos"`
	ControllerKind  string          `json:"controller_kind,omitempty"`
	ControllerState []byte          `json:"controller_state,omitempty"`
}

// PlayerHandoffEvent is published on PlayerHandoffTopic so cross-cutting
// subscribers (currently the WS handler) can react to a player crossing
// sector borders without watching every worker's intake topic. Emitted by
// executeJump after the JumpEvent is on the bus and the ship is gone from
// the source RAM.
type PlayerHandoffEvent struct {
	PlayerID     domain.PlayerID `json:"player_id"`
	ShipID       domain.ShipID   `json:"ship_id"`
	SourceSector domain.SectorID `json:"source_sector"`
	TargetSector domain.SectorID `json:"target_sector"`
}

// PlayerHandoffTopic is the bus topic on which PlayerHandoffEvent for a
// given player is broadcast. WS subscribes per-connection and uses it to
// re-bind the patch stream to the new sector after a gate jump.
func PlayerHandoffTopic(playerID domain.PlayerID) string {
	return fmt.Sprintf("player.%d.handoff", int64(playerID))
}

// JumpCommand is the player- (or, in future, autodocker-) issued request to
// hop a ship through a gate. The worker that owns the ship's current sector
// validates ownership, gate identity, and proximity, then persists the
// authority transfer to DB and broadcasts the JumpEvent.
type JumpCommand struct {
	PlayerID domain.PlayerID
	ShipID   domain.ShipID
	GateID   domain.GateID
	Reply    chan<- CmdResult
}

func (c JumpCommand) apply(w *Worker, s *sectorState) {
	var res CmdResult

	ship, ok := s.ships[c.ShipID]
	switch {
	case !ok:
		res.Err = ErrShipNotFound
	case ship.PlayerID != c.PlayerID:
		res.Err = ErrForbidden
	case w.topology == nil || w.bus == nil:
		res.Err = ErrHandoffUnavailable
	}
	if res.Err != nil {
		replyOnce(c.Reply, res)
		return
	}

	gate := w.gateByID(c.GateID)
	if gate == nil {
		res.Err = ErrInvalidGate
		replyOnce(c.Reply, res)
		return
	}
	sourcePos, targetSector, exitPos, ok := gateSides(gate, s.sectorID)
	if !ok {
		res.Err = ErrInvalidGate
		replyOnce(c.Reply, res)
		return
	}
	if ship.Pos.Sub(sourcePos).Length() > w.cfg.GateRange {
		res.Err = ErrGateOutOfRange
		replyOnce(c.Reply, res)
		return
	}

	res.Err = executeJump(w, s, ship, targetSector, exitPos)
	replyOnce(c.Reply, res)
}

// executeJump performs the persistence + bus + RAM-eviction steps shared by
// the player-issued JumpCommand and the tick-driven auto-jump. The caller
// must have already validated ownership, gate identity, and proximity.
// Returns nil on success; on any error the ship stays in the source sector.
func executeJump(w *Worker, s *sectorState, ship *domain.Ship, targetSector domain.SectorID, exitPos domain.Vec2) error {
	relocated := *ship
	relocated.SectorID = targetSector
	relocated.Pos = exitPos
	relocated.Vel = domain.Vec2{}
	relocated.Target = nil
	relocated.FinalTarget = cloneCourse(ship.FinalTarget)
	relocated.Docked = cloneEntityRef(ship.Docked)
	relocated.CurrentTargetRef = cloneEntityRef(ship.CurrentTargetRef)
	// AttackTarget is cross-sector noise: the target lived in the source
	// sector and is no longer reachable. Phase 4.2 only supports
	// same-sector combat, so we drop the reference on every gate jump.
	relocated.AttackTarget = nil
	// MiningTarget points at an asteroid in the source sector (phase 10.3.6),
	// unreachable after the jump — drop it so the relocated ship is not stuck
	// holding station against a missing target.
	relocated.MiningTarget = nil

	// Carry the AI controller across the handoff (phase 5.3): marshal its
	// state into the JumpEvent and re-home its ai_state row to the target
	// sector so a cold-start after the jump rebuilds it in the right place.
	// On a marshal error we log and hand off without the controller — the
	// destination rebuilds it from zero rather than aborting the jump.
	var ctrlKind string
	var ctrlState []byte
	if ctrl, ok := s.controllers[ship.ID]; ok {
		ctrlKind = ctrl.Kind()
		data, err := ctrl.MarshalState()
		if err != nil {
			w.logger.Error("marshal ai controller on jump",
				"err", err, "ship", int64(ship.ID), "kind", ctrlKind)
		} else {
			ctrlState = data
		}
	}

	if w.repo != nil {
		if err := w.repo.Save(context.Background(), relocated); err != nil {
			return fmt.Errorf("save ship: %w", err)
		}
	}

	if ctrlKind != "" && w.aiStateRepo != nil {
		if err := w.aiStateRepo.BatchUpsert(context.Background(), []domain.AIState{{
			ShipID:         ship.ID,
			SectorID:       targetSector,
			ControllerKind: ctrlKind,
			StateJSON:      ctrlState,
		}}); err != nil {
			// Non-fatal: the destination still rebuilds from the event. A
			// crash before the destination's next snapshot would leave the
			// row pointing at the source sector, which cold-start prunes.
			w.logger.Error("re-home ai state on jump",
				"err", err, "ship", int64(ship.ID), "target", int64(targetSector))
		}
	}

	payload, err := json.Marshal(JumpEvent{
		Ship:            relocated,
		SourceSector:    s.sectorID,
		TargetSector:    targetSector,
		ExitPos:         exitPos,
		ControllerKind:  ctrlKind,
		ControllerState: ctrlState,
	})
	if err != nil {
		return fmt.Errorf("marshal jump event: %w", err)
	}
	if err := w.bus.Publish(context.Background(), IntakeTopic(targetSector), payload); err != nil {
		return fmt.Errorf("publish jump event: %w", err)
	}

	// PlayerHandoffEvent lets the WS handler re-bind its patch subscription
	// to the new sector without polling every worker. NPC handoffs (player
	// id 0) skip it — no client listens. The intake hop above is the
	// authoritative side of the handoff: if PlayerHandoffEvent fails to
	// publish, the ship still moves correctly — the client just has to
	// reconnect to discover the new sector. So we log and continue rather
	// than rolling the jump back.
	if ship.PlayerID != 0 {
		handoffPayload, err := json.Marshal(PlayerHandoffEvent{
			PlayerID:     ship.PlayerID,
			ShipID:       ship.ID,
			SourceSector: s.sectorID,
			TargetSector: targetSector,
		})
		if err != nil {
			w.logger.Warn("marshal player handoff event",
				"err", err, "player", int64(ship.PlayerID), "ship", int64(ship.ID))
		} else if err := w.bus.Publish(context.Background(), PlayerHandoffTopic(ship.PlayerID), handoffPayload); err != nil {
			w.logger.Warn("publish player handoff event",
				"err", err, "player", int64(ship.PlayerID), "ship", int64(ship.ID))
		}
	}

	// Phase 10.23: passengers ride along — their WS must follow the host into
	// the new sector too. The host already carries them in B (the JumpEvent's
	// ship copy keeps PassengerPlayers), so we only fan the WS handoff out here.
	for _, pid := range ship.PassengerPlayers {
		pp, err := json.Marshal(PlayerHandoffEvent{
			PlayerID: pid, ShipID: ship.ID, SourceSector: s.sectorID, TargetSector: targetSector,
		})
		if err != nil {
			w.logger.Warn("marshal passenger handoff", "err", err, "player", int64(pid), "host", int64(ship.ID))
			continue
		}
		if err := w.bus.Publish(context.Background(), PlayerHandoffTopic(pid), pp); err != nil {
			w.logger.Warn("publish passenger handoff", "err", err, "player", int64(pid), "host", int64(ship.ID))
		}
	}

	delete(s.ships, ship.ID)
	delete(s.dirty, ship.ID)
	delete(s.controllers, ship.ID)
	s.recordOutbound(targetSector)
	// Count on the outbound (authoritative) side only — the matching inbound
	// on the target worker is the same handoff.
	w.metrics.IncHandoff(s.sectorID, targetSector)
	return nil
}

// JumpIntakeCommand is the worker-internal command synthesised from an
// inbound JumpEvent. It registers the ship in the target sector's state;
// the DB row is already current courtesy of the source worker's Save.
type JumpIntakeCommand struct {
	Event JumpEvent
}

func (c JumpIntakeCommand) apply(w *Worker, s *sectorState) {
	if _, exists := s.ships[c.Event.Ship.ID]; exists {
		// Duplicate intake — A failed to remove from its RAM after the
		// publish landed, then republished after restart. Trust the latest
		// event (positions in B may have changed if anyone collided with
		// the stale copy, but the source-of-truth is the inbound event).
		ship := c.Event.Ship
		s.ships[ship.ID] = &ship
		c.attachController(w, s, ship.ID)
		return
	}
	ship := c.Event.Ship
	if ship.Target != nil {
		t := *ship.Target
		ship.Target = &t
	}
	s.ships[ship.ID] = &ship
	c.attachController(w, s, ship.ID)
	s.recordInbound(c.Event.SourceSector)
}

// attachController rebuilds the inbound ship's AI controller from the
// JumpEvent (phase 5.3 AI-controller handoff). A no-op when the event
// carries no controller (player ships) or no registry is wired. A build
// failure (unknown kind / bad state) is logged and skipped — the ship
// still arrives, just without AI, mirroring buildControllers at cold-start.
func (c JumpIntakeCommand) attachController(w *Worker, s *sectorState, shipID domain.ShipID) {
	if c.Event.ControllerKind == "" || w.aiRegistry == nil {
		return
	}
	ctrl, err := w.aiRegistry.Build(c.Event.ControllerKind, c.Event.ControllerState)
	if err != nil {
		w.logger.Error("rebuild ai controller on intake",
			"err", err, "ship", int64(shipID),
			"kind", c.Event.ControllerKind, "sector", int64(s.sectorID))
		return
	}
	s.controllers[shipID] = ctrl
}

// IntakeTopic is the bus topic on which a sector receives JumpEvents.
// Workers subscribe to one per owned sector during Run.
func IntakeTopic(sectorID domain.SectorID) string {
	return fmt.Sprintf("sector.%d.intake", int64(sectorID))
}

func (w *Worker) gateByID(id domain.GateID) *domain.Gate {
	for i := range w.topology.Gates() {
		g := &w.topology.Gates()[i]
		if g.ID == id {
			return g
		}
	}
	return nil
}

// gateSides returns the (source-side exit pos, target sector, target-side
// exit pos) for a gate when traversed starting from sourceSector. Returns
// ok=false when sourceSector is not one of the gate's endpoints.
func gateSides(g *domain.Gate, sourceSector domain.SectorID) (sourcePos domain.Vec2, targetSector domain.SectorID, targetPos domain.Vec2, ok bool) {
	switch sourceSector {
	case g.SectorA:
		return g.PosA, g.SectorB, g.PosB, true
	case g.SectorB:
		return g.PosB, g.SectorA, g.PosA, true
	default:
		return domain.Vec2{}, 0, domain.Vec2{}, false
	}
}
