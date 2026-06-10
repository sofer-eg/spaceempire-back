-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    ADD COLUMN final_target_sector BIGINT,
    ADD COLUMN final_target_x      DOUBLE PRECISION,
    ADD COLUMN final_target_y      DOUBLE PRECISION;

ALTER TABLE ships
    ADD CONSTRAINT ships_final_target_all_or_none CHECK (
        (final_target_sector IS NULL
         AND final_target_x IS NULL
         AND final_target_y IS NULL)
        OR
        (final_target_sector IS NOT NULL
         AND final_target_x IS NOT NULL
         AND final_target_y IS NOT NULL)
    );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    DROP CONSTRAINT IF EXISTS ships_final_target_all_or_none,
    DROP COLUMN IF EXISTS final_target_sector,
    DROP COLUMN IF EXISTS final_target_x,
    DROP COLUMN IF EXISTS final_target_y;
-- +goose StatementEnd
