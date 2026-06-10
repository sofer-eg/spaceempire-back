-- +goose Up
-- +goose StatementBegin
ALTER TABLE stations
    ADD COLUMN in_progress   BOOLEAN     NOT NULL DEFAULT FALSE,
    ADD COLUMN next_cycle_at TIMESTAMPTZ;

-- Seed: rebrand one of the existing stations as a microchip factory so the
-- production tick has something to do out of the box. The recipe key
-- station_type=1 is wired in configs/balance.yaml.
UPDATE stations SET type = 1 WHERE id = 1;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE stations SET type = 0 WHERE id = 1;
ALTER TABLE stations
    DROP COLUMN IF EXISTS next_cycle_at,
    DROP COLUMN IF EXISTS in_progress;
-- +goose StatementEnd
