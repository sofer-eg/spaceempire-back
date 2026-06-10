package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/pkg/database/testdb"
)

func TestIntegration_Repository_CreatePlayer_RoundTrip(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := auth.NewRepository(pool)
	ctx := context.Background()

	id, err := repo.CreatePlayer(ctx, "sofer", []byte("hash-stub"), 1)
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	if id == 0 {
		t.Fatal("id = 0")
	}

	p, err := repo.GetPlayerByLogin(ctx, "sofer")
	if err != nil {
		t.Fatalf("GetPlayerByLogin: %v", err)
	}
	if p.ID != id || p.Login != "sofer" || string(p.PasswordHash) != "hash-stub" {
		t.Fatalf("unexpected player = %+v", p)
	}

	p2, err := repo.GetPlayerByID(ctx, id)
	if err != nil {
		t.Fatalf("GetPlayerByID: %v", err)
	}
	if p2.Login != "sofer" {
		t.Fatalf("p2.Login = %q, want sofer", p2.Login)
	}
}

func TestIntegration_Repository_CreatePlayer_DuplicateReturnsErrLoginTaken(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := auth.NewRepository(pool)
	ctx := context.Background()

	if _, err := repo.CreatePlayer(ctx, "sofer", []byte("h"), 1); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := repo.CreatePlayer(ctx, "sofer", []byte("h"), 1)
	if !errors.Is(err, auth.ErrLoginTaken) {
		t.Fatalf("err = %v, want ErrLoginTaken", err)
	}
}

func TestIntegration_Repository_GetPlayerByLogin_NotFound(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := auth.NewRepository(pool)

	_, err := repo.GetPlayerByLogin(context.Background(), "ghost")
	if !errors.Is(err, auth.ErrPlayerNotFound) {
		t.Fatalf("err = %v, want ErrPlayerNotFound", err)
	}
}

func TestIntegration_Repository_Session_LifecycleAndExpiry(t *testing.T) {
	t.Parallel()

	pool := testdb.Setup(t)
	repo := auth.NewRepository(pool)
	ctx := context.Background()

	pid, err := repo.CreatePlayer(ctx, "sofer", []byte("h"), 1)
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}

	// fresh session
	live := auth.Session{Token: "tok-live", PlayerID: pid, ExpiresAt: time.Now().Add(time.Hour)}
	if err := repo.CreateSession(ctx, live); err != nil {
		t.Fatalf("CreateSession live: %v", err)
	}
	got, err := repo.GetSession(ctx, "tok-live")
	if err != nil {
		t.Fatalf("GetSession live: %v", err)
	}
	if got.PlayerID != pid {
		t.Fatalf("got.PlayerID = %d, want %d", got.PlayerID, pid)
	}

	// expired session — repo's WHERE expires_at > NOW() must hide it.
	expired := auth.Session{Token: "tok-expired", PlayerID: pid, ExpiresAt: time.Now().Add(-time.Minute)}
	if err := repo.CreateSession(ctx, expired); err != nil {
		t.Fatalf("CreateSession expired: %v", err)
	}
	if _, err := repo.GetSession(ctx, "tok-expired"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("expired GetSession err = %v, want ErrSessionNotFound", err)
	}

	// delete live → not found
	if err := repo.DeleteSession(ctx, "tok-live"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := repo.GetSession(ctx, "tok-live"); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("post-delete GetSession err = %v, want ErrSessionNotFound", err)
	}

	// delete non-existent → no error (idempotent)
	if err := repo.DeleteSession(ctx, "no-such"); err != nil {
		t.Fatalf("DeleteSession unknown: %v", err)
	}
}
