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
