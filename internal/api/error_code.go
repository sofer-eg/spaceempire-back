package api

import "net/http"

// Machine-readable error codes (TASK-185).
//
// Every failure answers `{"error": "<русский текст>"}`; a handful add
// `{"code": "<constant below>"}`. The code exists for exactly one reason: the
// SPA has to tell two outcomes apart that share a status and need different
// advice, and until now it did that by matching the English sentinel text
// (front/src/api.ts — jumpDriveErrorText on 409/422/400/503, installErrorText on
// 400/503). Once the messages are Russian, keying on their wording would be the
// same coupling in a new language, one rewording away from a jammed jump reading
// «вы пристыкованы» again (that regression is TASK-131).
//
// So a code is written where — and only where — the status is overloaded:
//
//	409 jump-drive         ship_docked | jump_blocked_antijump
//	422 jump-drive         jump_drive_required | shield_required
//	400 jump-drive         jump_forbidden_sector | (invalid target / bad body)
//	503 jump-drive         sector_busy | (handoff unavailable)
//	400 install-*          ship_docked | cargo_insufficient | (bad body)
//	503 install-*          sector_busy | (installer unwired)
//
// The other member of each pair stays uncoded on purpose: the SPA tests
// positively for the code it can act on and treats everything else as the
// cautious branch, so a new sentinel appearing under one of these statuses can
// never inherit «попробуйте ещё раз» by accident.
//
// Statuses that mean one thing (404, 403, 429, 504) carry no code — the SPA
// already words them from the status alone, and inventing codes for them would
// be a vocabulary nobody reads.
const (
	// CodeSectorBusy — ErrInboxFull: the command was refused at the door and
	// never reached the worker. The only retryable 503 of the set.
	CodeSectorBusy = "sector_busy"

	// CodeShipDocked — ErrShipDocked. Shares 409 with the antijump field on the
	// jump drive, and 400 with an empty hold on the install commands.
	CodeShipDocked = "ship_docked"

	// CodeJumpBlockedAntijump — ErrJumpBlockedByAntijump: a powered up_antijump
	// ship or a deployed hyper-interference generator jams the jump.
	CodeJumpBlockedAntijump = "jump_blocked_antijump"

	// CodeJumpDriveRequired — ErrEquipmentRequired on the jump drive: no
	// up_jump_drive fitted.
	CodeJumpDriveRequired = "jump_drive_required"

	// CodeShieldRequired — ErrShieldRequired: the jump drains the shield, so a
	// damaged or missing generator refuses the jump.
	CodeShieldRequired = "shield_required"

	// CodeJumpForbiddenSector — ErrJumpForbiddenSector: this sector forbids
	// jumping out, as opposed to the destination being invalid.
	CodeJumpForbiddenSector = "jump_forbidden_sector"

	// CodeCargoInsufficient — cargo.ErrInsufficientQuantity on an install: the
	// hold ran out of the satellite/generator the command deploys.
	CodeCargoInsufficient = "cargo_insufficient"
)

// writeErrorCode answers like writeError but adds the machine-readable code.
// Both shapes stay a flat JSON object, so a client that ignores `code` sees no
// change at all.
func writeErrorCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

// writeInternalError is the 500 branch of the space commands. It logs the
// server-side failure and answers the one line a player can act on.
//
// These forty branches used to hand err.Error() straight to the client, and
// that is how «publish jump event: context deadline exceeded» — or a Postgres
// message — reached the Russian combat log: ObjectActionsMenu prints the
// backend's message verbatim, into the panel and into the journal alike. The
// two commands that have their own mapper (jump-drive, install-*) already hide
// a 5xx body behind a Russian line and console.error it (TASK-157/149); the
// other fifteen had nothing, so the fix belongs here, where it covers all of
// them and stops the leak at the source rather than after it crossed the wire.
//
// The raw error is not lost — it goes to the server log, which is where a
// deadline or a Postgres error is diagnosed anyway.
func (s *Server) writeInternalError(w http.ResponseWriter, err error) {
	s.logger.Error("space command failed", "err", err)
	writeError(w, http.StatusInternalServerError, "внутренняя ошибка сервера")
}
