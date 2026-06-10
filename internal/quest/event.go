package quest

import "spaceempire/back/internal/domain"

// EventKind classifies a discrete domain event the quest engine reconciles
// against event-driven steps (kill/deliver/trade). Polled steps don't use this.
type EventKind string

const (
	EventKill    EventKind = "kill"
	EventDeliver EventKind = "deliver"
	EventTrade   EventKind = "trade"
)

// Event is a player-scoped domain signal fed to Service.OnEvent. The app
// builder translates bus payloads (sector.EntityKilledEvent, the cargo/trade
// events below) into this neutral shape.
type Event struct {
	Player domain.PlayerID
	Kind   EventKind
	Victim domain.EntityRef   // EventKill: who died
	Target domain.EntityRef   // EventDeliver: destination station
	Goods  domain.GoodsTypeID // EventDeliver/EventTrade
	Side   string             // EventTrade: "buy"/"sell"
	Amount int64              // delivered/traded qty (kill contributes 1)
}

// Bus topics + payloads for the player-action events the api layer publishes
// (kill already has sector.EntityKilledEvent / sector.EntityKilledTopic).
const (
	CargoDeliveredTopic = "quest.cargo.delivered"
	TradeCompletedTopic = "quest.trade.completed"
)

// CargoDeliveredEvent is published when a player unloads cargo onto a station
// (cargo move_down). Drives StepDeliver.
type CargoDeliveredEvent struct {
	Player domain.PlayerID    `json:"player"`
	Target domain.EntityRef   `json:"target"`
	Goods  domain.GoodsTypeID `json:"goods"`
	Qty    int64              `json:"qty"`
}

// TradeCompletedEvent is published on a successful player buy/sell. Drives
// StepTrade.
type TradeCompletedEvent struct {
	Player domain.PlayerID    `json:"player"`
	Side   string             `json:"side"`
	Goods  domain.GoodsTypeID `json:"goods"`
	Qty    int64              `json:"qty"`
}
