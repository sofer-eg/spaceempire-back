package clans_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"spaceempire/back/internal/domain"
	"spaceempire/back/internal/pkg/database/testdb"
	"spaceempire/back/internal/social/clans"
)

func seedPlayer(t *testing.T, pool *pgxpool.Pool, login string) domain.PlayerID {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO players (login, password_hash) VALUES ($1, 'h') RETURNING id`, login).Scan(&id)
	require.NoError(t, err)
	return domain.PlayerID(id)
}

// TestIntegration_Clans_FullCycle exercises create → invite → accept → leave
// against real Postgres, asserting the CTE-based create/accept land both the
// clan/leader and the invite-consume/member-add atomically.
func TestIntegration_Clans_FullCycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := clans.NewRepository(pool)

	leader := seedPlayer(t, pool, "leader")
	joiner := seedPlayer(t, pool, "joiner")

	clanID, err := repo.CreateClanWithLeader(ctx, "Argon Federation", "AGN", leader)
	require.NoError(t, err)

	lm, err := repo.GetMembership(ctx, leader)
	require.NoError(t, err)
	assert.Equal(t, clanID, lm.ClanID)
	assert.Equal(t, clans.RoleLeader, lm.Role)

	require.NoError(t, repo.CreateInvitation(ctx, clanID, joiner, leader))
	require.NoError(t, repo.AcceptInvitation(ctx, clanID, joiner))

	jm, err := repo.GetMembership(ctx, joiner)
	require.NoError(t, err)
	assert.Equal(t, clans.RoleMember, jm.Role)

	members, err := repo.ListMembers(ctx, clanID)
	require.NoError(t, err)
	assert.Len(t, members, 2)

	// invitation was consumed
	invs, err := repo.ListInvitationsByClan(ctx, clanID)
	require.NoError(t, err)
	assert.Empty(t, invs)

	// joiner leaves
	require.NoError(t, repo.DeleteMember(ctx, joiner))
	n, err := repo.CountMembers(ctx, clanID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestIntegration_Clans_NameAndTagTaken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := clans.NewRepository(pool)

	a := seedPlayer(t, pool, "a")
	b := seedPlayer(t, pool, "b")
	c := seedPlayer(t, pool, "c")

	_, err := repo.CreateClanWithLeader(ctx, "Argon Federation", "AGN", a)
	require.NoError(t, err)

	_, err = repo.CreateClanWithLeader(ctx, "Argon Federation", "XXX", b)
	assert.ErrorIs(t, err, clans.ErrNameTaken)

	_, err = repo.CreateClanWithLeader(ctx, "Other Name", "AGN", c)
	assert.ErrorIs(t, err, clans.ErrTagTaken)
}

func TestIntegration_Clans_CreateAlreadyInClan(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := clans.NewRepository(pool)

	leader := seedPlayer(t, pool, "leader")
	_, err := repo.CreateClanWithLeader(ctx, "Argon Federation", "AGN", leader)
	require.NoError(t, err)

	// Same player founding a second clan hits clan_members PK → ErrAlreadyInClan,
	// and the would-be clan row is rolled back by the CTE.
	_, err = repo.CreateClanWithLeader(ctx, "Boron Kingdom", "BRN", leader)
	assert.ErrorIs(t, err, clans.ErrAlreadyInClan)

	clansList, err := repo.ListClans(ctx)
	require.NoError(t, err)
	assert.Len(t, clansList, 1, "second clan must not be left behind")
}

func TestIntegration_Clans_Accept_Errors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := clans.NewRepository(pool)

	leader := seedPlayer(t, pool, "leader")
	joiner := seedPlayer(t, pool, "joiner")
	clanID, err := repo.CreateClanWithLeader(ctx, "Argon Federation", "AGN", leader)
	require.NoError(t, err)

	// No invitation yet.
	assert.ErrorIs(t, repo.AcceptInvitation(ctx, clanID, joiner), clans.ErrInvitationNotFound)

	// Joiner already in another clan → accept hits member PK.
	other := seedPlayer(t, pool, "other")
	otherClan, err := repo.CreateClanWithLeader(ctx, "Boron Kingdom", "BRN", other)
	require.NoError(t, err)
	require.NoError(t, repo.CreateInvitation(ctx, clanID, other, leader))
	_ = otherClan
	assert.ErrorIs(t, repo.AcceptInvitation(ctx, clanID, other), clans.ErrAlreadyInClan)
}

func TestIntegration_Clans_LeaderDeleteCascades(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.Setup(t)
	repo := clans.NewRepository(pool)

	leader := seedPlayer(t, pool, "leader")
	clanID, err := repo.CreateClanWithLeader(ctx, "Argon Federation", "AGN", leader)
	require.NoError(t, err)

	// Deleting the leader player cascades to the clan (clans.leader_id ON
	// DELETE CASCADE) and its memberships/invitations.
	_, err = pool.Exec(ctx, `DELETE FROM players WHERE id = $1`, int64(leader))
	require.NoError(t, err)

	_, err = repo.GetClan(ctx, clanID)
	assert.ErrorIs(t, err, clans.ErrClanNotFound)
}
