-- +goose Up
-- NPC faction foundation (phase 9.1): a ship's race drives Race-AI hostility
-- via the race.DefaultStanding matrix (8.13). Players are race 0 (neutral);
-- NPC faction ships carry 1–8. Immutable after create.
ALTER TABLE ships ADD COLUMN race SMALLINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE ships DROP COLUMN race;
