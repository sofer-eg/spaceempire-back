-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    ADD COLUMN speed DOUBLE PRECISION NOT NULL DEFAULT 10;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    DROP COLUMN speed;
-- +goose StatementEnd
