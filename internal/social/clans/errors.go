// Package clans implements player clans/alliances (phase 6.1): creation,
// invitations, membership and roles. It exposes HTTP handlers, a Service
// with the business rules (permission checks, one-clan-per-player), and a
// Repository that talks to Postgres. Relations between clans land in 6.2.
package clans

import "errors"

var (
	// ErrNameTaken / ErrTagTaken are returned by Create when the clan name
	// or tag collides with an existing one (unique constraints).
	ErrNameTaken = errors.New("clans: name already taken")
	ErrTagTaken  = errors.New("clans: tag already taken")

	// ErrAlreadyInClan is returned when a player who already belongs to a
	// clan tries to create or join another. A player is in at most one clan.
	ErrAlreadyInClan = errors.New("clans: player already in a clan")

	// ErrClanNotFound is returned when an operation targets a clan id that
	// does not exist.
	ErrClanNotFound = errors.New("clans: clan not found")

	// ErrNotMember is returned when the actor is not a member of the clan
	// they try to act on (invite/kick/leave).
	ErrNotMember = errors.New("clans: not a member of this clan")

	// ErrForbidden is returned when the actor's role does not permit the
	// operation (e.g. a plain member tries to invite or kick).
	ErrForbidden = errors.New("clans: insufficient role")

	// ErrTargetNotMember is returned by Kick when the target is not a member
	// of the clan.
	ErrTargetNotMember = errors.New("clans: target is not a member")

	// ErrCannotKickLeader is returned by Kick when the target is the clan
	// leader (the leader cannot be kicked).
	ErrCannotKickLeader = errors.New("clans: cannot kick the leader")

	// ErrInvalidRole is returned by SetRole for a role other than member or
	// officer (8.6 — leader is assigned at create, not via SetRole).
	ErrInvalidRole = errors.New("clans: invalid role")

	// ErrCannotChangeLeader is returned by SetRole when the target is the
	// clan leader (the leader's role is fixed; transfer is a later task).
	ErrCannotChangeLeader = errors.New("clans: cannot change the leader's role")

	// ErrLeaderMustTransfer is returned by Leave when the leader tries to
	// leave a clan that still has other members. Leadership transfer is a
	// later task; for now the leader must remove everyone (or be the last
	// member, which disbands the clan).
	ErrLeaderMustTransfer = errors.New("clans: leader must transfer leadership or remove members first")

	// ErrInvitationNotFound is returned by Accept when there is no pending
	// invitation for the player to the clan.
	ErrInvitationNotFound = errors.New("clans: invitation not found")

	// ErrAlreadyInvited is returned by Invite when the target already has a
	// pending invitation to the clan.
	ErrAlreadyInvited = errors.New("clans: player already invited")

	// ErrInvalidInput is returned by Create when name/tag fail validation.
	ErrInvalidInput = errors.New("clans: invalid input")
)
