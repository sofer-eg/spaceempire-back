package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"spaceempire/back/internal/api/dto"
	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/economy/rent"
	"spaceempire/back/internal/sector"
)

const wsWriteTimeout = 5 * time.Second

// handoffEventBuffer caps the per-WS queue of inbound PlayerHandoffEvents.
// Realistically one jump per ~3 seconds (tick interval), so 8 is plenty;
// overflow drops the oldest and logs — the WS reconnects on the next jump
// and recovers state from the welcome frame.
const handoffEventBuffer = 8

// rentOverdueBuffer caps the per-WS queue of rent.OverdueEvents. Rent bills
// at most a few times an hour, so a small buffer is ample; overflow drops the
// oldest (the client re-reads GET /api/my/rents on next view).
const rentOverdueBuffer = 4

// rentOverdueFrame is the WS frame pushed to the payer when a rent charge is
// missed (6.4). The SPA consumer is out of phase-6.4 scope (back+db only);
// the server-side emission completes the "WS event rent_overdue" requirement.
type rentOverdueFrame struct {
	Type            string `json:"type"`
	RentID          int64  `json:"rentId"`
	StationKind     int    `json:"stationKind"`
	StationID       int64  `json:"stationId"`
	AmountPerPeriod int64  `json:"amountPerPeriod"`
	UnpaidPeriods   int    `json:"unpaidPeriods"`
	Confiscated     bool   `json:"confiscated"`
}

// policeScanBuffer caps the per-WS queue of police confiscation events (9.4).
// Scans are throttled by a per-target cooldown, so a small buffer is ample;
// overflow drops the oldest and logs.
const policeScanBuffer = 4

// policeScanFrame is the WS frame pushed to a player when a race's police
// confiscate contraband from their ship (9.4). The SPA logs it (and refreshes
// the reputation panel); race lets it name the faction, wanted drives the
// "WANTED" badge.
type policeScanFrame struct {
	Type      string `json:"type"`
	Race      int    `json:"race"`
	SectorID  int64  `json:"sectorId"`
	GoodsType int    `json:"goodsType"`
	Quantity  int64  `json:"quantity"`
	Wanted    bool   `json:"wanted"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// RequireAuth (when wired) has already populated the player ID.
	playerID, ok := auth.PlayerIDFromContext(r.Context())
	if !ok {
		// Phase-0 dev mode: middleware not wired (e.g. unit tests). Subscribe
		// as the zero player so the test harness still receives patches —
		// production app.go always wires RequireAuth.
		playerID = 0
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Dev: SPA on :5173 connects to API on :8080. Tightened in 7.3.
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.logger.Warn("ws accept", "err", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Pick the sector the player's own ship currently lives in. cfg.SectorID
	// is only a fallback for unauthenticated clients (tests) or players that
	// have no ship yet — auth.Spawner ensures every real player has one
	// before the WS opens, but the fallback keeps legacy callers working.
	initialSector := s.initialSubscribeSector(ctx, playerID)

	sub, unsub, err := s.router.Subscribe(ctx, initialSector, playerID)
	if err != nil {
		s.logger.Warn("ws subscribe", "err", err)
		_ = conn.Close(websocket.StatusInternalError, "subscribe failed")
		return
	}
	defer func() { unsub() }()

	// Send the sector's static objects once before the patch stream starts.
	// They never change during the session in phase 3.1, so a single message
	// at subscribe time is enough — keeps the per-tick patches lean.
	s.sendStatics(ctx, conn, initialSector)

	// Reader goroutine: detects client close. We don't process client messages
	// in phase 1.4 — every patch is broadcast from the worker.
	go s.wsReader(ctx, conn, cancel)

	// Subscribe to per-player handoff events so a gate jump rebinds this WS
	// to the new sector without a reconnect. Unauthenticated clients (player
	// id 0) skip it — there is no shared handoff topic for everyone.
	handoffCh := make(chan sector.PlayerHandoffEvent, handoffEventBuffer)
	if s.handoffBus != nil && playerID != 0 {
		err := s.handoffBus.Subscribe(ctx, sector.PlayerHandoffTopic(playerID), func(payload []byte) {
			var ev sector.PlayerHandoffEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				s.logger.Warn("ws decode handoff", "err", err, "player", int64(playerID))
				return
			}
			select {
			case handoffCh <- ev:
			default:
				s.logger.Warn("ws handoff event dropped — buffer full",
					"player", int64(playerID), "target", int64(ev.TargetSector))
			}
		})
		if err != nil {
			s.logger.Warn("ws handoff subscribe", "err", err, "player", int64(playerID))
		}
	}

	// Subscribe to per-player rent-overdue events (6.4) on the same bus. Same
	// pattern as handoff: decode and forward to a buffered channel the push
	// loop drains. Unauthenticated clients skip it.
	rentCh := make(chan rentOverdueFrame, rentOverdueBuffer)
	if s.handoffBus != nil && playerID != 0 {
		err := s.handoffBus.Subscribe(ctx, rent.OverdueTopic(playerID), func(payload []byte) {
			var ev rent.OverdueEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				s.logger.Warn("ws decode rent overdue", "err", err, "player", int64(playerID))
				return
			}
			frame := rentOverdueFrame{
				Type:            "rent_overdue",
				RentID:          int64(ev.RentID),
				StationKind:     int(ev.Station.Kind),
				StationID:       ev.Station.ID,
				AmountPerPeriod: ev.AmountPerPeriod,
				UnpaidPeriods:   ev.UnpaidPeriods,
				Confiscated:     ev.Confiscated,
			}
			select {
			case rentCh <- frame:
			default:
				s.logger.Warn("ws rent overdue event dropped — buffer full", "player", int64(playerID))
			}
		})
		if err != nil {
			s.logger.Warn("ws rent overdue subscribe", "err", err, "player", int64(playerID))
		}
	}

	// Subscribe to per-player police scan events (9.4) on the same bus. Same
	// pattern as rent: decode and forward to a buffered channel the push loop
	// drains. Unauthenticated clients skip it.
	policeCh := make(chan policeScanFrame, policeScanBuffer)
	if s.handoffBus != nil && playerID != 0 {
		err := s.handoffBus.Subscribe(ctx, sector.PoliceScanTopic(playerID), func(payload []byte) {
			var ev sector.PoliceScanEvent
			if err := json.Unmarshal(payload, &ev); err != nil {
				s.logger.Warn("ws decode police scan", "err", err, "player", int64(playerID))
				return
			}
			frame := policeScanFrame{
				Type:      "police_scan",
				Race:      int(ev.Race),
				SectorID:  int64(ev.SectorID),
				GoodsType: int(ev.GoodsType),
				Quantity:  ev.Quantity,
				Wanted:    ev.Wanted,
			}
			select {
			case policeCh <- frame:
			default:
				s.logger.Warn("ws police scan event dropped — buffer full", "player", int64(playerID))
			}
		})
		if err != nil {
			s.logger.Warn("ws police scan subscribe", "err", err, "player", int64(playerID))
		}
	}

	s.wsPushLoop(ctx, conn, sub, unsub, handoffCh, rentCh, policeCh, playerID)
}

// initialSubscribeSector chooses which sector a freshly opened WS should
// subscribe to. The explicit active ship (10.14a) wins; then the player's
// lowest-id ship in RAM; fallback is cfg.SectorID for unauthenticated clients
// (tests) and edge cases where the spawn race left the player without a ship.
// A dangling active_ship_id (ship not in any worker — sold/destroyed) silently
// falls through to the min-id rule.
func (s *Server) initialSubscribeSector(ctx context.Context, playerID domain.PlayerID) domain.SectorID {
	if playerID != 0 {
		if s.activeShips != nil {
			// Passenger (10.23) wins: follow the host's sector so a reload while
			// riding stays on the host, not the player's parked own ship.
			if hostID, ok, err := s.activeShips.PassengerHost(ctx, playerID); err == nil && ok && hostID != 0 {
				if sectorID, found := s.router.LookupShipSector(hostID); found {
					return sectorID
				}
			}
			if shipID, ok, err := s.activeShips.ActiveShip(ctx, playerID); err == nil && ok && shipID != 0 {
				if sectorID, found := s.router.LookupShipSector(shipID); found {
					return sectorID
				}
			}
		}
		if _, sectorID, ok := s.router.LookupPrimaryShipByPlayer(playerID); ok {
			return sectorID
		}
	}
	return domain.SectorID(s.cfg.SectorID)
}

// sendStatics pushes a single dto.StaticsMessage with the sector's static
// objects and the engine tick interval. Sent unconditionally — even an
// empty-statics sector must deliver tickIntervalMs so the SPA can size
// its interpolation step. Failures (marshal, write) are logged and
// ignored — they take down the WS connection naturally on the next
// write attempt.
func (s *Server) sendStatics(ctx context.Context, conn *websocket.Conn, sectorID domain.SectorID) {
	snap := s.router.Snapshot(sectorID)
	boundsRadius := s.cfg.SectorBoundsRadius
	if boundsRadius <= 0 {
		boundsRadius = 5000
	}
	nearZoom := s.cfg.NearZoomRadius
	if nearZoom <= 0 {
		nearZoom = 500
	}
	dockRange := s.cfg.DockRange
	if dockRange <= 0 {
		dockRange = 3
	}
	gateRange := s.cfg.GateRange
	if gateRange <= 0 {
		gateRange = 50
	}
	maxHP := s.cfg.MaxHP
	if maxHP <= 0 {
		maxHP = 100
	}
	maxShield := s.cfg.MaxShield
	if maxShield <= 0 {
		maxShield = 100
	}
	msg := dto.StaticsMessage{
		Type:               "statics",
		SectorID:           int64(sectorID),
		TickIntervalMs:     s.cfg.SnapshotInterval.Milliseconds(),
		SectorBoundsRadius: boundsRadius,
		NearZoomRadius:     nearZoom,
		DockRange:          dockRange,
		GateRange:          gateRange,
		MaxHP:              maxHP,
		MaxShield:          maxShield,
		Statics:            dto.StaticsFromDomain(snap.Statics),
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		s.logger.Warn("ws marshal statics", "err", err)
		return
	}
	writeCtx, wc := context.WithTimeout(ctx, wsWriteTimeout)
	defer wc()
	if err := conn.Write(writeCtx, websocket.MessageText, payload); err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logger.Debug("ws write statics", "err", err)
		}
	}
}

func (s *Server) wsReader(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err != nil {
			return
		}
	}
}

// wsPushLoop pumps patches and handoff events to the client until ctx ends
// or the write fails. On every PlayerHandoffEvent it unsubscribes from the
// current sector, opens a new subscription on the target, and resends the
// statics frame so the SPA can swap its world map. The WS connection stays
// open across hops.
func (s *Server) wsPushLoop(
	ctx context.Context,
	conn *websocket.Conn,
	sub *sector.Subscription,
	unsub func(),
	handoff <-chan sector.PlayerHandoffEvent,
	rentOverdue <-chan rentOverdueFrame,
	policeScan <-chan policeScanFrame,
	playerID domain.PlayerID,
) {
	currentSector := sub.SectorID
	patches := sub.Patch
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-rentOverdue:
			payload, err := json.Marshal(frame)
			if err != nil {
				s.logger.Warn("ws marshal rent overdue", "err", err)
				continue
			}
			writeCtx, wc := context.WithTimeout(ctx, wsWriteTimeout)
			err = conn.Write(writeCtx, websocket.MessageText, payload)
			wc()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.logger.Debug("ws write rent overdue", "err", err)
				}
				return
			}
		case frame := <-policeScan:
			payload, err := json.Marshal(frame)
			if err != nil {
				s.logger.Warn("ws marshal police scan", "err", err)
				continue
			}
			writeCtx, wc := context.WithTimeout(ctx, wsWriteTimeout)
			err = conn.Write(writeCtx, websocket.MessageText, payload)
			wc()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.logger.Debug("ws write police scan", "err", err)
				}
				return
			}
		case ev := <-handoff:
			if ev.TargetSector == currentSector {
				continue
			}
			newSub, newUnsub, err := s.router.Subscribe(ctx, ev.TargetSector, playerID)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.logger.Warn("ws resubscribe after handoff", "err", err,
						"player", int64(playerID), "target", int64(ev.TargetSector))
				}
				return
			}
			unsub()
			unsub = newUnsub
			currentSector = ev.TargetSector
			patches = newSub.Patch
			s.sendStatics(ctx, conn, currentSector)
		case patch, ok := <-patches:
			if !ok {
				return
			}
			payload, err := json.Marshal(s.buildSnapshotDTO(patch, int64(currentSector)))
			if err != nil {
				s.logger.Warn("ws marshal snapshot", "err", err)
				return
			}
			writeCtx, wc := context.WithTimeout(ctx, wsWriteTimeout)
			err = conn.Write(writeCtx, websocket.MessageText, payload)
			wc()
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.logger.Debug("ws write", "err", err)
				}
				return
			}
		}
	}
}

func (s *Server) buildSnapshotDTO(patch sector.Patch, sectorID int64) dto.Snapshot {
	npcID := int64(s.npcPlayerID)
	out := dto.Snapshot{
		Type:      "snapshot",
		SectorID:  sectorID,
		Tick:      patch.Tick,
		TimeScale: patch.TimeScale,
	}
	if len(patch.Added) > 0 {
		out.Added = dto.ShipsFromDomain(patch.Added, s.hullCategoryOf)
		dto.MarkNPC(out.Added, npcID)
	}
	if len(patch.Updated) > 0 {
		out.Updated = dto.ShipsFromDomain(patch.Updated, s.hullCategoryOf)
		dto.MarkNPC(out.Updated, npcID)
	}
	if len(patch.Removed) > 0 {
		out.Removed = make([]int64, len(patch.Removed))
		for i, id := range patch.Removed {
			out.Removed[i] = int64(id)
		}
	}
	if len(patch.LaserEffects) > 0 {
		out.LaserEffects = make([]dto.LaserBeam, len(patch.LaserEffects))
		for i, b := range patch.LaserEffects {
			out.LaserEffects[i] = dto.LaserBeam{
				AttackerShipID: int64(b.AttackerShipID),
				Target: dto.EntityRef{
					Kind: int(b.Target.Kind),
					ID:   b.Target.ID,
				},
				FromX:  b.From.X,
				FromY:  b.From.Y,
				ToX:    b.To.X,
				ToY:    b.To.Y,
				Damage: b.DamageDealt,
				Killed: b.Killed,
			}
		}
	}
	if len(patch.MissilesAdded) > 0 {
		out.MissilesAdded = dto.MissilesFromDomain(patch.MissilesAdded)
	}
	if len(patch.MissilesUpdated) > 0 {
		out.MissilesUpdated = dto.MissilesFromDomain(patch.MissilesUpdated)
	}
	if len(patch.MissilesRemoved) > 0 {
		out.MissilesRemoved = make([]int64, len(patch.MissilesRemoved))
		for i, id := range patch.MissilesRemoved {
			out.MissilesRemoved[i] = int64(id)
		}
	}
	if len(patch.MissileImpacts) > 0 {
		out.MissileImpacts = make([]dto.MissileImpact, len(patch.MissileImpacts))
		for i, imp := range patch.MissileImpacts {
			out.MissileImpacts[i] = dto.MissileImpact{
				MissileID: int64(imp.MissileID),
				Attacker:  int64(imp.AttackerShipID),
				Target: dto.EntityRef{
					Kind: int(imp.Target.Kind),
					ID:   imp.Target.ID,
				},
				X:       imp.Pos.X,
				Y:       imp.Pos.Y,
				Damage:  imp.Damage,
				Killed:  imp.Killed,
				Expired: imp.Expired,
			}
		}
	}
	if len(patch.DronesAdded) > 0 {
		out.DronesAdded = dto.DronesFromDomain(patch.DronesAdded)
	}
	if len(patch.DronesUpdated) > 0 {
		out.DronesUpdated = dto.DronesFromDomain(patch.DronesUpdated)
	}
	if len(patch.DronesRemoved) > 0 {
		out.DronesRemoved = make([]int64, len(patch.DronesRemoved))
		for i, id := range patch.DronesRemoved {
			out.DronesRemoved[i] = int64(id)
		}
	}
	if len(patch.DroneImpacts) > 0 {
		out.DroneImpacts = make([]dto.DroneImpact, len(patch.DroneImpacts))
		for i, imp := range patch.DroneImpacts {
			out.DroneImpacts[i] = dto.DroneImpact{
				DroneID: int64(imp.DroneID),
				Owner:   int64(imp.OwnerShipID),
				Target: dto.EntityRef{
					Kind: int(imp.Target.Kind),
					ID:   imp.Target.ID,
				},
				X:       imp.Pos.X,
				Y:       imp.Pos.Y,
				Damage:  imp.Damage,
				Killed:  imp.Killed,
				Expired: imp.Expired,
			}
		}
	}
	if len(patch.ContainersAdded) > 0 {
		out.ContainersAdded = dto.ContainersFromDomain(patch.ContainersAdded)
	}
	if len(patch.ContainersRemoved) > 0 {
		out.ContainersRemoved = make([]int64, len(patch.ContainersRemoved))
		for i, id := range patch.ContainersRemoved {
			out.ContainersRemoved[i] = int64(id)
		}
	}
	if len(patch.StaticsUpdated) > 0 {
		out.StaticsUpdated = make([]dto.DestructibleStatic, len(patch.StaticsUpdated))
		for i, d := range patch.StaticsUpdated {
			out.StaticsUpdated[i] = dto.DestructibleStatic{
				Ref:       dto.EntityRef{Kind: int(d.Ref.Kind), ID: d.Ref.ID},
				HP:        d.HP,
				Shield:    d.Shield,
				MaxShield: d.MaxShield,
			}
		}
	}
	if len(patch.StaticsRemoved) > 0 {
		out.StaticsRemoved = make([]dto.EntityRef, len(patch.StaticsRemoved))
		for i, ref := range patch.StaticsRemoved {
			out.StaticsRemoved[i] = dto.EntityRef{Kind: int(ref.Kind), ID: ref.ID}
		}
	}
	// Big-radar statics that just entered view (phase 10.20 L2) — full objects,
	// same encoding as the welcome StaticsMessage.
	if !patch.StaticsAdded.IsEmpty() {
		added := dto.StaticsFromDomain(patch.StaticsAdded)
		out.StaticsAdded = &added
	}
	return out
}
