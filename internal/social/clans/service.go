package clans

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"spaceempire/back/internal/domain"
)

// Clan name/tag length bounds (after trimming surrounding whitespace).
const (
	minNameLen = 3
	maxNameLen = 32
	minTagLen  = 2
	maxTagLen  = 6
)

// Repo is the persistence surface Service needs (ISP). The concrete
// *Repository satisfies it; tests stub it.
type Repo interface {
	CreateClanWithLeader(ctx context.Context, name, tag string, leader domain.PlayerID) (domain.ClanID, error)
	AcceptInvitation(ctx context.Context, clanID domain.ClanID, player domain.PlayerID) error
	GetMembership(ctx context.Context, player domain.PlayerID) (Membership, error)
	DeleteMember(ctx context.Context, player domain.PlayerID) error
	DeleteClan(ctx context.Context, clanID domain.ClanID) error
	CountMembers(ctx context.Context, clanID domain.ClanID) (int, error)
	CreateInvitation(ctx context.Context, clanID domain.ClanID, target, invitedBy domain.PlayerID) error
	SetMemberRole(ctx context.Context, clanID domain.ClanID, target domain.PlayerID, role string) error
	GetClan(ctx context.Context, clanID domain.ClanID) (Clan, error)
	ListClans(ctx context.Context) ([]ClanSummary, error)
	ListMembers(ctx context.Context, clanID domain.ClanID) ([]MemberView, error)
	ListInvitationsByClan(ctx context.Context, clanID domain.ClanID) ([]InvitationView, error)
	ListInvitationsByPlayer(ctx context.Context, player domain.PlayerID) ([]InvitationView, error)
}

// ClanDetail is a clan with its members and pending invitations.
type ClanDetail struct {
	Clan        Clan
	Members     []MemberView
	Invitations []InvitationView
}

// Service holds the clan business rules.
type Service struct {
	repo Repo
}

// NewService wires a Service over the repository.
func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

// Create founds a new clan with the caller as its leader. The caller must
// not already be in a clan. Returns the created clan.
func (s *Service) Create(ctx context.Context, leader domain.PlayerID, name, tag string) (Clan, error) {
	name = strings.TrimSpace(name)
	tag = strings.TrimSpace(tag)
	if l := len(name); l < minNameLen || l > maxNameLen {
		return Clan{}, fmt.Errorf("%w: name length must be %d..%d", ErrInvalidInput, minNameLen, maxNameLen)
	}
	if l := len(tag); l < minTagLen || l > maxTagLen {
		return Clan{}, fmt.Errorf("%w: tag length must be %d..%d", ErrInvalidInput, minTagLen, maxTagLen)
	}

	clanID, err := s.repo.CreateClanWithLeader(ctx, name, tag, leader)
	if err != nil {
		return Clan{}, err
	}
	return s.repo.GetClan(ctx, clanID)
}

// Invite invites target into clanID. The caller must be a manager (leader
// or officer) of that clan, and the target must not already be in any clan.
func (s *Service) Invite(ctx context.Context, inviter domain.PlayerID, clanID domain.ClanID, target domain.PlayerID) error {
	if err := s.requireManager(ctx, inviter, clanID); err != nil {
		return err
	}
	// Reject inviting someone already in a clan (covers self-invite too,
	// since the inviter is in this clan).
	if _, err := s.repo.GetMembership(ctx, target); err == nil {
		return ErrAlreadyInClan
	} else if !isNotMember(err) {
		return err
	}
	return s.repo.CreateInvitation(ctx, clanID, target, inviter)
}

// Accept consumes the caller's pending invitation to clanID and joins them
// as a member.
func (s *Service) Accept(ctx context.Context, player domain.PlayerID, clanID domain.ClanID) error {
	return s.repo.AcceptInvitation(ctx, clanID, player)
}

// Kick removes target from clanID. The caller must be a manager; the leader
// cannot be kicked.
func (s *Service) Kick(ctx context.Context, actor domain.PlayerID, clanID domain.ClanID, target domain.PlayerID) error {
	if err := s.requireManager(ctx, actor, clanID); err != nil {
		return err
	}
	tm, err := s.repo.GetMembership(ctx, target)
	if isNotMember(err) || (err == nil && tm.ClanID != clanID) {
		return ErrTargetNotMember
	}
	if err != nil {
		return err
	}
	if tm.Role == RoleLeader {
		return ErrCannotKickLeader
	}
	return s.repo.DeleteMember(ctx, target)
}

// SetRole promotes/demotes a member between member and officer (phase 8.6).
// Only the clan leader may change roles; the leader's own role cannot be
// changed here (transfer is a separate concern). Once promoted, an officer
// passes canManage and can invite/kick.
func (s *Service) SetRole(ctx context.Context, actor domain.PlayerID, clanID domain.ClanID, target domain.PlayerID, role string) error {
	if role != RoleMember && role != RoleOfficer {
		return ErrInvalidRole
	}
	am, err := s.repo.GetMembership(ctx, actor)
	if isNotMember(err) || (err == nil && am.ClanID != clanID) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if am.Role != RoleLeader {
		return ErrForbidden
	}
	tm, err := s.repo.GetMembership(ctx, target)
	if isNotMember(err) || (err == nil && tm.ClanID != clanID) {
		return ErrTargetNotMember
	}
	if err != nil {
		return err
	}
	if tm.Role == RoleLeader {
		return ErrCannotChangeLeader
	}
	return s.repo.SetMemberRole(ctx, clanID, target, role)
}

// Leave removes the caller from clanID. A plain member just leaves; the
// leader may only leave when they are the last member (which disbands the
// clan) — otherwise ErrLeaderMustTransfer.
func (s *Service) Leave(ctx context.Context, player domain.PlayerID, clanID domain.ClanID) error {
	m, err := s.repo.GetMembership(ctx, player)
	if isNotMember(err) || (err == nil && m.ClanID != clanID) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if m.Role != RoleLeader {
		return s.repo.DeleteMember(ctx, player)
	}
	count, err := s.repo.CountMembers(ctx, clanID)
	if err != nil {
		return err
	}
	if count > 1 {
		return ErrLeaderMustTransfer
	}
	return s.repo.DeleteClan(ctx, clanID)
}

// List returns every clan with its member count.
func (s *Service) List(ctx context.Context) ([]ClanSummary, error) {
	return s.repo.ListClans(ctx)
}

// Detail returns a clan with its members and pending invitations.
func (s *Service) Detail(ctx context.Context, clanID domain.ClanID) (ClanDetail, error) {
	clan, err := s.repo.GetClan(ctx, clanID)
	if err != nil {
		return ClanDetail{}, err
	}
	members, err := s.repo.ListMembers(ctx, clanID)
	if err != nil {
		return ClanDetail{}, err
	}
	invites, err := s.repo.ListInvitationsByClan(ctx, clanID)
	if err != nil {
		return ClanDetail{}, err
	}
	return ClanDetail{Clan: clan, Members: members, Invitations: invites}, nil
}

// MyClan returns the caller's clan detail, or (nil, nil) when they are not
// in a clan.
func (s *Service) MyClan(ctx context.Context, player domain.PlayerID) (*ClanDetail, error) {
	m, err := s.repo.GetMembership(ctx, player)
	if isNotMember(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	detail, err := s.Detail(ctx, m.ClanID)
	if err != nil {
		return nil, err
	}
	return &detail, nil
}

// MyInvitations returns the caller's pending invitations.
func (s *Service) MyInvitations(ctx context.Context, player domain.PlayerID) ([]InvitationView, error) {
	return s.repo.ListInvitationsByPlayer(ctx, player)
}

// requireManager checks the actor is a manager (leader/officer) of clanID.
func (s *Service) requireManager(ctx context.Context, actor domain.PlayerID, clanID domain.ClanID) error {
	m, err := s.repo.GetMembership(ctx, actor)
	if isNotMember(err) || (err == nil && m.ClanID != clanID) {
		return ErrNotMember
	}
	if err != nil {
		return err
	}
	if !canManage(m.Role) {
		return ErrForbidden
	}
	return nil
}

// isNotMember reports whether err is the ErrNotMember sentinel.
func isNotMember(err error) bool {
	return errors.Is(err, ErrNotMember)
}
