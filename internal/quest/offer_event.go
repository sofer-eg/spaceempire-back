package quest

import (
	"fmt"

	"spaceempire/back/internal/domain"
)

// OfferTopic is the per-player bus topic on which a generated quest offer is
// pushed to the journal (TASK-89, FR-10). The WS handler subscribes per
// connection and forwards an OfferEvent as a quest_offer frame, mirroring the
// per-player police/handoff topics.
func OfferTopic(p domain.PlayerID) string {
	return fmt.Sprintf("quest.offer.%d", int64(p))
}

// OfferEvent is the journal projection of a fresh offer the pacer just created
// (TASK-89). It carries only what the journal renders — the full definition
// stays server-side; accept round-trips through OfferID. OfferID is the offer
// row id in "proc:<id>" form for both procedural and story offers; the accept
// path (MR5) resolves a story offer through its persisted template_id.
type OfferEvent struct {
	OfferID     string `json:"offerId"`
	Title       string `json:"title"`
	Desc        string `json:"desc"`
	Source      string `json:"source"`
	ExpiresUnix int64  `json:"expiresUnix"`
	RewardCash  int64  `json:"rewardCash"`
}
