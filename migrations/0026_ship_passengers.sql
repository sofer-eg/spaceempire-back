-- +goose Up
-- +goose StatementBegin
-- Phase 5.5: passenger count carried by a ship. NPC passenger TS board
-- passengers on departure and drop them on arrival; a non-zero count on a
-- killed ship later spills "slaves" loot (phase 5.6). Players' ships keep 0.
ALTER TABLE ships ADD COLUMN passengers INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships DROP COLUMN IF EXISTS passengers;
-- +goose StatementEnd
