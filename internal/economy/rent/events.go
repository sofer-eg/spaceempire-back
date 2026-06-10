package rent

import (
	"fmt"

	"spaceempire/back/internal/domain"
)

// OverdueEvent is published per-player when a rent charge is missed (6.4). It
// is delivered to the player's WS connection as a "rent_overdue" frame. When
// Confiscated is true the object has been taken (owner cleared) and the rent
// row deleted; otherwise it is a warning with the running UnpaidPeriods count.
type OverdueEvent struct {
	RentID          domain.RentID
	Payer           domain.PlayerID
	Station         domain.EntityRef
	AmountPerPeriod int64
	UnpaidPeriods   int
	Confiscated     bool
}

// OverdueTopic is the per-player bus topic OverdueEvents are published on,
// mirroring sector.PlayerHandoffTopic. The WS handler subscribes to its own
// player's topic.
func OverdueTopic(player domain.PlayerID) string {
	return fmt.Sprintf("rent.overdue.%d", int64(player))
}
