package sector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"spaceempire/back/internal/domain"
)

const captureModuleType = "up_capture"

// khaakRace is race 8 in the StarWind dump (Kha'ak). Capturing a Kha'ak ship uses
// the higher khaak_capture_chance threshold (SP DoCapture). Kept local so the
// sector package stays free of the race reference dependency (mirrors pirateRace).
const khaakRace domain.RaceID = 8

var (
	// ErrCaptureOutOfRange is reported by CaptureShipCommand when the attacker is
	// farther than CaptureRange from the target. HTTP maps it to 422.
	ErrCaptureOutOfRange = errors.New("sector: capture target out of range")
	// ErrCaptureShielded is reported when the target still has a working shield
	// generator (MaxShield > 0) — "захват при работающем щите невозможен" (SP
	// DoCapture). The target must first have its up_shield knocked off
	// (TASK-100.3.9.1 → MaxShield 0) to be capturable. HTTP maps it to 422.
	ErrCaptureShielded = errors.New("sector: capture target shield is up")
)

// ShipCaptureTopic is the per-player bus topic a capture outcome is journalled to
// (TASK-100.3.9.4). The WS handler subscribes to its own player's topic, mirroring
// ModuleKnockedTopic / StationHackedTopic. Distinct from ShipCapturedTopic (the
// global crew-eject topic from .2): this one is per-recipient and carries the
// journal line, published to BOTH the captor and the (real-player) old owner.
func ShipCaptureTopic(player domain.PlayerID) string {
	return fmt.Sprintf("ship.capture.%d", int64(player))
}

// ShipCaptureEvent is the per-player capture journal line. Captor distinguishes the
// attacker (true) from the old owner (false); Success is the roll outcome. The SPA
// renders: captor+success "Корабль захвачен", captor+!success "Захват не удался",
// !captor+success "Ваш корабль захвачен" (TASK-100.3.9.5).
type ShipCaptureEvent struct {
	PlayerID domain.PlayerID `json:"playerId"`
	ShipID   domain.ShipID   `json:"shipId"`
	SectorID domain.SectorID `json:"sectorId"`
	Captor   bool            `json:"captor"`
	Success  bool            `json:"success"`
}

// publishShipCapture emits the per-player capture journal event on the bus. Best-
// effort: a nil bus (pure unit tests), a zero recipient, or a publish error is
// skipped/logged, never blocking the tick. Mirrors publishStationHacked.
func (w *Worker) publishShipCapture(ctx context.Context, s *sectorState, recipient domain.PlayerID, shipID domain.ShipID, captor, success bool) {
	if w.bus == nil || recipient == 0 {
		return
	}
	payload, err := json.Marshal(ShipCaptureEvent{
		PlayerID: recipient,
		ShipID:   shipID,
		SectorID: s.sectorID,
		Captor:   captor,
		Success:  success,
	})
	if err != nil {
		w.logger.ErrorContext(ctx, "capture: marshal journal", "err", err, "player", int64(recipient))
		return
	}
	if err := w.bus.Publish(ctx, ShipCaptureTopic(recipient), payload); err != nil {
		w.logger.ErrorContext(ctx, "capture: publish journal", "err", err, "player", int64(recipient))
	}
}
