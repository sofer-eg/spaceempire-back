package api

import (
	"context"
	"encoding/json"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/quest"
)

// publishCargoDelivered emits a quest cargo.delivered signal for a successful
// player unload onto a station (phase 8.17). Best-effort: a nil bus or a
// publish error never affects the cargo move that already committed.
func (s *Server) publishCargoDelivered(ctx context.Context, player domain.PlayerID, target domain.EntityRef, goods domain.GoodsTypeID, qty int64) {
	if s.eventBus == nil || player == 0 {
		return
	}
	payload, err := json.Marshal(quest.CargoDeliveredEvent{Player: player, Target: target, Goods: goods, Qty: qty})
	if err != nil {
		return
	}
	if err := s.eventBus.Publish(ctx, quest.CargoDeliveredTopic, payload); err != nil {
		s.logger.Warn("publish cargo.delivered", "err", err)
	}
}

// publishTradeCompleted emits a quest trade.completed signal for a successful
// player buy/sell (phase 8.17). Best-effort.
func (s *Server) publishTradeCompleted(ctx context.Context, player domain.PlayerID, side string, goods domain.GoodsTypeID, qty int64) {
	if s.eventBus == nil || player == 0 {
		return
	}
	payload, err := json.Marshal(quest.TradeCompletedEvent{Player: player, Side: side, Goods: goods, Qty: qty})
	if err != nil {
		return
	}
	if err := s.eventBus.Publish(ctx, quest.TradeCompletedTopic, payload); err != nil {
		s.logger.Warn("publish trade.completed", "err", err)
	}
}
