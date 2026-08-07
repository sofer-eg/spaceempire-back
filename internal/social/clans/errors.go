// Package clans implements player clans/alliances (phase 6.1): creation,
// invitations, membership and roles. It exposes HTTP handlers, a Service
// with the business rules (permission checks, one-clan-per-player), and a
// Repository that talks to Postgres. Relations between clans land in 6.2.
package clans

import "errors"

var (
	// ErrNameTaken / ErrTagTaken are returned by Create when the clan name
	// or tag collides with an existing one (unique constraints).
	ErrNameTaken = errors.New("клан с таким названием уже есть")
	ErrTagTaken  = errors.New("клан с таким тегом уже есть")

	// ErrAlreadyInClan is returned when a player who already belongs to a
	// clan tries to create or join another, and by Invite when the *target*
	// already belongs to one. A player is in at most one clan. The wording is
	// impersonal ("игрок", not "вы") precisely because of those two callers:
	// the player reading it is the subject in one case and the inviter in the
	// other, and one sentinel cannot say "вы" and be true in both.
	ErrAlreadyInClan = errors.New("игрок уже состоит в клане")

	// ErrClanNotFound is returned when an operation targets a clan id that
	// does not exist.
	ErrClanNotFound = errors.New("клан не найден")

	// ErrNotMember is returned when the actor is not a member of the clan
	// they try to act on (invite/kick/leave).
	ErrNotMember = errors.New("вы не состоите в этом клане")

	// ErrForbidden is returned when the actor's role does not permit the
	// operation (e.g. a plain member tries to invite or kick).
	ErrForbidden = errors.New("недостаточно прав в клане")

	// ErrTargetNotMember is returned by Kick when the target is not a member
	// of the clan.
	ErrTargetNotMember = errors.New("игрок не состоит в этом клане")

	// ErrCannotKickLeader is returned by Kick when the target is the clan
	// leader (the leader cannot be kicked).
	ErrCannotKickLeader = errors.New("нельзя исключить главу клана")

	// ErrInvalidRole is returned by SetRole for a role other than member or
	// officer (8.6 — leader is assigned at create, not via SetRole).
	ErrInvalidRole = errors.New("недопустимая роль")

	// ErrCannotChangeLeader is returned by SetRole when the target is the
	// clan leader (the leader's role is fixed; transfer is a later task).
	ErrCannotChangeLeader = errors.New("нельзя изменить роль главы клана")

	// ErrLeaderMustTransfer is returned by Leave when the leader tries to
	// leave a clan that still has other members. Leadership transfer is a
	// later task; for now the leader must remove everyone (or be the last
	// member, which disbands the clan).
	ErrLeaderMustTransfer = errors.New("глава клана должен передать управление или исключить остальных участников")

	// ErrInvitationNotFound is returned by Accept when there is no pending
	// invitation for the player to the clan.
	ErrInvitationNotFound = errors.New("приглашение не найдено")

	// ErrAlreadyInvited is returned by Invite when the target already has a
	// pending invitation to the clan.
	ErrAlreadyInvited = errors.New("игрок уже приглашён")

	// ErrInvalidInput is returned by Create when name/tag fail validation.
	ErrInvalidInput = errors.New("некорректные данные")
)
