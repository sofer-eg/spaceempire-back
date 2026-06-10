package clans

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database"
)

const pgUniqueViolation = "23505"

// Roles a clan member can hold. Phase 6.1 only ever assigns leader (the
// founder) and member (an accepted invitee); officer exists for forward
// compatibility (promotion is a later task) and counts as a manager.
const (
	RoleLeader  = "leader"
	RoleOfficer = "officer"
	RoleMember  = "member"
)

// canManage reports whether a role may invite and kick. Leaders and
// officers manage; plain members cannot.
func canManage(role string) bool {
	return role == RoleLeader || role == RoleOfficer
}

// Clan is a persisted clans row.
type Clan struct {
	ID        domain.ClanID
	Name      string
	Tag       string
	LeaderID  domain.PlayerID
	Treasury  int64
	CreatedAt time.Time
}

// Membership ties a player to their single clan with a role.
type Membership struct {
	PlayerID domain.PlayerID
	ClanID   domain.ClanID
	Role     string
	JoinedAt time.Time
}

// ClanSummary is a clan plus its current member count, for the clans list.
type ClanSummary struct {
	Clan
	MemberCount int
}

// MemberView is a clan member enriched with their login, for the detail view.
type MemberView struct {
	PlayerID domain.PlayerID
	Login    string
	Role     string
	JoinedAt time.Time
}

// InvitationView is a pending invitation enriched with both the invited
// player's login and the clan's name/tag, so it can serve both the
// clan-side ("who is invited") and the player-side ("which clans invited
// me") listings.
type InvitationView struct {
	ClanID    domain.ClanID
	ClanName  string
	ClanTag   string
	PlayerID  domain.PlayerID
	Login     string
	InvitedBy domain.PlayerID
	CreatedAt time.Time
}

// Repository talks to the clan tables via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// NewRepository wires a Repository to the given executor.
func NewRepository(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// WithExecutor returns a Repository bound to a different executor (a tx). Used
// by the bounties TxRunner (6.3) so a clan-treasury debit/refund commits in
// the same transaction as the bounty insert/settle.
func (r *Repository) WithExecutor(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// ErrInsufficientTreasury is returned by AdjustTreasury when a negative delta
// would overdraw the clan's treasury.
var ErrInsufficientTreasury = errors.New("clans: insufficient treasury")

const adjustTreasurySQL = `
UPDATE clans
SET treasury_cash = treasury_cash + $2
WHERE id = $1 AND treasury_cash + $2 >= 0
RETURNING treasury_cash`

// AdjustTreasury applies delta to the clan's treasury atomically and returns
// the new balance. A negative delta that would overdraw is rejected with
// ErrInsufficientTreasury (mirrors players.AdjustCash). Added for 6.3
// clan-funded bounties.
func (r *Repository) AdjustTreasury(ctx context.Context, clanID domain.ClanID, delta int64) (int64, error) {
	var newCash int64
	err := r.exec.QueryRow(ctx, adjustTreasurySQL, int64(clanID), delta).Scan(&newCash)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInsufficientTreasury
	}
	if err != nil {
		return 0, fmt.Errorf("adjust clan treasury: %w", err)
	}
	return newCash, nil
}

const createClanSQL = `
WITH new_clan AS (
    INSERT INTO clans (name, tag, leader_id) VALUES ($1, $2, $3) RETURNING id
)
INSERT INTO clan_members (player_id, clan_id, role)
SELECT $3, id, 'leader' FROM new_clan
RETURNING clan_id
`

// CreateClanWithLeader inserts the clan and its leader membership in one
// atomic statement (a data-modifying CTE — no explicit transaction needed).
// Unique-violations map to: clans.name → ErrNameTaken, clans.tag →
// ErrTagTaken, clan_members.player_id (leader already in a clan) →
// ErrAlreadyInClan. On any failure the whole statement rolls back, so no
// orphan clan is left behind.
func (r *Repository) CreateClanWithLeader(ctx context.Context, name, tag string, leader domain.PlayerID) (domain.ClanID, error) {
	var clanID int64
	err := r.exec.QueryRow(ctx, createClanSQL, name, tag, int64(leader)).Scan(&clanID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			switch pgErr.ConstraintName {
			case "clans_name_key":
				return 0, ErrNameTaken
			case "clans_tag_key":
				return 0, ErrTagTaken
			case "clan_members_pkey":
				return 0, ErrAlreadyInClan
			}
		}
		return 0, fmt.Errorf("insert clan with leader: %w", err)
	}
	return domain.ClanID(clanID), nil
}

const acceptInvitationSQL = `
WITH del AS (
    DELETE FROM clan_invitations WHERE clan_id = $1 AND player_id = $2 RETURNING clan_id
)
INSERT INTO clan_members (player_id, clan_id, role)
SELECT $2, clan_id, 'member' FROM del
RETURNING clan_id
`

// AcceptInvitation consumes the pending invitation and adds the player as a
// member in one atomic statement. Returns ErrInvitationNotFound when there
// was no pending invitation (the INSERT then has no source row), or
// ErrAlreadyInClan when the player is already in a clan (the member insert
// hits the PK; the invitation delete rolls back so it can be retried after
// leaving).
func (r *Repository) AcceptInvitation(ctx context.Context, clanID domain.ClanID, player domain.PlayerID) error {
	var joined int64
	err := r.exec.QueryRow(ctx, acceptInvitationSQL, int64(clanID), int64(player)).Scan(&joined)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvitationNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return ErrAlreadyInClan
		}
		return fmt.Errorf("accept invitation: %w", err)
	}
	return nil
}

const getMembershipSQL = `
SELECT player_id, clan_id, role, joined_at FROM clan_members WHERE player_id = $1
`

// GetMembership returns the player's single membership, or ErrNotMember when
// the player is not in any clan.
func (r *Repository) GetMembership(ctx context.Context, player domain.PlayerID) (Membership, error) {
	var m Membership
	var pid, cid int64
	err := r.exec.QueryRow(ctx, getMembershipSQL, int64(player)).Scan(&pid, &cid, &m.Role, &m.JoinedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Membership{}, ErrNotMember
	}
	if err != nil {
		return Membership{}, fmt.Errorf("select membership: %w", err)
	}
	m.PlayerID = domain.PlayerID(pid)
	m.ClanID = domain.ClanID(cid)
	return m, nil
}

const deleteMemberSQL = `DELETE FROM clan_members WHERE player_id = $1`

// DeleteMember removes a player's membership. Missing rows are a no-op.
func (r *Repository) DeleteMember(ctx context.Context, player domain.PlayerID) error {
	if _, err := r.exec.Exec(ctx, deleteMemberSQL, int64(player)); err != nil {
		return fmt.Errorf("delete clan member: %w", err)
	}
	return nil
}

const setMemberRoleSQL = `UPDATE clan_members SET role = $3 WHERE player_id = $2 AND clan_id = $1`

// SetMemberRole changes a member's role (phase 8.6, promote/demote). Missing
// rows are a no-op; the service validates membership and the target role.
func (r *Repository) SetMemberRole(ctx context.Context, clanID domain.ClanID, target domain.PlayerID, role string) error {
	if _, err := r.exec.Exec(ctx, setMemberRoleSQL, int64(clanID), int64(target), role); err != nil {
		return fmt.Errorf("set clan member role: %w", err)
	}
	return nil
}

const deleteClanSQL = `DELETE FROM clans WHERE id = $1`

// DeleteClan removes a clan; members and invitations cascade away.
func (r *Repository) DeleteClan(ctx context.Context, clanID domain.ClanID) error {
	if _, err := r.exec.Exec(ctx, deleteClanSQL, int64(clanID)); err != nil {
		return fmt.Errorf("delete clan: %w", err)
	}
	return nil
}

const countMembersSQL = `SELECT count(*) FROM clan_members WHERE clan_id = $1`

// CountMembers returns the number of members in a clan.
func (r *Repository) CountMembers(ctx context.Context, clanID domain.ClanID) (int, error) {
	var n int
	if err := r.exec.QueryRow(ctx, countMembersSQL, int64(clanID)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count members: %w", err)
	}
	return n, nil
}

const createInvitationSQL = `
INSERT INTO clan_invitations (clan_id, player_id, invited_by) VALUES ($1, $2, $3)
`

// CreateInvitation inserts a pending invitation. A duplicate (clan, player)
// triggers a unique-violation, mapped to ErrAlreadyInvited.
func (r *Repository) CreateInvitation(ctx context.Context, clanID domain.ClanID, target, invitedBy domain.PlayerID) error {
	_, err := r.exec.Exec(ctx, createInvitationSQL, int64(clanID), int64(target), int64(invitedBy))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return ErrAlreadyInvited
		}
		return fmt.Errorf("insert invitation: %w", err)
	}
	return nil
}

const loadAllMembershipsSQL = `SELECT player_id, clan_id FROM clan_members`

// LoadAllMemberships returns every player's clan as a map, for the relations
// Service to resolve same-clan / clan-war propagation at Precount. Satisfies
// relations.Memberships.
func (r *Repository) LoadAllMemberships(ctx context.Context) (map[domain.PlayerID]domain.ClanID, error) {
	rows, err := r.exec.Query(ctx, loadAllMembershipsSQL)
	if err != nil {
		return nil, fmt.Errorf("query memberships: %w", err)
	}
	defer rows.Close()

	out := make(map[domain.PlayerID]domain.ClanID)
	for rows.Next() {
		var player, clan int64
		if err := rows.Scan(&player, &clan); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		out[domain.PlayerID(player)] = domain.ClanID(clan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memberships: %w", err)
	}
	return out, nil
}

const getClanSQL = `SELECT id, name, tag, leader_id, treasury_cash, created_at FROM clans WHERE id = $1`

// GetClan returns one clan or ErrClanNotFound.
func (r *Repository) GetClan(ctx context.Context, clanID domain.ClanID) (Clan, error) {
	var c Clan
	var id, leader int64
	err := r.exec.QueryRow(ctx, getClanSQL, int64(clanID)).Scan(&id, &c.Name, &c.Tag, &leader, &c.Treasury, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Clan{}, ErrClanNotFound
	}
	if err != nil {
		return Clan{}, fmt.Errorf("select clan: %w", err)
	}
	c.ID = domain.ClanID(id)
	c.LeaderID = domain.PlayerID(leader)
	return c, nil
}

const listClansSQL = `
SELECT c.id, c.name, c.tag, c.leader_id, c.treasury_cash, c.created_at,
       count(m.player_id) AS member_count
FROM clans c
LEFT JOIN clan_members m ON m.clan_id = c.id
GROUP BY c.id
ORDER BY c.name
`

// ListClans returns every clan with its member count, ordered by name.
func (r *Repository) ListClans(ctx context.Context) ([]ClanSummary, error) {
	rows, err := r.exec.Query(ctx, listClansSQL)
	if err != nil {
		return nil, fmt.Errorf("query clans: %w", err)
	}
	defer rows.Close()

	var out []ClanSummary
	for rows.Next() {
		var s ClanSummary
		var id, leader int64
		if err := rows.Scan(&id, &s.Name, &s.Tag, &leader, &s.Treasury, &s.CreatedAt, &s.MemberCount); err != nil {
			return nil, fmt.Errorf("scan clan: %w", err)
		}
		s.ID = domain.ClanID(id)
		s.LeaderID = domain.PlayerID(leader)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate clans: %w", err)
	}
	return out, nil
}

const listMembersSQL = `
SELECT m.player_id, p.login, m.role, m.joined_at
FROM clan_members m
JOIN players p ON p.id = m.player_id
WHERE m.clan_id = $1
ORDER BY m.joined_at
`

// ListMembers returns the clan's members enriched with their login.
func (r *Repository) ListMembers(ctx context.Context, clanID domain.ClanID) ([]MemberView, error) {
	rows, err := r.exec.Query(ctx, listMembersSQL, int64(clanID))
	if err != nil {
		return nil, fmt.Errorf("query members: %w", err)
	}
	defer rows.Close()

	var out []MemberView
	for rows.Next() {
		var mv MemberView
		var pid int64
		if err := rows.Scan(&pid, &mv.Login, &mv.Role, &mv.JoinedAt); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		mv.PlayerID = domain.PlayerID(pid)
		out = append(out, mv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return out, nil
}

const listInvitationsByClanSQL = `
SELECT i.clan_id, c.name, c.tag, i.player_id, p.login, i.invited_by, i.created_at
FROM clan_invitations i
JOIN clans c ON c.id = i.clan_id
JOIN players p ON p.id = i.player_id
WHERE i.clan_id = $1
ORDER BY i.created_at
`

// ListInvitationsByClan returns the clan's pending invitations (with the
// invited player's login).
func (r *Repository) ListInvitationsByClan(ctx context.Context, clanID domain.ClanID) ([]InvitationView, error) {
	return r.scanInvitations(ctx, listInvitationsByClanSQL, int64(clanID))
}

const listInvitationsByPlayerSQL = `
SELECT i.clan_id, c.name, c.tag, i.player_id, p.login, i.invited_by, i.created_at
FROM clan_invitations i
JOIN clans c ON c.id = i.clan_id
JOIN players p ON p.id = i.player_id
WHERE i.player_id = $1
ORDER BY i.created_at
`

// ListInvitationsByPlayer returns the player's pending invitations (with the
// clan name/tag) so the SPA can offer an Accept action.
func (r *Repository) ListInvitationsByPlayer(ctx context.Context, player domain.PlayerID) ([]InvitationView, error) {
	return r.scanInvitations(ctx, listInvitationsByPlayerSQL, int64(player))
}

func (r *Repository) scanInvitations(ctx context.Context, sql string, arg int64) ([]InvitationView, error) {
	rows, err := r.exec.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("query invitations: %w", err)
	}
	defer rows.Close()

	var out []InvitationView
	for rows.Next() {
		var iv InvitationView
		var cid, pid, by int64
		if err := rows.Scan(&cid, &iv.ClanName, &iv.ClanTag, &pid, &iv.Login, &by, &iv.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan invitation: %w", err)
		}
		iv.ClanID = domain.ClanID(cid)
		iv.PlayerID = domain.PlayerID(pid)
		iv.InvitedBy = domain.PlayerID(by)
		out = append(out, iv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invitations: %w", err)
	}
	return out, nil
}
