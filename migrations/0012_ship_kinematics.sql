-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    RENAME COLUMN speed TO max_speed;
ALTER TABLE ships
    ADD COLUMN acceleration DOUBLE PRECISION NOT NULL DEFAULT 10,
    ADD COLUMN turn_rate    DOUBLE PRECISION NOT NULL DEFAULT 1.5707963267948966,
    ADD COLUMN facing       DOUBLE PRECISION NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    DROP COLUMN facing,
    DROP COLUMN turn_rate,
    DROP COLUMN acceleration;
ALTER TABLE ships
    RENAME COLUMN max_speed TO speed;
-- +goose StatementEnd
