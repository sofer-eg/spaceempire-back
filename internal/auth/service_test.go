package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"spaceempire/back/internal/auth"
	"spaceempire/back/internal/domain"
)

// stubRepo is an in-memory Repo for service-level unit tests.
type stubRepo struct {
	mu        sync.Mutex
	players   map[string]auth.Player // by login
	byID      map[domain.PlayerID]auth.Player
	sessions  map[string]auth.Session
	nextID    int64
	createErr error
}

func newStubRepo() *stubRepo {
	return &stubRepo{
		players:  make(map[string]auth.Player),
		byID:     make(map[domain.PlayerID]auth.Player),
		sessions: make(map[string]auth.Session),
	}
}

func (s *stubRepo) CreatePlayer(_ context.Context, login string, hash []byte, race domain.RaceID) (domain.PlayerID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return 0, s.createErr
	}
	if _, ok := s.players[login]; ok {
		return 0, auth.ErrLoginTaken
	}
	s.nextID++
	p := auth.Player{ID: domain.PlayerID(s.nextID), Login: login, PasswordHash: hash, CreatedAt: time.Now(), Race: race}
	s.players[login] = p
	s.byID[p.ID] = p
	return p.ID, nil
}

func (s *stubRepo) GetPlayerByLogin(_ context.Context, login string) (auth.Player, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.players[login]
	if !ok {
		return auth.Player{}, auth.ErrPlayerNotFound
	}
	return p, nil
}

func (s *stubRepo) GetPlayerByID(_ context.Context, id domain.PlayerID) (auth.Player, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[id]
	if !ok {
		return auth.Player{}, auth.ErrPlayerNotFound
	}
	return p, nil
}

func (s *stubRepo) ListPlayers(_ context.Context) ([]auth.Player, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]auth.Player, 0, len(s.byID))
	for _, p := range s.byID {
		out = append(out, p)
	}
	return out, nil
}

func (s *stubRepo) CreateSession(_ context.Context, sess auth.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.Token] = sess
	return nil
}

func (s *stubRepo) GetSession(_ context.Context, token string) (auth.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return auth.Session{}, auth.ErrSessionNotFound
	}
	return sess, nil
}

func (s *stubRepo) DeleteSession(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
	return nil
}

// fixedClock returns a constant time so tests can assert expires_at exactly.
type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

func newSvc(t *testing.T) (*auth.Service, *stubRepo, time.Time) {
	t.Helper()
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	repo := newStubRepo()
	svc := auth.NewService(repo, fixedClock{t: now}, nil, auth.ServiceConfig{
		SessionTTL: time.Hour,
		BcryptCost: bcrypt.MinCost, // keep tests fast
	})
	return svc, repo, now
}

func TestUnit_Register_IssuesSessionWithExpectedExpiry(t *testing.T) {
	t.Parallel()

	svc, _, now := newSvc(t)

	pid, sess, err := svc.Register(context.Background(), "sofer", "1", 1)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if pid == 0 {
		t.Fatal("playerID = 0, want > 0")
	}
	if len(sess.Token) != 64 {
		t.Fatalf("token length = %d, want 64 (hex of 32 bytes)", len(sess.Token))
	}
	if sess.PlayerID != pid {
		t.Fatalf("session.PlayerID = %d, want %d", sess.PlayerID, pid)
	}
	if !sess.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("ExpiresAt = %v, want %v", sess.ExpiresAt, now.Add(time.Hour))
	}
}

func TestUnit_Register_DuplicateLoginReturnsErrLoginTaken(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)

	if _, _, err := svc.Register(context.Background(), "sofer", "1", 1); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	_, _, err := svc.Register(context.Background(), "sofer", "1", 1)
	if !errors.Is(err, auth.ErrLoginTaken) {
		t.Fatalf("err = %v, want ErrLoginTaken", err)
	}
}

func TestUnit_Login_VerifiesPassword(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)
	if _, _, err := svc.Register(context.Background(), "sofer", "secret", 1); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p, sess, err := svc.Login(context.Background(), "sofer", "secret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if p.Login != "sofer" {
		t.Fatalf("player.Login = %q, want sofer", p.Login)
	}
	if sess.Token == "" {
		t.Fatal("login produced empty token")
	}
}

func TestUnit_Login_WrongPasswordReturnsInvalidCredentials(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)
	_, _, _ = svc.Register(context.Background(), "sofer", "secret", 1)

	_, _, err := svc.Login(context.Background(), "sofer", "wrong")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestUnit_Login_UnknownLoginReturnsInvalidCredentials(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)

	_, _, err := svc.Login(context.Background(), "ghost", "1")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestUnit_Authenticate_ValidToken(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)
	pid, sess, _ := svc.Register(context.Background(), "sofer", "1", 1)

	p, err := svc.Authenticate(context.Background(), sess.Token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.ID != pid {
		t.Fatalf("Authenticate returned id %d, want %d", p.ID, pid)
	}
}

func TestUnit_Authenticate_EmptyTokenReturnsNotAuthenticated(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)

	_, err := svc.Authenticate(context.Background(), "")
	if !errors.Is(err, auth.ErrNotAuthenticated) {
		t.Fatalf("err = %v, want ErrNotAuthenticated", err)
	}
}

func TestUnit_Authenticate_UnknownTokenReturnsNotAuthenticated(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)

	_, err := svc.Authenticate(context.Background(), "no-such-token")
	if !errors.Is(err, auth.ErrNotAuthenticated) {
		t.Fatalf("err = %v, want ErrNotAuthenticated", err)
	}
}

func TestUnit_Logout_RemovesSession(t *testing.T) {
	t.Parallel()

	svc, repo, _ := newSvc(t)
	_, sess, _ := svc.Register(context.Background(), "sofer", "1", 1)

	if err := svc.Logout(context.Background(), sess.Token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if _, ok := repo.sessions[sess.Token]; ok {
		t.Fatal("session still present after Logout")
	}
}

func TestUnit_Logout_EmptyTokenIsNoOp(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSvc(t)
	if err := svc.Logout(context.Background(), ""); err != nil {
		t.Fatalf("Logout empty: %v", err)
	}
}
