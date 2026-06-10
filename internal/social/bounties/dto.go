package bounties

import (
	"time"

	"spaceempire/back/internal/domain"
	bountyrepo "spaceempire/back/internal/persistence/bounties"
)

// setRequest is the POST /api/bounties body. ttlHours=0 → server default;
// fromClan funds it from the caller's clan treasury (leader only).
type setRequest struct {
	TargetKind string `json:"targetKind"` // "player" | "clan"
	TargetID   int64  `json:"targetId"`
	Amount     int64  `json:"amount"`
	TTLHours   int    `json:"ttlHours"`
	FromClan   bool   `json:"fromClan"`
}

// bountyDTO is the JSON shape returned by the list/history endpoints.
type bountyDTO struct {
	ID          int64     `json:"id"`
	TargetKind  string    `json:"targetKind"`
	TargetID    int64     `json:"targetId"`
	TargetName  string    `json:"targetName"`
	SponsorKind string    `json:"sponsorKind"`
	SponsorID   int64     `json:"sponsorId"`
	SponsorName string    `json:"sponsorName"`
	Amount      int64     `json:"amount"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func toBountyDTO(v bountyrepo.View) bountyDTO {
	return bountyDTO{
		ID:          int64(v.ID),
		TargetKind:  kindString(v.Target.Kind),
		TargetID:    v.Target.ID,
		TargetName:  v.TargetName,
		SponsorKind: kindString(v.Sponsor.Kind),
		SponsorID:   v.Sponsor.ID,
		SponsorName: v.SponsorName,
		Amount:      v.Amount,
		Status:      string(v.Status),
		CreatedAt:   v.CreatedAt,
		ExpiresAt:   v.ExpiresAt,
	}
}

// kindString maps the entity kind to its wire token; "" for unsupported.
func kindString(k domain.EntityKind) string {
	switch k {
	case domain.EntityKindPlayer:
		return "player"
	case domain.EntityKindClan:
		return "clan"
	default:
		return ""
	}
}

// parseKind is the inverse of kindString.
func parseKind(s string) (domain.EntityKind, bool) {
	switch s {
	case "player":
		return domain.EntityKindPlayer, true
	case "clan":
		return domain.EntityKindClan, true
	default:
		return domain.EntityKindUnknown, false
	}
}
