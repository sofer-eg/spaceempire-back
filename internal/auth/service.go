package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"spaceempire/back/internal/domain"
)

// Repo abstracts the persistence ops Service needs. Defined in the consumer
// package per ISP so tests can stub it without pulling pgx in.
type Repo interface {
	CreatePlayer(ctx context.Context, login string, passwordHash []byte, race domain.RaceID) (domain.PlayerID, error)
	GetPlayerByLogin(ctx context.Context, login string) (Player, error)
	GetPlayerByID(ctx context.Context, id domain.PlayerID) (Player, error)
	ListPlayers(ctx context.Context) ([]Player, error)
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, token string) (Session, error)
	DeleteSession(ctx context.Context, token string) error
}

// Clock returns the current time. Injected so tests can freeze it for
// session-expiry assertions.
type Clock interface {
	Now() time.Time
}

// ShipSpawner creates the starter ship for a freshly registered player.
// The real implementation lives in the spawner adapter wired by app/.
// Passing nil to NewService disables auto-spawn (used in pure auth tests).
type ShipSpawner interface {
	SpawnFor(ctx context.Context, playerID domain.PlayerID) error
}

// ServiceConfig holds the few knobs Service needs.
type ServiceConfig struct {
	// SessionTTL is how long a freshly issued session stays valid.
	SessionTTL time.Duration
	// BcryptCost is the bcrypt work factor. 0 → bcrypt.DefaultCost.
	BcryptCost int
}

// Service implements registration, login, logout, and session lookup.
type Service struct {
	repo    Repo
	clock   Clock
	spawner ShipSpawner
	cfg     ServiceConfig
}

// NewService wires a Service. SessionTTL must be > 0; BcryptCost falls back
// to bcrypt.DefaultCost when zero. spawner may be nil — if so, Register
// skips the ship-creation step.
func NewService(repo Repo, clock Clock, spawner ShipSpawner, cfg ServiceConfig) *Service {
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	return &Service{repo: repo, clock: clock, spawner: spawner, cfg: cfg}
}

// Register creates the player with the chosen race and issues a fresh
// session. Returns ErrLoginTaken if the login is already in use. The race
// must be validated by the caller (RegisterRequest.Validate restricts it to
// the playable 1..5); it drives the starter ship's home shipyard / race /
// name via the spawner (phase 10.10).
func (s *Service) Register(ctx context.Context, login, password string, race domain.RaceID) (domain.PlayerID, Session, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cfg.BcryptCost)
	if err != nil {
		return 0, Session{}, fmt.Errorf("hash password: %w", err)
	}

	playerID, err := s.repo.CreatePlayer(ctx, login, hash, race)
	if err != nil {
		return 0, Session{}, err
	}

	if s.spawner != nil {
		if err := s.spawner.SpawnFor(ctx, playerID); err != nil {
			return 0, Session{}, fmt.Errorf("spawn ship: %w", err)
		}
	}

	session, err := s.issueSession(ctx, playerID)
	if err != nil {
		return 0, Session{}, err
	}
	return playerID, session, nil
}

// Login verifies credentials and issues a session.
// Returns ErrInvalidCredentials for any mismatch (unknown login or wrong
// password) — both branches surface as 401 to avoid leaking which side
// failed.
func (s *Service) Login(ctx context.Context, login, password string) (Player, Session, error) {
	p, err := s.repo.GetPlayerByLogin(ctx, login)
	if errors.Is(err, ErrPlayerNotFound) {
		return Player{}, Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Player{}, Session{}, err
	}
	if err := bcrypt.CompareHashAndPassword(p.PasswordHash, []byte(password)); err != nil {
		return Player{}, Session{}, ErrInvalidCredentials
	}
	session, err := s.issueSession(ctx, p.ID)
	if err != nil {
		return Player{}, Session{}, err
	}
	return p, session, nil
}

// ListPlayers proxies repo.ListPlayers for the players-endpoint handler.
func (s *Service) ListPlayers(ctx context.Context) ([]Player, error) {
	return s.repo.ListPlayers(ctx)
}

// GetByID proxies repo.GetPlayerByID for the player-self handler.
func (s *Service) GetByID(ctx context.Context, id domain.PlayerID) (Player, error) {
	return s.repo.GetPlayerByID(ctx, id)
}

// Logout removes the session by token. Always idempotent.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.repo.DeleteSession(ctx, token)
}

// Authenticate looks up the player behind a session token.
// Returns ErrNotAuthenticated when the token is missing, unknown, or expired.
func (s *Service) Authenticate(ctx context.Context, token string) (Player, error) {
	if token == "" {
		return Player{}, ErrNotAuthenticated
	}
	session, err := s.repo.GetSession(ctx, token)
	if errors.Is(err, ErrSessionNotFound) {
		return Player{}, ErrNotAuthenticated
	}
	if err != nil {
		return Player{}, err
	}
	p, err := s.repo.GetPlayerByID(ctx, session.PlayerID)
	if errors.Is(err, ErrPlayerNotFound) {
		// Session points at a player that no longer exists (e.g. deleted
		// while the cookie was still valid). Treat as not authenticated.
		return Player{}, ErrNotAuthenticated
	}
	if err != nil {
		return Player{}, err
	}
	return p, nil
}

func (s *Service) issueSession(ctx context.Context, playerID domain.PlayerID) (Session, error) {
	token, err := newSessionToken()
	if err != nil {
		return Session{}, err
	}
	session := Session{
		Token:     token,
		PlayerID:  playerID,
		ExpiresAt: s.clock.Now().Add(s.cfg.SessionTTL),
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		return Session{}, err
	}
	return session, nil
}

// newSessionToken returns 32 random bytes encoded as hex (64 chars).
// crypto/rand.Read is documented to either fill the buffer or return an
// error, so a short read is impossible.
func newSessionToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
