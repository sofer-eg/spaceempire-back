package auth

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

// pgUniqueViolation is the SQLSTATE for unique_violation. Hard-coded to avoid
// pulling jackc/pgerrcode in for a single constant.
const pgUniqueViolation = "23505"

// Player is the persisted player row used by Service.
type Player struct {
	ID           domain.PlayerID
	Login        string
	PasswordHash []byte
	CreatedAt    time.Time
	// Race is the faction the player picked at registration (1..5 playable).
	// 0 = unset (system __npc__ / legacy rows). Drives the starter ship's
	// home shipyard / race / name (phase 10.10).
	Race domain.RaceID
}

// Session is a persisted session row tying a token to a player.
type Session struct {
	Token     string
	PlayerID  domain.PlayerID
	ExpiresAt time.Time
}

// Repository talks to the players/sessions tables via an Executor (pool or tx).
type Repository struct {
	exec database.Executor
}

// NewRepository wires a Repository to the given executor.
func NewRepository(exec database.Executor) *Repository {
	return &Repository{exec: exec}
}

// CreatePlayer inserts a new player and returns its ID. On unique-violation
// of players.login it returns ErrLoginTaken.
func (r *Repository) CreatePlayer(ctx context.Context, login string, passwordHash []byte, race domain.RaceID) (domain.PlayerID, error) {
	const q = `INSERT INTO players (login, password_hash, race) VALUES ($1, $2, $3) RETURNING id`
	var id int64
	if err := r.exec.QueryRow(ctx, q, login, string(passwordHash), int16(race)).Scan(&id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return 0, ErrLoginTaken
		}
		return 0, fmt.Errorf("insert player: %w", err)
	}
	return domain.PlayerID(id), nil
}

// GetPlayerByLogin returns the player with the given login.
// Returns ErrPlayerNotFound when no row matches.
func (r *Repository) GetPlayerByLogin(ctx context.Context, login string) (Player, error) {
	const q = `SELECT id, login, password_hash, created_at, race FROM players WHERE login = $1`
	var p Player
	var id int64
	var hash string
	var race int16
	err := r.exec.QueryRow(ctx, q, login).Scan(&id, &p.Login, &hash, &p.CreatedAt, &race)
	if errors.Is(err, pgx.ErrNoRows) {
		return Player{}, ErrPlayerNotFound
	}
	if err != nil {
		return Player{}, fmt.Errorf("select player by login: %w", err)
	}
	p.ID = domain.PlayerID(id)
	p.PasswordHash = []byte(hash)
	p.Race = domain.RaceID(race)
	return p, nil
}

// GetPlayerByID returns the player with the given ID.
// Returns ErrPlayerNotFound when no row matches.
func (r *Repository) GetPlayerByID(ctx context.Context, id domain.PlayerID) (Player, error) {
	const q = `SELECT id, login, password_hash, created_at, race FROM players WHERE id = $1`
	var p Player
	var pid int64
	var hash string
	var race int16
	err := r.exec.QueryRow(ctx, q, int64(id)).Scan(&pid, &p.Login, &hash, &p.CreatedAt, &race)
	if errors.Is(err, pgx.ErrNoRows) {
		return Player{}, ErrPlayerNotFound
	}
	if err != nil {
		return Player{}, fmt.Errorf("select player by id: %w", err)
	}
	p.ID = domain.PlayerID(pid)
	p.PasswordHash = []byte(hash)
	p.Race = domain.RaceID(race)
	return p, nil
}

// PlayerRace returns the player's chosen race. The ship spawner uses it to
// pick the home shipyard and the starter ship's race/name (phase 10.10).
// Returns ErrPlayerNotFound when no row matches.
func (r *Repository) PlayerRace(ctx context.Context, id domain.PlayerID) (domain.RaceID, error) {
	const q = `SELECT race FROM players WHERE id = $1`
	var race int16
	err := r.exec.QueryRow(ctx, q, int64(id)).Scan(&race)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrPlayerNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("select player race: %w", err)
	}
	return domain.RaceID(race), nil
}

// CreateSession persists a new session token.
func (r *Repository) CreateSession(ctx context.Context, s Session) error {
	const q = `INSERT INTO sessions (token, player_id, expires_at) VALUES ($1, $2, $3)`
	if _, err := r.exec.Exec(ctx, q, s.Token, int64(s.PlayerID), s.ExpiresAt); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// GetSession returns a non-expired session by token. Expired sessions are
// reported as ErrSessionNotFound so callers do not need a second branch.
func (r *Repository) GetSession(ctx context.Context, token string) (Session, error) {
	const q = `SELECT token, player_id, expires_at FROM sessions WHERE token = $1 AND expires_at > NOW()`
	var s Session
	var pid int64
	err := r.exec.QueryRow(ctx, q, token).Scan(&s.Token, &pid, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("select session: %w", err)
	}
	s.PlayerID = domain.PlayerID(pid)
	return s, nil
}

const listPlayersSQL = `SELECT id, login FROM players ORDER BY id`

// ListPlayers returns id+login for every player in the database, ordered
// by id ascending. Used by GET /api/players so the SPA can resolve owner
// nicknames for ships received via WS deltas.
func (r *Repository) ListPlayers(ctx context.Context) ([]Player, error) {
	rows, err := r.exec.Query(ctx, listPlayersSQL)
	if err != nil {
		return nil, fmt.Errorf("query players: %w", err)
	}
	defer rows.Close()

	var out []Player
	for rows.Next() {
		var (
			id    int64
			login string
		)
		if err := rows.Scan(&id, &login); err != nil {
			return nil, fmt.Errorf("scan player: %w", err)
		}
		out = append(out, Player{ID: domain.PlayerID(id), Login: login})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate players: %w", err)
	}
	return out, nil
}

// DeleteSession removes the session by token. Missing tokens are a no-op so
// idempotent logout never returns an error.
func (r *Repository) DeleteSession(ctx context.Context, token string) error {
	const q = `DELETE FROM sessions WHERE token = $1`
	if _, err := r.exec.Exec(ctx, q, token); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
