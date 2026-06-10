package clans

import "time"

// --- request bodies ---

type createRequest struct {
	Name string `json:"name"`
	Tag  string `json:"tag"`
}

type playerRequest struct {
	PlayerID int64 `json:"playerId"`
}

type roleRequest struct {
	PlayerID int64  `json:"playerId"`
	Role     string `json:"role"` // "officer" | "member"
}

// --- response shapes ---

type clanSummaryDTO struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Tag         string    `json:"tag"`
	LeaderID    int64     `json:"leaderId"`
	MemberCount int       `json:"memberCount"`
	CreatedAt   time.Time `json:"createdAt"`
}

type memberDTO struct {
	PlayerID int64     `json:"playerId"`
	Login    string    `json:"login"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}

type invitationDTO struct {
	ClanID    int64     `json:"clanId"`
	ClanName  string    `json:"clanName"`
	ClanTag   string    `json:"clanTag"`
	PlayerID  int64     `json:"playerId"`
	Login     string    `json:"login"`
	InvitedBy int64     `json:"invitedBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type clanDetailDTO struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Tag         string          `json:"tag"`
	LeaderID    int64           `json:"leaderId"`
	Treasury    int64           `json:"treasury"`
	CreatedAt   time.Time       `json:"createdAt"`
	Members     []memberDTO     `json:"members"`
	Invitations []invitationDTO `json:"invitations"`
}

func toClanSummaryDTO(s ClanSummary) clanSummaryDTO {
	return clanSummaryDTO{
		ID:          int64(s.ID),
		Name:        s.Name,
		Tag:         s.Tag,
		LeaderID:    int64(s.LeaderID),
		MemberCount: s.MemberCount,
		CreatedAt:   s.CreatedAt,
	}
}

func toMemberDTO(m MemberView) memberDTO {
	return memberDTO{
		PlayerID: int64(m.PlayerID),
		Login:    m.Login,
		Role:     m.Role,
		JoinedAt: m.JoinedAt,
	}
}

func toInvitationDTO(i InvitationView) invitationDTO {
	return invitationDTO{
		ClanID:    int64(i.ClanID),
		ClanName:  i.ClanName,
		ClanTag:   i.ClanTag,
		PlayerID:  int64(i.PlayerID),
		Login:     i.Login,
		InvitedBy: int64(i.InvitedBy),
		CreatedAt: i.CreatedAt,
	}
}

func toClanDetailDTO(d ClanDetail) clanDetailDTO {
	members := make([]memberDTO, 0, len(d.Members))
	for _, m := range d.Members {
		members = append(members, toMemberDTO(m))
	}
	invites := make([]invitationDTO, 0, len(d.Invitations))
	for _, i := range d.Invitations {
		invites = append(invites, toInvitationDTO(i))
	}
	return clanDetailDTO{
		ID:          int64(d.Clan.ID),
		Name:        d.Clan.Name,
		Tag:         d.Clan.Tag,
		LeaderID:    int64(d.Clan.LeaderID),
		Treasury:    d.Clan.Treasury,
		CreatedAt:   d.Clan.CreatedAt,
		Members:     members,
		Invitations: invites,
	}
}
