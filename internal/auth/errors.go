// Package auth handles player registration, password-based login, and
// cookie-backed session management. It exposes HTTP handlers, a RequireAuth
// middleware, and a Service that talks to Postgres via Repository.
package auth

import "errors"

var (
	// ErrInvalidCredentials is returned when login does not exist or the
	// password does not match the stored bcrypt hash.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")

	// ErrLoginTaken is returned when registration hits the players_login_key
	// unique constraint.
	ErrLoginTaken = errors.New("auth: login taken")

	// ErrSessionNotFound is returned by Repository when the session token
	// does not exist or has expired.
	ErrSessionNotFound = errors.New("auth: session not found")

	// ErrPlayerNotFound is returned by Repository when no player matches
	// the lookup.
	ErrPlayerNotFound = errors.New("auth: player not found")

	// ErrNotAuthenticated is surfaced by RequireAuth when no valid session
	// cookie is present. Handlers should not return this directly — they
	// translate it to HTTP 401 in middleware.
	ErrNotAuthenticated = errors.New("auth: not authenticated")
)
