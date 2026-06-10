-- +goose Up
-- +goose StatementBegin
ALTER TABLE ships
    DROP COLUMN auto_pilot_module;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE ships
    ADD COLUMN auto_pilot_module BOOLEAN NOT NULL DEFAULT TRUE;
-- +goose StatementEnd
