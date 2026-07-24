package domain

import "time"

// QuestStatus is the lifecycle of a player's quest: active until the last step
// is satisfied (completed), or deadline-expired (failed), or dropped by the
// player (abandoned). Phase 8.17 added failed/abandoned.
type QuestStatus string

const (
	QuestActive    QuestStatus = "active"
	QuestCompleted QuestStatus = "completed"
	QuestFailed    QuestStatus = "failed"
	QuestAbandoned QuestStatus = "abandoned"
)

// QuestProgress is one player's persisted progress on one quest (phase 8.12,
// extended in 8.17). Quest definitions (steps, rewards) live in code
// (internal/quest); this is only the moving state. State holds the raw JSONB
// progress (e.g. the counter toward the current event-driven step). DeadlineAt
// is the wall-clock deadline (zero = no deadline). See docs/specs/quest.md.
type QuestProgress struct {
	Player      PlayerID
	QuestID     string
	StepIndex   int
	Status      QuestStatus
	State       []byte
	DeadlineAt  time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

// QuestOffer is a generated procedural quest instance awaiting the player's
// accept (TASK-89). It lives in player_quest_offers with a TTL (ExpiresAt).
// Definition holds the frozen instance JSONB for a procedural template; it is
// nil for a story offer, whose parameters resolve through the static
// Lookup(TemplateID). Source is the pacer trigger that produced the offer:
// "dock" (bulletin board) or "space" (intercepted signal).
type QuestOffer struct {
	ID         int64
	Player     PlayerID
	TemplateID string
	Definition []byte
	Source     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// QuestCounters is the per-player pacer state (TASK-89): how many docks/jumps
// have accrued toward the next offer, and the randomised thresholds at which
// the next offer fires. NextDocks/NextJumps == 0 means the threshold has not
// been rolled yet. One row per player in player_quest_counters.
type QuestCounters struct {
	Player    PlayerID
	Docks     int
	Jumps     int
	NextDocks int
	NextJumps int
}
