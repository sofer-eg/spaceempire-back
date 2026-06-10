package clans_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/social/clans"
)

// fakeRepo is an in-memory model of clans.Repo with the same semantics as
// the real Postgres-backed Repository (one clan per player, atomic
// create/accept, unique name/tag), so service-level permission and flow
// tests exercise real behaviour without a DB.
type fakeRepo struct {
	clans   map[domain.ClanID]clans.Clan
	members map[domain.PlayerID]clans.Membership
	invites map[invKey]bool
	logins  map[domain.PlayerID]string
	nextID  domain.ClanID
}

type invKey struct {
	clan   domain.ClanID
	player domain.PlayerID
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		clans:   map[domain.ClanID]clans.Clan{},
		members: map[domain.PlayerID]clans.Membership{},
		invites: map[invKey]bool{},
		logins:  map[domain.PlayerID]string{},
		nextID:  0,
	}
}

func (f *fakeRepo) CreateClanWithLeader(_ context.Context, name, tag string, leader domain.PlayerID) (domain.ClanID, error) {
	for _, c := range f.clans {
		if c.Name == name {
			return 0, clans.ErrNameTaken
		}
		if c.Tag == tag {
			return 0, clans.ErrTagTaken
		}
	}
	if _, ok := f.members[leader]; ok {
		return 0, clans.ErrAlreadyInClan
	}
	f.nextID++
	id := f.nextID
	f.clans[id] = clans.Clan{ID: id, Name: name, Tag: tag, LeaderID: leader}
	f.members[leader] = clans.Membership{PlayerID: leader, ClanID: id, Role: clans.RoleLeader}
	return id, nil
}

func (f *fakeRepo) AcceptInvitation(_ context.Context, clanID domain.ClanID, player domain.PlayerID) error {
	if !f.invites[invKey{clanID, player}] {
		return clans.ErrInvitationNotFound
	}
	if _, ok := f.members[player]; ok {
		return clans.ErrAlreadyInClan
	}
	delete(f.invites, invKey{clanID, player})
	f.members[player] = clans.Membership{PlayerID: player, ClanID: clanID, Role: clans.RoleMember}
	return nil
}

func (f *fakeRepo) GetMembership(_ context.Context, player domain.PlayerID) (clans.Membership, error) {
	m, ok := f.members[player]
	if !ok {
		return clans.Membership{}, clans.ErrNotMember
	}
	return m, nil
}

func (f *fakeRepo) DeleteMember(_ context.Context, player domain.PlayerID) error {
	delete(f.members, player)
	return nil
}

func (f *fakeRepo) SetMemberRole(_ context.Context, clanID domain.ClanID, target domain.PlayerID, role string) error {
	if m, ok := f.members[target]; ok && m.ClanID == clanID {
		m.Role = role
		f.members[target] = m
	}
	return nil
}

func (f *fakeRepo) DeleteClan(_ context.Context, clanID domain.ClanID) error {
	delete(f.clans, clanID)
	for p, m := range f.members {
		if m.ClanID == clanID {
			delete(f.members, p)
		}
	}
	return nil
}

func (f *fakeRepo) CountMembers(_ context.Context, clanID domain.ClanID) (int, error) {
	n := 0
	for _, m := range f.members {
		if m.ClanID == clanID {
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) CreateInvitation(_ context.Context, clanID domain.ClanID, target, _ domain.PlayerID) error {
	if f.invites[invKey{clanID, target}] {
		return clans.ErrAlreadyInvited
	}
	f.invites[invKey{clanID, target}] = true
	return nil
}

func (f *fakeRepo) GetClan(_ context.Context, clanID domain.ClanID) (clans.Clan, error) {
	c, ok := f.clans[clanID]
	if !ok {
		return clans.Clan{}, clans.ErrClanNotFound
	}
	return c, nil
}

func (f *fakeRepo) ListClans(_ context.Context) ([]clans.ClanSummary, error) {
	var out []clans.ClanSummary
	for id, c := range f.clans {
		n, _ := f.CountMembers(context.Background(), id)
		out = append(out, clans.ClanSummary{Clan: c, MemberCount: n})
	}
	return out, nil
}

func (f *fakeRepo) ListMembers(_ context.Context, clanID domain.ClanID) ([]clans.MemberView, error) {
	var out []clans.MemberView
	for p, m := range f.members {
		if m.ClanID == clanID {
			out = append(out, clans.MemberView{PlayerID: p, Login: f.logins[p], Role: m.Role})
		}
	}
	return out, nil
}

func (f *fakeRepo) ListInvitationsByClan(_ context.Context, clanID domain.ClanID) ([]clans.InvitationView, error) {
	var out []clans.InvitationView
	for k := range f.invites {
		if k.clan == clanID {
			out = append(out, clans.InvitationView{ClanID: k.clan, PlayerID: k.player, Login: f.logins[k.player]})
		}
	}
	return out, nil
}

func (f *fakeRepo) ListInvitationsByPlayer(_ context.Context, player domain.PlayerID) ([]clans.InvitationView, error) {
	var out []clans.InvitationView
	for k := range f.invites {
		if k.player == player {
			c := f.clans[k.clan]
			out = append(out, clans.InvitationView{ClanID: k.clan, ClanName: c.Name, ClanTag: c.Tag, PlayerID: player})
		}
	}
	return out, nil
}

func newService(t *testing.T) (*clans.Service, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo()
	return clans.NewService(repo), repo
}

const (
	leaderID = domain.PlayerID(1)
	memberID = domain.PlayerID(2)
	outsider = domain.PlayerID(3)
)

// seedClan creates a clan led by leaderID with memberID as a plain member.
func seedClan(t *testing.T, svc *clans.Service, repo *fakeRepo) domain.ClanID {
	t.Helper()
	ctx := context.Background()
	clan, err := svc.Create(ctx, leaderID, "Argon Federation", "AGN")
	require.NoError(t, err)
	repo.invites[invKey{clan.ID, memberID}] = true
	require.NoError(t, svc.Accept(ctx, memberID, clan.ID))
	return clan.ID
}

func TestUnit_Clans_Create_Success(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)

	clan, err := svc.Create(context.Background(), leaderID, "Argon Federation", "AGN")
	require.NoError(t, err)
	assert.Equal(t, "Argon Federation", clan.Name)
	assert.Equal(t, leaderID, clan.LeaderID)

	m := repo.members[leaderID]
	assert.Equal(t, clan.ID, m.ClanID)
	assert.Equal(t, clans.RoleLeader, m.Role)
}

func TestUnit_Clans_Create_Validation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, clanName, tag string
	}{
		{"short name", "ab", "AGN"},
		{"empty name", "   ", "AGN"},
		{"short tag", "Argon Federation", "A"},
		{"long tag", "Argon Federation", "TOOLONG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newService(t)
			_, err := svc.Create(context.Background(), leaderID, tt.clanName, tt.tag)
			assert.ErrorIs(t, err, clans.ErrInvalidInput)
		})
	}
}

func TestUnit_Clans_Create_AlreadyInClan(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, leaderID, "Argon Federation", "AGN")
	require.NoError(t, err)

	_, err = svc.Create(ctx, leaderID, "Boron Kingdom", "BRN")
	assert.ErrorIs(t, err, clans.ErrAlreadyInClan)
}

func TestUnit_Clans_Create_NameAndTagTaken(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx := context.Background()
	_, err := svc.Create(ctx, leaderID, "Argon Federation", "AGN")
	require.NoError(t, err)

	_, err = svc.Create(ctx, outsider, "Argon Federation", "XXX")
	assert.ErrorIs(t, err, clans.ErrNameTaken)
	_, err = svc.Create(ctx, outsider, "Other Name", "AGN")
	assert.ErrorIs(t, err, clans.ErrTagTaken)
}

func TestUnit_Clans_Invite_Success(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	require.NoError(t, svc.Invite(context.Background(), leaderID, clanID, outsider))
	assert.True(t, repo.invites[invKey{clanID, outsider}])
}

func TestUnit_Clans_Invite_ByMemberForbidden(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	// memberID is a plain member — cannot invite.
	err := svc.Invite(context.Background(), memberID, clanID, outsider)
	assert.ErrorIs(t, err, clans.ErrForbidden)
}

func TestUnit_Clans_Invite_ByNonMember(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.Invite(context.Background(), outsider, clanID, domain.PlayerID(9))
	assert.ErrorIs(t, err, clans.ErrNotMember)
}

func TestUnit_Clans_Invite_TargetAlreadyInClan(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	// memberID already belongs to the clan.
	err := svc.Invite(context.Background(), leaderID, clanID, memberID)
	assert.ErrorIs(t, err, clans.ErrAlreadyInClan)
}

func TestUnit_Clans_Accept_NoInvitation(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.Accept(context.Background(), outsider, clanID)
	assert.ErrorIs(t, err, clans.ErrInvitationNotFound)
}

func TestUnit_Clans_Kick_Success(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	require.NoError(t, svc.Kick(context.Background(), leaderID, clanID, memberID))
	_, ok := repo.members[memberID]
	assert.False(t, ok, "member should be removed")
}

func TestUnit_Clans_Kick_ByMemberForbidden(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.Kick(context.Background(), memberID, clanID, leaderID)
	assert.ErrorIs(t, err, clans.ErrForbidden)
}

func TestUnit_Clans_Kick_CannotKickLeader(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.Kick(context.Background(), leaderID, clanID, leaderID)
	assert.ErrorIs(t, err, clans.ErrCannotKickLeader)
}

func TestUnit_Clans_Kick_TargetNotMember(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.Kick(context.Background(), leaderID, clanID, outsider)
	assert.ErrorIs(t, err, clans.ErrTargetNotMember)
}

func TestUnit_Clans_SetRole_PromoteAndDemote(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	ctx := context.Background()
	clanID := seedClan(t, svc, repo)

	require.NoError(t, svc.SetRole(ctx, leaderID, clanID, memberID, clans.RoleOfficer))
	assert.Equal(t, clans.RoleOfficer, repo.members[memberID].Role)

	require.NoError(t, svc.SetRole(ctx, leaderID, clanID, memberID, clans.RoleMember))
	assert.Equal(t, clans.RoleMember, repo.members[memberID].Role)
}

func TestUnit_Clans_SetRole_ByNonLeaderForbidden(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	// member promotes themselves → forbidden (only the leader sets roles).
	err := svc.SetRole(context.Background(), memberID, clanID, memberID, clans.RoleOfficer)
	assert.ErrorIs(t, err, clans.ErrForbidden)
}

func TestUnit_Clans_SetRole_CannotChangeLeader(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.SetRole(context.Background(), leaderID, clanID, leaderID, clans.RoleMember)
	assert.ErrorIs(t, err, clans.ErrCannotChangeLeader)
}

func TestUnit_Clans_SetRole_InvalidRole(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.SetRole(context.Background(), leaderID, clanID, memberID, clans.RoleLeader)
	assert.ErrorIs(t, err, clans.ErrInvalidRole)
}

func TestUnit_Clans_SetRole_OfficerThenManages(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	ctx := context.Background()
	clanID := seedClan(t, svc, repo)
	// A third plain member to be kicked by the promoted officer.
	const extra = domain.PlayerID(40)
	repo.members[extra] = clans.Membership{PlayerID: extra, ClanID: clanID, Role: clans.RoleMember}

	require.NoError(t, svc.SetRole(ctx, leaderID, clanID, memberID, clans.RoleOfficer))
	// The newly-promoted officer can now kick (canManage).
	require.NoError(t, svc.Kick(ctx, memberID, clanID, extra))
	_, ok := repo.members[extra]
	assert.False(t, ok, "officer kicked the plain member")
}

func TestUnit_Clans_Leave_Member(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	require.NoError(t, svc.Leave(context.Background(), memberID, clanID))
	_, ok := repo.members[memberID]
	assert.False(t, ok)
}

func TestUnit_Clans_Leave_LeaderWithMembers(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	err := svc.Leave(context.Background(), leaderID, clanID)
	assert.ErrorIs(t, err, clans.ErrLeaderMustTransfer)
}

func TestUnit_Clans_Leave_LeaderAloneDisbands(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	ctx := context.Background()
	clan, err := svc.Create(ctx, leaderID, "Solo Clan", "SOLO")
	require.NoError(t, err)

	require.NoError(t, svc.Leave(ctx, leaderID, clan.ID))
	_, ok := repo.clans[clan.ID]
	assert.False(t, ok, "clan should be disbanded when the last member (leader) leaves")
	_, ok = repo.members[leaderID]
	assert.False(t, ok)
}

func TestUnit_Clans_MyClan(t *testing.T) {
	t.Parallel()
	svc, repo := newService(t)
	clanID := seedClan(t, svc, repo)

	detail, err := svc.MyClan(context.Background(), memberID)
	require.NoError(t, err)
	require.NotNil(t, detail)
	assert.Equal(t, clanID, detail.Clan.ID)

	none, err := svc.MyClan(context.Background(), outsider)
	require.NoError(t, err)
	assert.Nil(t, none, "a player in no clan returns nil detail")
}
