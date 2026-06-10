package auth

import (
	"errors"
	"strings"
	"unicode/utf8"

	"spaceempire/back/internal/domain"
)

const (
	loginMinLen    = 1
	loginMaxLen    = 32
	passwordMinLen = 1
	passwordMaxLen = 72 // bcrypt hard limit
	// playableRaceMin/Max bound the races a player may pick at registration:
	// 1..5 (Argon/Boron/Paranid/Split/Teladi). 6..8 (Pirate/Xenon/Kha'ak) are
	// NPC-only — a player of those would spawn among enemies and be attacked
	// by police/everyone (phase 9.4). 0 (unset) is rejected too. See 10.10.
	playableRaceMin domain.RaceID = 1
	playableRaceMax domain.RaceID = 5
)

// RegisterRequest is the POST /api/auth/register body.
type RegisterRequest struct {
	Login    string        `json:"login"`
	Password string        `json:"password"`
	Race     domain.RaceID `json:"race"`
}

// LoginRequest is the POST /api/auth/login body.
type LoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

// PlayerResponse is the body of /me and the success body of register/login.
type PlayerResponse struct {
	PlayerID int64  `json:"playerID"`
	Login    string `json:"login,omitempty"`
}

// PlayerSelfResponse is the body of GET /api/player/me — the authenticated
// player plus their wallet and active ship. The endpoint is mounted only when
// a self reader is registered (RegisterPlayerSelf); on success Cash is always
// included (0 is a valid balance, so no omitempty). ActiveShipID is null when
// active_ship_id is unset — no omitempty so the SPA always sees the field (it
// drives the own-ship resolution; 10.14a).
type PlayerSelfResponse struct {
	PlayerID     int64  `json:"playerID"`
	Login        string `json:"login"`
	Cash         int64  `json:"cash"`
	ActiveShipID *int64 `json:"activeShipID"`
	// PassengerOfShipID is the host ship the player rides as a passenger
	// (phase 10.23), or null. No omitempty so the SPA always sees the field.
	PassengerOfShipID *int64 `json:"passengerOfShipID"`
}

// Validate performs minimal field validation. We keep it local (no
// ozzo-validation in phase 1) to match the existing minimal-deps style.
func (r RegisterRequest) Validate() error {
	if err := validateLogin(r.Login); err != nil {
		return err
	}
	if err := validatePassword(r.Password); err != nil {
		return err
	}
	if r.Race < playableRaceMin || r.Race > playableRaceMax {
		return errors.New("race must be one of 1..5 (Argon/Boron/Paranid/Split/Teladi)")
	}
	return nil
}

// Validate performs minimal field validation for login requests.
func (r LoginRequest) Validate() error {
	if err := validateLogin(r.Login); err != nil {
		return err
	}
	return validatePassword(r.Password)
}

func validateLogin(login string) error {
	login = strings.TrimSpace(login)
	if utf8.RuneCountInString(login) < loginMinLen {
		return errors.New("login is required")
	}
	if utf8.RuneCountInString(login) > loginMaxLen {
		return errors.New("login is too long")
	}
	return nil
}

func validatePassword(pwd string) error {
	if len(pwd) < passwordMinLen {
		return errors.New("password is required")
	}
	// bcrypt silently truncates at 72 bytes. Reject longer to avoid
	// surprising users who think their long passphrase is fully checked.
	if len(pwd) > passwordMaxLen {
		return errors.New("password is too long")
	}
	return nil
}
